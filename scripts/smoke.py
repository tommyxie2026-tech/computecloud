#!/usr/bin/env python3
"""Linux CLI smoke test. Protocol fixtures only; no model account or API calls."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import sqlite3
import subprocess
import tarfile
import tempfile
import time

FIXTURE = r'''#!/bin/sh
if [ "$1" = '--version' ]; then echo fixture-1; exit 0; fi
prompt=$(cat)
case "$prompt" in
  *slow*) sleep 90 & echo $! > descendant.pid; wait ;;
  *) sleep 0.3 ;;
esac
printf 'fixture result\n' > result.txt
if [ "$1" = 'exec' ]; then
  echo '{"type":"thread.started","thread_id":"fixture-session"}'
  echo '{"type":"item.completed","item":{"type":"agent_message","text":"fixture completed"}}'
  echo '{"type":"turn.completed"}'
else
  echo '{"type":"result","subtype":"success","is_error":false,"result":"fixture completed","session_id":"fixture-session"}'
fi
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/computecloud")
    parser.add_argument("--old-binary", help="optional v0.1 binary for downgrade refusal check")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    print(subprocess.check_output([binary, "version"], text=True).strip(), flush=True)
    processes, logs = {}, []
    with tempfile.TemporaryDirectory(prefix="computecloud-smoke-") as temporary:
        root = Path(temporary)

        def write(name, value):
            path = root / name
            path.write_text(json.dumps(value) if not isinstance(value, str) else value)
            path.chmod(0o600)
            return str(path)

        def run(*cmd):
            result = subprocess.run(cmd, text=True, capture_output=True, timeout=25)
            if result.returncode:
                raise RuntimeError(f"command failed: {cmd[0:3]}\n{result.stderr}")
            return result.stdout

        def cli(*cmd):
            return run(binary, *cmd, "--config", str(root / "client.json"))

        def start(name, role, config):
            log = open(root / (name + ".log"), "a")
            logs.append(log)
            processes[name] = subprocess.Popen([binary, role, "--config", str(config)], stdout=log, stderr=log)

        def stop(name, hard=False):
            process = processes.get(name)
            if process and process.poll() is None:
                process.send_signal(signal.SIGKILL if hard else signal.SIGTERM)
                process.wait(timeout=15)

        def wait(predicate, timeout=25):
            end = time.monotonic() + timeout
            last = None
            while time.monotonic() < end:
                try:
                    result = predicate()
                    if result:
                        return result
                except (RuntimeError, KeyError) as error:
                    last = error
                time.sleep(0.1)
            raise AssertionError(f"condition timed out: {last}")

        def task(task_id):
            return json.loads(cli("task", "get", "--id", task_id))

        def state(task_id, states):
            current = task(task_id)
            return current if current["state"] in states else None

        def submit(key, engine="codex", prompt="hello"):
            spec = {"project_id": "demo", "idempotency_key": key, "engine": engine,
                    "credential_ref": "fixture-account", "workspace": {"repository_ref": "repo", "base_commit": commit},
                    "input": {"text": prompt}, "policy_ref": "trusted", "acceptance_profile": "file-exists", "timeout_seconds": 90}
            filename = write(key + ".json", spec)
            return json.loads(cli("task", "submit", "--file", filename))["task_id"]

        def child_file(current):
            return root / current["worker_id"] / "workspaces" / current["attempt_id"] / "descendant.pid"

        def assert_child_stopped(current):
            pid = child_file(current).read_text().strip()
            stat = Path("/proc") / pid / "stat"
            assert not stat.exists() or ") Z " in stat.read_text(), f"child {pid} is alive"

        try:
            repo = root / "repository"
            repo.mkdir()
            run("git", "-C", str(repo), "init")
            (repo / "README.md").write_text("Fixture repository\n")
            run("git", "-C", str(repo), "add", ".")
            run("git", "-C", str(repo), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
            commit = run("git", "-C", str(repo), "rev-parse", "HEAD").strip()
            fixture = Path(write("agent-fixture", FIXTURE))
            fixture.chmod(0o700)
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                address = f"127.0.0.1:{sock.getsockname()[1]}"
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                http_address = f"127.0.0.1:{sock.getsockname()[1]}"
            tls = {"insecure_loopback": True}
            user_token = write("user.token", secrets.token_hex(32))
            workers = []
            worker_configs = {}
            for name in ["worker-a", "worker-b"]:
                token = write(name + ".token", secrets.token_hex(32))
                workers.append({"worker_id": name, "token_file": token, "projects": ["demo"], "credentials": ["fixture-account"]})
                runtimes = {profile: {"executable": str(fixture), "version": "fixture-1", "models": [model], "credentials": ["fixture-account"]}
                            for profile, model in [("codex_exec", "fixture-codex"), ("claude_print", "fixture-claude")]}
                worker_configs[name] = write(name + ".json", {"worker": {
                    "id": name, "server_address": address, "data_dir": str(root / name), "token_file": token, "tls": tls,
                    "slots": 1, "stop_grace_ms": 100, "runtimes": runtimes, "repositories": {"repo": str(repo)},
                    "policies": {"trusted": {"codex_sandbox": "workspace-write", "claude_permission_mode": "dontAsk"}},
                    "verifiers": {"file-exists": [["test", "-f", "result.txt"]]}}})
            templates = json.loads(run(binary, "templates", "--config", worker_configs["worker-a"]))["templates"]
            server_config = write("server.json", {"server": {
                "http": {"listen": http_address}, "jobs": {"enabled": True, "templates": templates}, "mcp": {"enabled": True},
                "listen": address, "data_dir": str(root / "server"), "tls": tls, "lease_seconds": 4, "tick_ms": 50,
                "models": {"codex_exec": "fixture-codex", "claude_print": "fixture-claude"}, "credentials": {"fixture-account": 2},
                "users": [{"token_file": user_token, "owner": "operator", "projects": ["demo"], "credentials": ["fixture-account"], "scopes": ["jobs:submit", "jobs:read", "jobs:cancel", "tasks:submit", "tasks:read", "tasks:cancel"]}], "workers": workers}})
            write("client.json", {"client": {"http_url": "http://" + http_address, "address": address, "token_file": user_token, "tls": tls}})
            start("server", "server", server_config)
            for name, filename in worker_configs.items():
                start(name, "worker", filename)
            wait(lambda: len([w for w in json.loads(cli("workers")).get("workers", []) if w.get("online")]) == 2)
            ids = [submit(f"success-{i}", engine) for i, engine in enumerate(["codex", "claude", "codex", "claude"])]
            completed = [wait(lambda task_id=task_id: state(task_id, {"SUCCEEDED", "FAILED"})) for task_id in ids]
            assert all(t["state"] == "SUCCEEDED" for t in completed), completed
            assert len({t["worker_id"] for t in completed}) == 2
            assert submit("success-0") == ids[0]
            events = [json.loads(line) for line in cli("task", "watch", "--id", ids[0]).splitlines()]
            assert [int(event["seq"]) for event in events] == list(range(1, len(events) + 1))
            assert "payload" in events[0]
            artifact = json.loads(cli("artifact", "list", "--id", ids[0]))["artifacts"][0]
            result = root / "result.tar"
            cli("artifact", "download", "--id", ids[0], "--artifact", artifact["artifact_id"], "--out", str(result))
            assert hashlib.sha256(result.read_bytes()).hexdigest() == artifact["sha256"]
            with tarfile.open(fileobj=io.BytesIO(result.read_bytes())) as archive:
                assert set(archive.getnames()) == {"report.json", "changes.patch", "stderr.log"}
            print("PASS: two worker processes, both profiles, idempotency, events, artifact checksum", flush=True)

            def submit_job(key, prompt="fixture job"):
                spec = {"schema_version": "v0.2", "project_id": "demo", "mode": "single",
                        "workspace": {"repository_ref": "repo", "base_commit": commit}, "input": {"text": prompt},
                        "execution": {"engine": "codex", "runtime_profile": "codex_exec", "model": "fixture-codex", "credential_ref": "fixture-account", "policy_ref": "trusted", "acceptance_profile": "file-exists"},
                        "limits": {"timeout_seconds": 90, "max_attempts_per_task": 1}}
                path = write(key + ".json", spec)
                return json.loads(cli("job", "submit", "--key", key, "--file", path))["job_id"]

            def job_state(jid, states):
                current = json.loads(cli("job", "get", "--id", jid))
                return current if current["state"] in states else None

            job_id = submit_job("job-success")
            assert submit_job("job-success") == job_id
            job_events = [json.loads(line) for line in cli("job", "watch", "--id", job_id).splitlines()]
            assert [int(event["seq"]) for event in job_events] == list(range(1, len(job_events) + 1))
            assert job_state(job_id, {"SUCCEEDED"})
            final = json.loads(cli("job", "result", "--id", job_id))["final_artifacts"][0]
            cli("job", "download", "--id", job_id, "--artifact", final["artifact_id"], "--out", str(root / "job-result.tar"))
            assert hashlib.sha256((root / "job-result.tar").read_bytes()).hexdigest() == final["sha256"]
            jid = submit_job("job-cancel", "slow")
            tid = json.loads(cli("job", "tasks", "--id", jid))["tasks"][0]["task_id"]
            running_job = wait(lambda: state(tid, {"RUNNING"}))
            wait(lambda: child_file(running_job).exists())
            cli("job", "cancel", "--id", jid, "--control-id", "cancel-job")
            wait(lambda: job_state(jid, {"CANCELED"}))
            assert_child_stopped(running_job)
            print("PASS: Job CLI, template digests, idempotency, events, download and cancellation", flush=True)

            slow = submit("cancel", prompt="slow")
            running = wait(lambda: state(slow, {"RUNNING"}))
            wait(lambda: child_file(running).exists())
            cli("task", "cancel", "--id", slow, "--control-id", "stop-once")
            wait(lambda: state(slow, {"CANCELED"}))
            assert_child_stopped(running)

            slow = submit("worker-crash", prompt="slow")
            running = wait(lambda: state(slow, {"RUNNING"}))
            wait(lambda: child_file(running).exists())
            name = running["worker_id"]
            stop(name, hard=True)
            start(name, "worker", worker_configs[name])
            failed = wait(lambda: state(slow, {"FAILED"}))
            assert failed["error_code"] in {"WORKER_RESTARTED", "WORKER_LOST"}, failed
            assert_child_stopped(running)
            print("PASS: running cancellation and worker SIGKILL recovery clean child processes", flush=True)

            slow = submit("lease-loss", prompt="slow")
            running = wait(lambda: state(slow, {"RUNNING"}))
            wait(lambda: child_file(running).exists())
            lost_job = submit_job("job-lease-loss", "slow")
            lost_task = json.loads(cli("job", "tasks", "--id", lost_job))["tasks"][0]["task_id"]
            running_job = wait(lambda: state(lost_task, {"RUNNING"}))
            wait(lambda: child_file(running_job).exists())
            stop("server", hard=True)
            time.sleep(5)
            assert_child_stopped(running)
            start("server", "server", server_config)
            failed = wait(lambda: state(slow, {"FAILED"}))
            assert failed["error_code"] == "WORKER_LOST", failed
            wait(lambda: job_state(lost_job, {"FAILED"}))
            assert_child_stopped(running_job)
            assert task(ids[0])["state"] == "SUCCEEDED"
            print("PASS: server SIGKILL, worker local lease stop, SQLite recovery and event delivery", flush=True)

            for name in worker_configs:
                stop(name)
            stop("server")
            if args.old_binary:
                legacy = json.loads(Path(server_config).read_text())
                for key in ["http", "jobs", "mcp"]:
                    legacy["server"].pop(key)
                for identity in legacy["server"]["users"]:
                    identity.pop("scopes")
                old_config = write("legacy-server.json", legacy)
                for role, cfg in [("server", old_config), ("worker", worker_configs["worker-a"])]:
                    refusal = subprocess.run([str(Path(args.old_binary).resolve()), role, "--config", cfg], capture_output=True, text=True, timeout=10)
                    assert refusal.returncode != 0 and "database schema is newer" in refusal.stderr, refusal.stderr
                print("PASS: real v0.1 server and worker binaries refuse upgraded databases", flush=True)
            backup = root / "backup.tar.gz"
            run(binary, "backup", "--data-dir", str(root / "server"), "--out", str(backup))
            restored = root / "restored"
            restored.mkdir()
            with tarfile.open(backup) as archive:
                archive.extractall(restored, filter="data")
            with sqlite3.connect(restored / "state.db") as database:
                assert database.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert database.execute("SELECT count(*) FROM tasks").fetchone()[0] == 10
                assert database.execute("SELECT count(*) FROM jobs").fetchone()[0] == 3
            assert (restored / "artifacts" / artifact["artifact_id"]).read_bytes() == result.read_bytes()
            print("PASS: offline backup restores task records and artifacts", flush=True)
        except BaseException:
            for filename in root.glob("*.log"):
                print(f"--- {filename.name} ---\n{filename.read_text()[-6000:]}", flush=True)
            raise
        finally:
            for name in list(processes):
                stop(name)
            for log in logs:
                log.close()


if __name__ == "__main__":
    main()
