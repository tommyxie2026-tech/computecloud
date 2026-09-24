#!/usr/bin/env python3
"""CI end-to-end task flow using protocol fixtures; never calls a model provider."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time


FIXTURE = r'''#!/usr/bin/env python3
import json, os, pathlib, sys, time
if sys.argv[1:] == ['--version']:
    print('ci-flow-fixture-1'); sys.exit(0)
prompt = sys.stdin.read()
if 'slow fixture' in prompt:
    pathlib.Path('fixture.pid').write_text(str(os.getpid()))
    time.sleep(90)
result = 'ci fixture single completed'
if 'COMPUTECLOUD JOB CONTRACT:' in prompt:
    meta = json.loads(prompt.splitlines()[-1])
    if meta['stage'] == 'map':
        result = {'schema_version':'findings.v1', 'base_commit':meta['base_commit'],
                  'partition_key':meta['partition_key'], 'findings':[]}
    else:
        result = {'schema_version':'merged-report.v1', 'base_commit':meta['base_commit'],
                  'manifest_sha256':meta['manifest_sha256'],
                  'summary':'CI fixture merged review', 'findings':[]}
    result = json.dumps(result)
if sys.argv[1] == 'exec':
    print(json.dumps({'type':'thread.started','thread_id':'ci-flow'}))
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':result}}))
    print(json.dumps({'type':'turn.completed'}))
else:
    print(json.dumps({'type':'system','session_id':'ci-flow'}))
    print(json.dumps({'type':'result','subtype':'success','is_error':False,
                      'result':result,'session_id':'ci-flow'}))
'''


TERMINAL = {"SUCCEEDED", "FAILED", "CANCELED"}


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def free_address():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{sock.getsockname()[1]}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/computecloud")
    parser.add_argument("--output", default="dist/ci-task-flow/report.json")
    parser.add_argument("--timeout", type=int, default=45)
    args = parser.parse_args()
    if args.timeout < 20 or args.timeout > 300:
        parser.error("timeout must be 20..300 seconds")

    binary = str(Path(args.binary).resolve())
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    logs_output = output.parent / "logs"
    report = {
        "schema_version": "ci-task-flow.v1",
        "started_at": utc_now(),
        "environment": {
            "binary_version": "unknown",
            "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
            "fixture": "ci-flow-fixture-1",
            "real_model_calls": False,
        },
        "topology": {"servers": 1, "workers": 2, "slots_per_worker": 1, "storage": "ephemeral SQLite"},
        "steps": [],
        "summary": {"status": "FAILED"},
    }
    processes, log_handles = {}, []
    root = None
    failure = None

    def run_command(*command, timeout=30):
        environment = os.environ.copy()
        for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]:
            environment.pop(name, None)
        environment["NO_PROXY"] = "127.0.0.1,localhost"
        result = subprocess.run(command, text=True, capture_output=True, timeout=timeout, env=environment)
        if result.returncode:
            raise RuntimeError(f"command failed ({' '.join(command[:3])}): {result.stderr[-2000:]}")
        return result.stdout.strip()

    def write(name, value):
        path = root / name
        path.write_text(value if isinstance(value, str) else json.dumps(value))
        path.chmod(0o600)
        return str(path)

    def cli(*command, timeout=30):
        return run_command(binary, *command, "--config", str(root / "client.json"), timeout=timeout)

    def start(name, role, config):
        log = open(root / f"{name}.log", "a")
        log_handles.append(log)
        processes[name] = subprocess.Popen([binary, role, "--config", str(config)], stdout=log, stderr=log)

    def stop(name, hard=False):
        process = processes.get(name)
        if not process or process.poll() is not None:
            return
        process.send_signal(signal.SIGKILL if hard else signal.SIGTERM)
        process.wait(timeout=15)

    def wait_for(action, timeout=None):
        deadline = time.monotonic() + (timeout or args.timeout)
        last = None
        while time.monotonic() < deadline:
            try:
                answer = action()
                if answer:
                    return answer
            except (KeyError, RuntimeError, json.JSONDecodeError) as error:
                last = error
            if any(process.poll() is not None for process in processes.values()):
                exited = {name: process.returncode for name, process in processes.items() if process.poll() is not None}
                raise RuntimeError(f"daemon exited unexpectedly: {exited}")
            time.sleep(0.1)
        raise TimeoutError(f"condition timed out; last error: {last}")

    def record(name, action):
        step = {"name": name, "started_at": utc_now(), "status": "FAILED"}
        report["steps"].append(step)
        started = time.monotonic()
        try:
            evidence = action() or {}
            step.update(status="PASSED", evidence=evidence)
            return evidence
        except BaseException as error:
            step["error"] = f"{type(error).__name__}: {error}"
            raise
        finally:
            step["duration_ms"] = round((time.monotonic() - started) * 1000, 1)
            step["finished_at"] = utc_now()

    def job(job_id):
        return json.loads(cli("job", "get", "--id", job_id))

    def job_tasks(job_id):
        return json.loads(cli("job", "tasks", "--id", job_id, "--limit", "100"))["tasks"]

    def wait_job(job_id, states=TERMINAL):
        return wait_for(lambda: (current if (current := job(job_id))["state"] in states else None))

    def wait_task(job_id, states):
        return wait_for(lambda: next((task for task in job_tasks(job_id) if task["state"] in states), None))

    def submit(key, spec):
        spec_file = write(f"{key}.json", spec)
        return json.loads(cli("job", "submit", "--key", key, "--file", spec_file))["job_id"]

    def events(job_id):
        cursor, collected = 0, []
        while True:
            page = json.loads(cli("job", "events", "--id", job_id, "--after", str(cursor), "--limit", "100"))
            sequence = [int(event["seq"]) for event in page["events"]]
            expected = list(range(cursor + 1, cursor + 1 + len(sequence)))
            if sequence != expected:
                raise AssertionError(f"non-contiguous events: {sequence} expected {expected}")
            collected.extend(page["events"])
            cursor = int(page["next_seq"])
            if not page["has_more"]:
                break
        current = job(job_id)
        if cursor != int(current["last_seq"]):
            raise AssertionError("event readback did not reach terminal sequence")
        return collected

    def final_artifact(job_id, filename):
        result = json.loads(cli("job", "result", "--id", job_id))
        artifacts = result["final_artifacts"]
        if len(artifacts) != 1:
            raise AssertionError(f"expected one final artifact, got {len(artifacts)}")
        artifact = artifacts[0]
        destination = root / filename
        cli("job", "download", "--id", job_id, "--artifact", artifact["artifact_id"], "--out", str(destination), timeout=60)
        digest = hashlib.sha256(destination.read_bytes()).hexdigest()
        if digest != artifact["sha256"]:
            raise AssertionError("downloaded artifact checksum mismatch")
        with tarfile.open(destination) as archive:
            members = sorted(archive.getnames())
        return {"artifact_id": artifact["artifact_id"], "sha256": digest,
                "size": destination.stat().st_size, "members": members}

    def base_spec(text="CI fixture task"):
        return {"schema_version": "v0.2", "project_id": "ci-flow", "mode": "single",
                "workspace": {"repository_ref": "repo", "base_commit": commit},
                "input": {"text": text}, "execution": execution("codex"),
                "limits": {"timeout_seconds": 120, "max_attempts_per_task": 1}}

    def execution(engine):
        return {"engine": engine, "runtime_profile": "codex_exec" if engine == "codex" else "claude_print",
                "model": f"fixture-{engine}", "credential_ref": "fixture", "policy_ref": "review",
                "acceptance_profile": "check"}

    def child_stopped(task):
        pid_file = root / task["worker_id"] / "workspaces" / task["attempt_id"] / "fixture.pid"
        wait_for(lambda: pid_file.exists())
        pid = pid_file.read_text().strip()
        stat = Path("/proc") / pid / "stat"
        wait_for(lambda: not stat.exists() or ") Z " in stat.read_text())
        return pid

    try:
        report["environment"]["binary_version"] = run_command(binary, "version")
        with tempfile.TemporaryDirectory(prefix="computecloud-ci-flow-") as temporary:
            root = Path(temporary)
            repo = root / "repository"
            repo.mkdir()
            run_command("git", "-C", str(repo), "init")
            (repo / "README.md").write_text("CI task-flow fixture repository\n")
            run_command("git", "-C", str(repo), "add", ".")
            run_command("git", "-C", str(repo), "-c", "user.name=CI Fixture", "-c",
                        "user.email=ci@example.invalid", "commit", "-m", "fixture")
            commit = run_command("git", "-C", str(repo), "rev-parse", "HEAD")
            fixture = Path(write("agent-fixture", FIXTURE))
            fixture.chmod(0o700)
            address, http_address = free_address(), free_address()
            while address == http_address:
                http_address = free_address()
            tls = {"insecure_loopback": True}
            user_token = write("user.token", secrets.token_hex(32))
            worker_configs, worker_identities = {}, []
            for name in ["worker-a", "worker-b"]:
                token = write(f"{name}.token", secrets.token_hex(32))
                worker_identities.append({"worker_id": name, "token_file": token,
                                          "projects": ["ci-flow"], "credentials": ["fixture"]})
                config = {"worker": {"id": name, "server_address": address,
                    "data_dir": str(root / name), "token_file": token, "tls": tls, "slots": 1,
                    "stop_grace_ms": 100, "repositories": {"repo": str(repo)},
                    "runtimes": {profile: {"executable": str(fixture), "version": "ci-flow-fixture-1",
                        "models": [model], "credentials": ["fixture"]}
                        for profile, model in [("codex_exec", "fixture-codex"),
                                               ("claude_print", "fixture-claude")]},
                    "policies": {"review": {"codex_sandbox": "read-only",
                                               "claude_permission_mode": "dontAsk",
                                               "claude_allowed_tools": ["Read", "Glob", "Grep"]}},
                    "verifiers": {"check": [["test", "-f", "README.md"]]}}}
                worker_configs[name] = write(f"{name}.json", config)
            templates = json.loads(run_command(binary, "templates", "--config", worker_configs["worker-a"]))["templates"]
            server_config = write("server.json", {"server": {"listen": address,
                "http": {"listen": http_address}, "data_dir": str(root / "server"), "tls": tls,
                "tick_ms": 50, "lease_seconds": 4, "max_project_tasks": 2,
                "credentials": {"fixture": 2}, "jobs": {"enabled": True, "max_parallelism": 2,
                    "recommended_parallelism": 2, "templates": templates},
                "users": [{"token_file": user_token, "owner": "ci", "projects": ["ci-flow"],
                    "credentials": ["fixture"], "scopes": ["jobs:submit", "jobs:read", "jobs:cancel"]}],
                "workers": worker_identities}})
            write("client.json", {"client": {"address": address, "http_url": "http://" + http_address,
                                               "token_file": user_token, "tls": tls}})

            def cluster_ready():
                start("server", "server", server_config)
                for worker_name in worker_configs:
                    start(worker_name, "worker", worker_configs[worker_name])
                workers = wait_for(lambda: (online if len(online := [item for item in json.loads(cli("workers")).get("workers", []) if item.get("online")]) == 2 else None))
                capabilities = json.loads(cli("job", "capabilities"))
                if set(capabilities["job_modes"]) != {"single", "map_reduce"}:
                    raise AssertionError("expected single and map_reduce capabilities")
                return {"workers": [worker["worker_id"] for worker in workers],
                        "job_modes": capabilities["job_modes"], "templates": len(templates)}

            record("cluster_ready", cluster_ready)

            def single_flow():
                spec = base_spec()
                job_id = submit("ci-single", spec)
                if submit("ci-single", spec) != job_id:
                    raise AssertionError("idempotent submission returned a different Job ID")
                current = wait_job(job_id)
                if current["state"] != "SUCCEEDED":
                    raise AssertionError(f"single Job failed: {current}")
                task_list = job_tasks(job_id)
                if len(task_list) != 1 or task_list[0]["state"] != "SUCCEEDED":
                    raise AssertionError(f"unexpected single tasks: {task_list}")
                return {"job_id": job_id, "state": current["state"],
                        "worker_id": task_list[0]["worker_id"], "event_count": len(events(job_id)),
                        "artifact": final_artifact(job_id, "single-result.tar")}

            record("single_job", single_flow)

            def map_reduce_flow():
                spec = base_spec("Review the fixture repository")
                spec.pop("execution")
                spec["mode"] = "map_reduce"
                spec["map"] = {"parallelism": 2, "partitions": [
                    {"key": "codex-review", "scope_paths": ["README.md"],
                     "input": {"text": "Review with Codex fixture"}, "execution": execution("codex")},
                    {"key": "claude-review", "scope_paths": ["README.md"],
                     "input": {"text": "Review with Claude fixture"}, "execution": execution("claude")} ]}
                spec["reduce"] = {"strategy": "report_merge_v1", "input": {"text": "Merge findings"},
                                  "execution": execution("codex")}
                job_id = submit("ci-map-reduce", spec)
                current = wait_job(job_id)
                if current["state"] != "SUCCEEDED":
                    raise AssertionError(f"Map/Reduce Job failed: {current}")
                task_list = job_tasks(job_id)
                if len(task_list) != 3 or any(task["state"] != "SUCCEEDED" for task in task_list):
                    raise AssertionError(f"unexpected Map/Reduce tasks: {task_list}")
                stages = {stage: sum(task["stage"] == stage for task in task_list) for stage in ["map", "reduce"]}
                if stages != {"map": 2, "reduce": 1}:
                    raise AssertionError(f"unexpected stage counts: {stages}")
                map_workers = {task["worker_id"] for task in task_list if task["stage"] == "map"}
                if len(map_workers) != 2:
                    raise AssertionError(f"Map partitions did not span both Workers: {map_workers}")
                return {"job_id": job_id, "state": current["state"], "stage_counts": stages,
                        "assignments": [{"stage": task["stage"], "partition_key": task["partition_key"],
                                         "worker_id": task["worker_id"]} for task in task_list],
                        "event_count": len(events(job_id)),
                        "artifact": final_artifact(job_id, "map-reduce-result.tar")}

            record("map_reduce_job", map_reduce_flow)

            def cancel_flow():
                job_id = submit("ci-cancel", base_spec("slow fixture cancellation"))
                running = wait_task(job_id, {"RUNNING"})
                wait_for(lambda: (root / running["worker_id"] / "workspaces" / running["attempt_id"] / "fixture.pid").exists())
                json.loads(cli("job", "cancel", "--id", job_id, "--control-id", "ci-cancel-once"))
                current = wait_job(job_id)
                if current["state"] != "CANCELED":
                    raise AssertionError(f"cancel did not win: {current}")
                pid = child_stopped(running)
                return {"job_id": job_id, "state": current["state"], "worker_id": running["worker_id"],
                        "attempt_id": running["attempt_id"], "stopped_pid": pid,
                        "event_count": len(events(job_id))}

            record("cancel_and_cleanup", cancel_flow)

            def worker_failure_flow():
                job_id = submit("ci-worker-failure", base_spec("slow fixture worker failure"))
                running = wait_task(job_id, {"RUNNING"})
                wait_for(lambda: (root / running["worker_id"] / "workspaces" / running["attempt_id"] / "fixture.pid").exists())
                worker_name = running["worker_id"]
                stop(worker_name, hard=True)
                start(worker_name, "worker", worker_configs[worker_name])
                current = wait_job(job_id)
                if current["state"] != "FAILED" or current.get("error_code") not in {"WORKER_LOST", "WORKER_RESTARTED"}:
                    raise AssertionError(f"unexpected Worker failure result: {current}")
                pid = child_stopped(running)
                return {"job_id": job_id, "state": current["state"], "error_code": current["error_code"],
                        "worker_id": worker_name, "attempt_id": running["attempt_id"],
                        "stopped_pid": pid, "event_count": len(events(job_id))}

            record("worker_failure_recovery", worker_failure_flow)

            def shutdown_and_integrity():
                for worker_name in worker_configs:
                    stop(worker_name)
                stop("server")
                with sqlite3.connect(root / "server" / "state.db") as database:
                    integrity = database.execute("PRAGMA integrity_check").fetchone()[0]
                    jobs = dict(database.execute("SELECT state,count(*) FROM jobs GROUP BY state"))
                    tasks = dict(database.execute("SELECT state,count(*) FROM tasks GROUP BY state"))
                if integrity != "ok":
                    raise AssertionError("SQLite integrity check failed")
                expected_jobs = {"CANCELED": 1, "FAILED": 1, "SUCCEEDED": 2}
                if jobs != expected_jobs:
                    raise AssertionError(f"unexpected Job state totals: {jobs}")
                return {"sqlite_integrity": integrity, "job_states": jobs, "task_states": tasks}

            record("shutdown_and_integrity", shutdown_and_integrity)
            report["summary"] = {"status": "PASSED", "steps_passed": len(report["steps"]),
                                 "successful_jobs": 2, "canceled_jobs": 1, "failed_jobs": 1}
    except BaseException as error:
        failure = error
        report["summary"] = {"status": "FAILED", "error": f"{type(error).__name__}: {error}"}
    finally:
        for name in list(reversed(processes)):
            try:
                stop(name)
            except BaseException as error:
                report.setdefault("cleanup_errors", []).append(f"{name}: {error}")
        for handle in log_handles:
            handle.close()
        if root and root.exists():
            logs_output.mkdir(parents=True, exist_ok=True)
            for source in root.glob("*.log"):
                shutil.copy2(source, logs_output / source.name)
            if failure:
                report["log_tails"] = {source.name: source.read_text(errors="replace")[-4000:]
                                       for source in root.glob("*.log")}
        report["finished_at"] = utc_now()
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print(json.dumps(report["summary"], sort_keys=True), flush=True)
        print(f"Report: {output}", flush=True)
    if failure:
        raise failure


if __name__ == "__main__":
    main()
