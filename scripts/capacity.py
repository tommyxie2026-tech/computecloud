#!/usr/bin/env python3
"""Local Linux fixture capacity probe; standard library only, never calls a model.

Creates an isolated Server and Workers per matrix cell. Only ephemeral databases
are inspected, read-only and after execution. Not a production load generator.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import secrets
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request


FIXTURE = r'''#!/usr/bin/env python3
import json, pathlib, sys, time
if sys.argv[1:] == ['--version']:
    print('capacity-fixture-1'); sys.exit(0)
prompt = sys.stdin.read()
time.sleep(0.2)
result = 'capacity single completed'
if 'COMPUTECLOUD JOB CONTRACT:' in prompt:
    meta = json.loads(prompt.splitlines()[-1])
    if meta['stage'] == 'map':
        result = {'schema_version':'findings.v1', 'base_commit':meta['base_commit'],
                  'partition_key':meta['partition_key'], 'findings':[]}
    else:
        result = {'schema_version':'merged-report.v1', 'base_commit':meta['base_commit'],
                  'manifest_sha256':meta['manifest_sha256'], 'summary':'Empty fixture review', 'findings':[]}
    result = json.dumps(result)
if sys.argv[1] == 'exec':
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':result}}))
    print(json.dumps({'type':'turn.completed'}))
else:
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':result,'session_id':'fixture'}))
'''


def distribution(values):
    """Nearest-rank percentiles; nulls distinguish missing samples from zero."""
    ordered = sorted(values)
    if not ordered:
        return {"count": 0, "p50": None, "p95": None, "max": None}
    return {"count": len(ordered), "p50": ordered[math.ceil(len(ordered) * .50) - 1],
            "p95": ordered[math.ceil(len(ordered) * .95) - 1], "max": ordered[-1]}


def dimensions(value):
    try:
        result = [int(v) for v in value.split(",")]
    except ValueError as error:
        raise argparse.ArgumentTypeError("expected comma-separated integers") from error
    if not result or len(set(result)) != len(result) or any(v < 1 or v > 8 for v in result):
        raise argparse.ArgumentTypeError("use unique integers from 1 through 8")
    return result


def process_sample(pid):
    # /proc stat comm may contain whitespace or closing parentheses.
    fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
    return ((int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
            int(fields[21]) * os.sysconf("SC_PAGE_SIZE"))


def cgroup_limits():
    # Record the process's visible cgroup v2 limits, not a claim of dedicated CPUs.
    result = {}
    for name in ["cpu.max", "memory.max"]:
        try:
            result[name] = (Path("/sys/fs/cgroup") / name).read_text().strip()
        except OSError:
            result[name] = None
    return result


class Sampler:
    def __init__(self, processes, wal):
        self.processes, self.wal = processes, wal
        self.stop = threading.Event()
        self.peak = {name: 0 for name in processes}
        self.initial = {name: process_sample(p.pid)[0] for name, p in processes.items()}
        self.cpu = dict(self.initial)
        self.wal_peak = 0
        self.samples = 0
        self.thread = threading.Thread(target=self.run, daemon=True)

    def sample(self):
        self.samples += 1
        for name, process in self.processes.items():
            try:
                cpu, rss = process_sample(process.pid)
                self.cpu[name] = cpu
                self.peak[name] = max(self.peak[name], rss)
            except (OSError, ValueError, IndexError):
                pass
        try:
            self.wal_peak = max(self.wal_peak, self.wal.stat().st_size)
        except FileNotFoundError:
            pass

    def run(self):
        while not self.stop.is_set():
            self.sample()
            self.stop.wait(.1)

    def finish(self):
        self.stop.set()
        self.thread.join()
        self.sample()
        return {"sample_interval_ms": 100, "samples": self.samples,
                "wal_peak_bytes": self.wal_peak,
                "processes": {name: {"peak_rss_bytes": self.peak[name],
                                     "cpu_seconds": round(self.cpu[name] - self.initial[name], 4)}
                              for name in self.processes}}


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError(f"{args[:2]} failed: {result.stderr[-2000:]}")
    return result.stdout.strip()


def free_address():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{sock.getsockname()[1]}"


def collect_database(path):
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
        integrity = db.execute("PRAGMA integrity_check").fetchone()[0]
        if integrity != "ok":
            raise RuntimeError("database integrity check failed")
        created = dict(db.execute("SELECT id,created FROM tasks"))
        queue = []
        for recorded, body in db.execute("SELECT recorded_at,body FROM job_events WHERE type='job.task_state'"):
            event = json.loads(body)
            if event["to"] == "STARTING":
                queue.append(recorded - created[event["task_id"]])
        counts = dict(db.execute("SELECT state,count(*) FROM tasks GROUP BY state"))
        assigned = dict(db.execute("SELECT worker,count(*) FROM tasks GROUP BY worker"))
        artifacts = db.execute("SELECT count(*),coalesce(sum(size),0) FROM artifacts").fetchone()
        return {"integrity": integrity, "task_states": counts, "tasks_by_worker": assigned,
                "task_queue_ms": distribution(queue), "artifact_count": artifacts[0],
                "artifact_stored_bytes": artifacts[1],
                "task_events": db.execute("SELECT count(*) FROM events").fetchone()[0],
                "job_events": db.execute("SELECT count(*) FROM job_events").fetchone()[0],
                "sqlite_write_latency_ms": None}


def cell(binary, worker_count, slots, jobs, submitters, timeout):
    result = {"workers": worker_count, "slots_per_worker": slots, "jobs_requested": jobs,
              "submitters": submitters, "status": "FAILED"}
    processes, logs = {}, []
    sampler = None
    with tempfile.TemporaryDirectory(prefix="computecloud-capacity-") as temporary:
        root = Path(temporary)

        def write(name, value):
            path = root / name
            path.write_text(value if isinstance(value, str) else json.dumps(value))
            path.chmod(0o600)
            return str(path)

        def start(name, role, config):
            log = open(root / (name + ".log"), "w")
            logs.append(log)
            processes[name] = subprocess.Popen([binary, role, "--config", config], stdout=log, stderr=log)

        try:
            repo = root / "repository"
            repo.mkdir()
            run("git", "-C", str(repo), "init")
            (repo / "README.md").write_text("Capacity fixture repository\n")
            run("git", "-C", str(repo), "add", ".")
            run("git", "-C", str(repo), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
            commit = run("git", "-C", str(repo), "rev-parse", "HEAD")
            fixture = Path(write("agent-fixture", FIXTURE))
            fixture.chmod(0o700)
            address, http_address = free_address(), free_address()
            while http_address == address:
                http_address = free_address()
            tls = {"insecure_loopback": True}
            token = secrets.token_hex(32)
            user_file = write("user.token", token)
            identities, configs = [], []
            for i in range(worker_count):
                name = f"worker-{i}"
                token_file = write(name + ".token", secrets.token_hex(32))
                identities.append({"worker_id": name, "token_file": token_file, "projects": ["capacity"], "credentials": ["fixture"]})
                config = {"worker": {"id": name, "server_address": address, "data_dir": str(root / name),
                    "token_file": token_file, "tls": tls, "slots": slots, "stop_grace_ms": 100,
                    "runtimes": {profile: {"executable": str(fixture), "version": "capacity-fixture-1",
                        "models": [model], "credentials": ["fixture"]}
                        for profile, model in [("codex_exec", "fixture-codex"), ("claude_print", "fixture-claude")]},
                    "repositories": {"repo": str(repo)},
                    "policies": {"review": {"codex_sandbox": "read-only", "claude_permission_mode": "dontAsk"}},
                    "verifiers": {"check": [["test", "-f", "README.md"]]}}}
                configs.append((name, write(name + ".json", config)))
            templates = json.loads(run(binary, "templates", "--config", configs[0][1]))["templates"]
            server = write("server.json", {"server": {"listen": address, "http": {"listen": http_address},
                "data_dir": str(root / "server"), "tls": tls, "tick_ms": 50, "lease_seconds": 15,
                "max_project_tasks": worker_count * slots, "credentials": {"fixture": worker_count * slots},
                "jobs": {"enabled": True, "templates": templates}, "workers": identities,
                "users": [{"token_file": user_file, "owner": "fixture", "projects": ["capacity"],
                    "credentials": ["fixture"], "scopes": ["jobs:submit", "jobs:read"]}]}})
            client = write("client.json", {"client": {"address": address, "token_file": user_file, "tls": tls}})
            # Never use an ambient HTTP proxy for ephemeral loopback test credentials.
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

            def request(path, body=None, key=None):
                headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
                if key:
                    headers["Idempotency-Key"] = key
                req = urllib.request.Request("http://" + http_address + path,
                    data=json.dumps(body).encode() if body is not None else None, headers=headers)
                with opener.open(req, timeout=min(timeout, 20)) as response:
                    return response.read()

            start("server", "server", server)
            for name, config in configs:
                start(name, "worker", config)
            ready_deadline = time.monotonic() + 30
            while True:
                if any(p.poll() is not None for p in processes.values()):
                    raise RuntimeError("daemon exited during startup")
                try:
                    workers = json.loads(run(binary, "workers", "--config", client)).get("workers", [])
                    if sum(bool(w.get("online")) for w in workers) == worker_count:
                        request("/v1/capabilities")
                        break
                except (RuntimeError, OSError):
                    pass
                if time.monotonic() > ready_deadline:
                    raise TimeoutError("worker registration timed out")
                time.sleep(.1)
            sampler = Sampler(processes, root / "server/state.db-wal")
            sampler.thread.start()
            started = time.monotonic()
            deadline = started + timeout

            def execution(engine):
                return {"engine": engine, "runtime_profile": "codex_exec" if engine == "codex" else "claude_print",
                        "model": "fixture-" + engine, "credential_ref": "fixture", "policy_ref": "review", "acceptance_profile": "check"}

            def submit(index):
                if time.monotonic() >= deadline:
                    raise TimeoutError("workload deadline reached during submission")
                spec = {"schema_version": "v0.2", "project_id": "capacity", "mode": "single",
                        "workspace": {"repository_ref": "repo", "base_commit": commit}, "input": {"text": "fixture capacity"},
                        "limits": {"timeout_seconds": timeout, "max_attempts_per_task": 1}}
                if index % 2 == 0:
                    spec["execution"] = execution("codex" if index % 4 == 0 else "claude")
                else:
                    spec["mode"] = "map_reduce"
                    spec["map"] = {"parallelism": 2, "partitions": [
                        {"key": key, "scope_paths": ["README.md"], "input": {"text": "review"}, "execution": execution(engine)}
                        for key, engine in [("a", "codex"), ("b", "claude")]]}
                    spec["reduce"] = {"strategy": "report_merge_v1", "input": {"text": "merge"}, "execution": execution("codex")}
                before = time.monotonic()
                answer = json.loads(request("/v1/jobs", spec, f"capacity-{index}"))
                return answer["job_id"], (time.monotonic() - before) * 1000

            with ThreadPoolExecutor(max_workers=submitters) as pool:
                submitted = list(pool.map(submit, range(jobs)))
            result["submit_http_ms"] = distribution([ms for _, ms in submitted])
            pending = {jid for jid, _ in submitted}
            terminal = {}
            while pending:
                if time.monotonic() > deadline:
                    raise TimeoutError(f"{len(pending)} Jobs still pending at workload deadline")
                if any(p.poll() is not None for p in processes.values()):
                    raise RuntimeError("daemon exited during workload")
                for jid in list(pending):
                    job = json.loads(request("/v1/jobs/" + jid))
                    if job["state"] in {"SUCCEEDED", "FAILED", "CANCELED"}:
                        pending.remove(jid)
                        terminal[jid] = job
                if pending:
                    time.sleep(.1)
            elapsed = time.monotonic() - started
            result.update(workload_seconds=elapsed, jobs_per_second=jobs / elapsed,
                          job_completion_ms=distribution([int(j["updated_at_ms"]) - int(j["created_at_ms"]) for j in terminal.values()]))
            failures = {jid: j.get("error_code", j["state"]) for jid, j in terminal.items() if j["state"] != "SUCCEEDED"}
            if failures:
                raise RuntimeError(f"Jobs did not succeed: {failures}")
            # Separate post-workload readback, not execution throughput or network capacity.
            read_started = time.monotonic()
            event_count, artifact_bytes, checked_artifacts = 0, 0, 0
            for jid in terminal:
                cursor = 0
                while True:
                    page = json.loads(request(f"/v1/jobs/{jid}/events?after_seq={cursor}&limit=7"))
                    seqs = [int(e["seq"]) for e in page["events"]]
                    if seqs != list(range(cursor + 1, cursor + 1 + len(seqs))):
                        raise RuntimeError("event sequence is not contiguous")
                    event_count += len(seqs)
                    cursor = int(page["next_seq"])
                    if not page["has_more"]:
                        break
                    if not seqs:
                        raise RuntimeError("event pagination did not advance")
                if cursor != int(terminal[jid]["last_seq"]):
                    raise RuntimeError("event readback did not reach terminal sequence")
                final = json.loads(request(f"/v1/jobs/{jid}/result"))
                if len(final["final_artifacts"]) != 1:
                    raise RuntimeError("expected exactly one final fixture bundle")
                for artifact in final["final_artifacts"]:
                    data = request(f"/v1/jobs/{jid}/artifacts/{artifact['artifact_id']}")
                    if hashlib.sha256(data).hexdigest() != artifact["sha256"]:
                        raise RuntimeError("artifact checksum mismatch")
                    artifact_bytes += len(data)
                    checked_artifacts += 1
            read_seconds = time.monotonic() - read_started
            result["readback"] = {"seconds": read_seconds, "events": event_count,
                "checked_final_artifacts": checked_artifacts, "artifact_bytes": artifact_bytes,
                "events_per_second_combined": event_count / read_seconds,
                "artifact_bytes_per_second_combined": artifact_bytes / read_seconds}
            result["resources"] = sampler.finish()
            sampler = None
            result["database"] = collect_database(root / "server/state.db")
            expected_tasks = jobs + 2 * (jobs // 2)
            if result["database"]["task_states"] != {"SUCCEEDED": expected_tasks}:
                raise RuntimeError("unexpected task count/state")
            if result["database"]["task_queue_ms"]["count"] != expected_tasks:
                raise RuntimeError("missing task dispatch timing")
            result["tasks_per_second"] = expected_tasks / elapsed
            result["status"] = "PASSED"
        except Exception as error:
            result["error"] = f"{type(error).__name__}: {error}"
            result["log_tails"] = {p.stem: p.read_text(errors="replace")[-3000:] for p in root.glob("*.log")}
        finally:
            if sampler:
                result["resources"] = sampler.finish()
            # Stop Workers first so their supervisors can reap fixture children.
            for name in list(reversed(processes)):
                process = processes[name]
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
                    try:
                        process.wait(timeout=20)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                        result["status"] = "FAILED"
                        result.setdefault("cleanup_errors", []).append(name + " required SIGKILL")
            for log in logs:
                log.close()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/computecloud")
    parser.add_argument("--workers", type=dimensions, default=[1, 2, 4, 8])
    parser.add_argument("--slots", type=dimensions, default=[1, 2, 4])
    parser.add_argument("--jobs", type=int, default=32)
    parser.add_argument("--submitters", type=int, default=4)
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--output", default="dist/capacity.json")
    args = parser.parse_args()
    if not (2 <= args.jobs <= 4096 and 1 <= args.submitters <= 32 and 10 <= args.timeout <= 3600):
        parser.error("jobs must be 2..4096, submitters 1..32, timeout 10..3600")
    if sys.platform != "linux":
        parser.error("Linux /proc is required")
    binary = str(Path(args.binary).resolve())
    report = {"schema_version": "capacity.v1", "timestamp": datetime.now(timezone.utc).isoformat(),
        "environment": {"platform": platform.platform(), "python": platform.python_version(),
                        "cpu_count": os.cpu_count(), "visible_cgroup_v2_limits": cgroup_limits(),
                        "binary_version": run(binary, "version"),
                        "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest()},
        "measurement_notes": ["Same-host fixture only; no real CLI/model/provider, TLS or independent hosts.",
            "Half single Jobs, half two-Map report Jobs; mixed Codex/Claude protocol fixtures sleep 200ms per Task.",
            "Submit HTTP time includes client/network/auth/validation/queue/transaction; it is not SQLite write latency.",
            "Task queue time: task creation to persisted STARTING; completion: Job created to terminal timestamp.",
            "RSS sampled every 100ms; CPU seconds and RSS cover daemons only, excluding Git/fixture/verifier children.",
            "Resources include workload and readback; exclude startup. WAL peak is file size, not write volume.",
            "Readback rates share one interval (events plus final artifacts); not independent maximum bandwidth.",
            "SQLite transaction latency is uninstrumented/null; no direct writes to application tables.",
            "One cold ephemeral database per cell; no steady-state, repeated-trial or saturation claim."], "cells": []}
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    # Refuse accidental overwrite of prior evidence or unrelated files.
    with output.open("x") as stream:
        for workers in args.workers:
            for slots in args.slots:
                result = cell(binary, workers, slots, args.jobs, args.submitters, args.timeout)
                report["cells"].append(result)
                stream.seek(0)
                json.dump(report, stream, indent=2)
                stream.write("\n")
                stream.truncate()
                stream.flush()
                os.fsync(stream.fileno())
                print(f"{result['status']}: workers={workers} slots={slots} jobs={args.jobs}", flush=True)
                if result["status"] != "PASSED":
                    print(result.get("error", result.get("cleanup_errors")), file=sys.stderr)
                    return 1
    print(f"Report: {output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
