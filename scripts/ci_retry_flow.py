#!/usr/bin/env python3
"""CI Retry Safety flow using local protocol fixtures; never calls a model provider."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import sqlite3
import subprocess
import tempfile
import time


FIXTURE = r'''#!/usr/bin/env python3
import json, os, pathlib, sys

if sys.argv[1:] == ['--version']:
    print('ci-retry-fixture-1')
    sys.exit(0)

prompt = sys.stdin.read()
marker = pathlib.Path(os.environ.get('CI_RETRY_MARKER', 'retry.marker'))

if 'verification failure' in prompt:
    pathlib.Path('README.md').unlink(missing_ok=True)

result = 'ci retry fixture completed'
print(json.dumps({'type':'thread.started','thread_id':'ci-retry'}))
print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':result}}))
print(json.dumps({'type':'turn.completed'}))

if 'retry once' in prompt and not marker.exists():
    marker.write_text('failed-once')
    sys.exit(1)
if 'always runtime fail' in prompt:
    sys.exit(1)
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
    parser.add_argument("--output", default="dist/ci-retry-flow/report.json")
    parser.add_argument("--timeout", type=int, default=45)
    args = parser.parse_args()

    binary = str(Path(args.binary).resolve())
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    log_dir = output.parent / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    report = {
        "schema_version": "ci-retry-flow.v1",
        "started_at": utc_now(),
        "environment": {
            "binary_version": subprocess.check_output([binary, "version"], text=True).strip(),
            "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
            "fixture": "ci-retry-fixture-1",
            "real_model_calls": False,
        },
        "steps": [],
        "summary": {"status": "FAILED"},
    }
    processes, logs = {}, []
    failure = None
    root = None

    def write(name, value):
        path = root / name
        path.write_text(value if isinstance(value, str) else json.dumps(value))
        path.chmod(0o600)
        return str(path)

    def run(*cmd, timeout=30, check=True):
        env = os.environ.copy()
        for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]:
            env.pop(name, None)
        env["NO_PROXY"] = "127.0.0.1,localhost"
        result = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout, env=env)
        if check and result.returncode:
            raise RuntimeError(f"command failed ({' '.join(cmd[:3])}): {result.stderr[-2000:]}")
        return result

    def cli(*cmd, timeout=30, check=True):
        return run(binary, *cmd, "--config", str(root / "client.json"), timeout=timeout, check=check)

    def start(name, role, config):
        log = open(log_dir / f"{name}.log", "a")
        logs.append(log)
        processes[name] = subprocess.Popen([binary, role, "--config", str(config)], stdout=log, stderr=log)

    def stop(name, hard=False):
        process = processes.get(name)
        if not process or process.poll() is not None:
            return
        process.send_signal(signal.SIGKILL if hard else signal.SIGTERM)
        process.wait(timeout=15)

    def wait_for(fn, timeout=None):
        end = time.monotonic() + (timeout or args.timeout)
        last = None
        while time.monotonic() < end:
            try:
                value = fn()
                if value:
                    return value
            except (RuntimeError, KeyError, json.JSONDecodeError, sqlite3.OperationalError) as exc:
                last = exc
            exited = {name: proc.returncode for name, proc in processes.items() if proc.poll() is not None}
            if exited:
                raise RuntimeError(f"daemon exited unexpectedly: {exited}")
            time.sleep(0.1)
        raise TimeoutError(f"condition timed out; last error: {last}")

    def record(name, fn):
        item = {"name": name, "started_at": utc_now(), "status": "FAILED"}
        report["steps"].append(item)
        started = time.monotonic()
        try:
            evidence = fn() or {}
            item.update(status="PASSED", evidence=evidence)
            return evidence
        except BaseException as exc:
            item["error"] = f"{type(exc).__name__}: {exc}"
            raise
        finally:
            item["duration_ms"] = round((time.monotonic() - started) * 1000, 1)
            item["finished_at"] = utc_now()

    def execution(replay_safe=True):
        return {
            "engine": "codex",
            "runtime_profile": "codex_exec",
            "model": "fixture-codex",
            "credential_ref": "fixture",
            "policy_ref": "review",
            "acceptance_profile": "check",
            "replay_safe": replay_safe,
        }

    def spec(text, attempts=2, replay_safe=True):
        return {
            "schema_version": "v0.2",
            "project_id": "ci-retry",
            "mode": "single",
            "workspace": {"repository_ref": "repo", "base_commit": commit},
            "input": {"text": text},
            "execution": execution(replay_safe),
            "limits": {"timeout_seconds": 60, "max_attempts_per_task": attempts},
        }

    def submit(key, document):
        filename = write(f"{key}.json", document)
        result = cli("job", "submit", "--key", key, "--file", filename)
        return json.loads(result.stdout)["job_id"]

    def job(job_id):
        return json.loads(cli("job", "get", "--id", job_id).stdout)

    def wait_job(job_id):
        return wait_for(lambda: (current if (current := job(job_id))["state"] in TERMINAL else None))

    def job_events(job_id):
        return json.loads(cli("job", "events", "--id", job_id, "--limit", "100").stdout)["events"]

    def db_rows(query, args=()):
        with sqlite3.connect(root / "server" / "state.db") as database:
            return database.execute(query, args).fetchall()

    def attempts(job_id):
        return db_rows(
            """SELECT a.generation,a.released,a.id
               FROM attempts a JOIN tasks t ON t.id=a.task
               WHERE t.job_id=? ORDER BY a.generation""",
            (job_id,),
        )

    try:
        with tempfile.TemporaryDirectory(prefix="computecloud-ci-retry-") as temporary:
            root = Path(temporary)
            repo_dir = root / "repository"
            repo_dir.mkdir()
            run("git", "-C", str(repo_dir), "init")
            (repo_dir / "README.md").write_text("Retry fixture repository\n")
            run("git", "-C", str(repo_dir), "add", ".")
            run("git", "-C", str(repo_dir), "-c", "user.name=CI Retry", "-c",
                "user.email=ci-retry@example.invalid", "commit", "-m", "fixture")
            commit = run("git", "-C", str(repo_dir), "rev-parse", "HEAD").stdout.strip()

            marker = root / "retry.marker"
            fixture = Path(write("agent-fixture", FIXTURE))
            fixture.chmod(0o700)
            address, http_address = free_address(), free_address()
            while address == http_address:
                http_address = free_address()
            tls = {"insecure_loopback": True}
            user_token = write("user.token", secrets.token_hex(32))
            worker_configs, worker_ids = {}, []
            for name in ["worker-a", "worker-b"]:
                token = write(f"{name}.token", secrets.token_hex(32))
                worker_ids.append({"worker_id": name, "token_file": token,
                                   "projects": ["ci-retry"], "credentials": ["fixture"]})
                worker_configs[name] = write(f"{name}.json", {"worker": {
                    "id": name,
                    "server_address": address,
                    "data_dir": str(root / name),
                    "token_file": token,
                    "tls": tls,
                    "slots": 1,
                    "stop_grace_ms": 100,
                    "repositories": {"repo": str(repo_dir)},
                    "runtimes": {"codex_exec": {
                        "executable": str(fixture),
                        "version": "ci-retry-fixture-1",
                        "models": ["fixture-codex"],
                        "credentials": ["fixture"],
                        "env": {"CI_RETRY_MARKER": str(marker)},
                    }},
                    "policies": {"review": {"codex_sandbox": "read-only"}},
                    "verifiers": {"check": [["test", "-f", "README.md"]]},
                }})
            templates = json.loads(run(binary, "templates", "--config", worker_configs["worker-a"]).stdout)["templates"]
            server_config = write("server.json", {"server": {
                "listen": address,
                "http": {"listen": http_address},
                "data_dir": str(root / "server"),
                "tls": tls,
                "tick_ms": 50,
                "lease_seconds": 4,
                "max_project_tasks": 2,
                "credentials": {"fixture": 2},
                "jobs": {"enabled": True, "templates": templates},
                "users": [{"token_file": user_token, "owner": "ci", "projects": ["ci-retry"],
                           "credentials": ["fixture"], "scopes": ["jobs:submit", "jobs:read", "jobs:cancel"]}],
                "workers": worker_ids,
            }})
            write("client.json", {"client": {"address": address, "http_url": "http://" + http_address,
                                             "token_file": user_token, "tls": tls}})

            start("server", "server", server_config)
            for name, cfg in worker_configs.items():
                start(name, "worker", cfg)
            wait_for(lambda: len([w for w in json.loads(cli("workers").stdout).get("workers", []) if w.get("online")]) == 2)

            def retry_once():
                marker.unlink(missing_ok=True)
                jid = submit("retry-once", spec("retry once", 2, True))
                deadline_before = db_rows("SELECT deadline FROM tasks WHERE job_id=?", (jid,))[0][0]
                current = wait_job(jid)
                if current["state"] != "SUCCEEDED":
                    raise AssertionError(f"retry-once Job did not succeed: {current}")
                rows = attempts(jid)
                if [row[0] for row in rows] != [1, 2] or any(row[1] != 1 for row in rows):
                    raise AssertionError(f"unexpected attempt history: {rows}")
                deadline_after = db_rows("SELECT deadline FROM tasks WHERE job_id=?", (jid,))[0][0]
                if deadline_before != deadline_after:
                    raise AssertionError("retry reset Task deadline")
                event_types = [event["type"] for event in job_events(jid)]
                if "job.task_retry_scheduled" not in event_types:
                    raise AssertionError(f"missing retry event: {event_types}")
                states = db_rows(
                    """SELECT ar.state,count(*) FROM artifacts ar
                       JOIN tasks t ON t.id=ar.task WHERE t.job_id=?
                       GROUP BY ar.state ORDER BY ar.state""",
                    (jid,),
                )
                if dict(states).get("ORPHANED", 0) < 1 or dict(states).get("ACCEPTED", 0) != 1:
                    raise AssertionError(f"artifact generation gating failed: {states}")
                return {"job_id": jid, "attempt_generations": [row[0] for row in rows],
                        "artifact_states": dict(states), "deadline_preserved": True}

            record("retry_once_then_succeed", retry_once)

            def retry_exhaustion():
                jid = submit("retry-exhaust", spec("always runtime fail", 2, True))
                current = wait_job(jid)
                if current["state"] != "FAILED":
                    raise AssertionError(f"retry exhaustion did not fail Job: {current}")
                rows = attempts(jid)
                if [row[0] for row in rows] != [1, 2]:
                    raise AssertionError(f"max attempts not enforced: {rows}")
                event_types = [event["type"] for event in job_events(jid)]
                if "job.task_retry_exhausted" not in event_types:
                    raise AssertionError(f"missing retry exhausted event: {event_types}")
                return {"job_id": jid, "attempt_generations": [row[0] for row in rows],
                        "error_code": current.get("error_code")}

            record("retry_budget_exhaustion", retry_exhaustion)

            def non_retryable_failure():
                jid = submit("verification-no-retry", spec("verification failure", 2, True))
                current = wait_job(jid)
                if current["state"] != "FAILED":
                    raise AssertionError(f"verification failure did not fail: {current}")
                rows = attempts(jid)
                if len(rows) != 1:
                    raise AssertionError(f"non-retryable failure retried: {rows}")
                event_types = [event["type"] for event in job_events(jid)]
                if "job.task_retry_scheduled" in event_types:
                    raise AssertionError("non-retryable error scheduled retry")
                return {"job_id": jid, "attempts": len(rows), "error_code": current.get("error_code")}

            record("non_retryable_error_stays_terminal", non_retryable_failure)

            def replay_safety_gate():
                unsafe = spec("unsafe", 2, False)
                filename = write("unsafe.json", unsafe)
                result = cli("job", "submit", "--key", "unsafe", "--file", filename, check=False)
                if result.returncode == 0 or "replay_safe" not in result.stderr:
                    raise AssertionError(f"unsafe retry policy accepted: rc={result.returncode} stderr={result.stderr}")
                return {"rejected": True}

            record("explicit_replay_safety_required", replay_safety_gate)

            for name in worker_configs:
                stop(name)
            stop("server")
            with sqlite3.connect(root / "server" / "state.db") as database:
                integrity = database.execute("PRAGMA integrity_check").fetchone()[0]
                schema = database.execute("PRAGMA user_version").fetchone()[0]
            if integrity != "ok" or schema != 6:
                raise AssertionError(f"storage gate failed: integrity={integrity} schema={schema}")

            report["summary"] = {
                "status": "PASSED",
                "steps_passed": len(report["steps"]),
                "retry_success_jobs": 1,
                "retry_exhausted_jobs": 1,
                "non_retryable_jobs": 1,
                "schema_version": schema,
            }
    except BaseException as exc:
        failure = exc
        report["summary"] = {"status": "FAILED", "error": f"{type(exc).__name__}: {exc}"}
    finally:
        for name in list(processes):
            try:
                stop(name)
            except BaseException as exc:
                report.setdefault("cleanup_errors", []).append(f"{name}: {exc}")
        for handle in logs:
            handle.close()
        if failure:
            report["log_tails"] = {p.name: p.read_text(errors="replace")[-5000:] for p in log_dir.glob("*.log")}
        report["finished_at"] = utc_now()
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print(json.dumps(report["summary"], sort_keys=True), flush=True)
        print(f"Report: {output}", flush=True)
    if failure:
        raise failure


if __name__ == "__main__":
    main()
