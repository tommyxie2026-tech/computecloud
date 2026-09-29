#!/usr/bin/env python3
"""C2 Control client E2E: real Server + fixture Worker + Node HTTP client, no model calls."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import time

FIXTURE = r'''#!/usr/bin/env python3
import json, pathlib, sys, time
if sys.argv[1:] == ['--version']:
    print('control-client-fixture-1'); sys.exit(0)
prompt = sys.stdin.read()\nif 'hold-control' in prompt:\n    time.sleep(15)
pathlib.Path('README.md').touch()
if sys.argv[1] == 'exec':
    print(json.dumps({'type':'thread.started','thread_id':'control-client'}))
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':'control client fixture done'}}))
    print(json.dumps({'type':'turn.completed'}))
else:
    print(json.dumps({'type':'system','session_id':'control-client'}))
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'control client fixture done','session_id':'control-client'}))
'''

def free_address():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{sock.getsockname()[1]}"

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--binary", default="bin/computecloud")
    ap.add_argument("--output", default="dist/control-client-e2e/report.json")
    ap.add_argument("--timeout", type=int, default=45)
    args = ap.parse_args()

    binary = str(Path(args.binary).resolve())
    e2e = Path(__file__).resolve().parents[1] / "clients" / "control" / "scripts" / "e2e.mjs"
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    logs = output.parent / "logs"
    logs.mkdir(parents=True, exist_ok=True)
    processes = {}
    handles = []

    def run(*cmd, timeout=30):
        env = os.environ.copy()
        for name in ["HTTP_PROXY","HTTPS_PROXY","ALL_PROXY","http_proxy","https_proxy","all_proxy"]:
            env.pop(name, None)
        env["NO_PROXY"] = "127.0.0.1,localhost"
        result = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout, env=env)
        if result.returncode:
            raise RuntimeError(f"{' '.join(cmd[:3])}: {result.stderr[-3000:]}")
        return result.stdout.strip()

    def start(name, role, config):
        handle = open(logs / f"{name}.log", "w")
        handles.append(handle)
        processes[name] = subprocess.Popen([binary, role, "--config", str(config)], stdout=handle, stderr=handle)

    def wait_for(fn):
        deadline = time.monotonic() + args.timeout
        last = None
        while time.monotonic() < deadline:
            try:
                value = fn()
                if value:
                    return value
            except Exception as exc:
                last = exc
            for name, process in processes.items():
                if process.poll() is not None:
                    raise RuntimeError(f"{name} exited: {process.returncode}")
            time.sleep(0.1)
        raise TimeoutError(f"timed out: {last}")

    def stop_all():
        for process in processes.values():
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
        for process in processes.values():
            if process.poll() is None:
                try: process.wait(timeout=10)
                except subprocess.TimeoutExpired: process.kill()
        for handle in handles: handle.close()

    report = {"schema_version":"control-client-e2e.v1","status":"FAILED","real_model_calls":False}
    try:
        with tempfile.TemporaryDirectory(prefix="computecloud-control-client-") as td:
            root = Path(td)
            def write(name, value, mode=0o600):
                path = root / name
                path.write_text(value if isinstance(value, str) else json.dumps(value))
                path.chmod(mode)
                return path

            repo = root / "repo"
            repo.mkdir()
            run("git","-C",str(repo),"init")
            (repo / "README.md").write_text("control client fixture\n")
            run("git","-C",str(repo),"add",".")
            run("git","-C",str(repo),"-c","user.name=Fixture","-c","user.email=fixture@example.invalid","commit","-m","fixture")
            commit = run("git","-C",str(repo),"rev-parse","HEAD")

            fixture = write("agent-fixture", FIXTURE, 0o700)
            grpc_addr, http_addr = free_address(), free_address()
            while grpc_addr == http_addr: http_addr = free_address()
            tls = {"insecure_loopback": True}
            user_token = write("user.token", secrets.token_hex(32))
            worker_token = write("worker.token", secrets.token_hex(32))
            worker_cfg = write("worker.json", {"worker":{
                "id":"worker-control","server_address":grpc_addr,"data_dir":str(root/"worker"),
                "token_file":str(worker_token),"tls":tls,"slots":1,"stop_grace_ms":100,
                "repositories":{"repo":str(repo)},
                "runtimes":{"codex_exec":{"executable":str(fixture),"version":"control-client-fixture-1","models":["fixture"],"credentials":["fixture"]}},
                "policies":{"review":{"codex_sandbox":"read-only"}},
                "verifiers":{"check":[["test","-f","README.md"]]}
            }})
            templates = json.loads(run(binary,"templates","--config",str(worker_cfg)))["templates"]
            server_cfg = write("server.json", {"server":{
                "listen":grpc_addr,"http":{"listen":http_addr},"data_dir":str(root/"server"),"tls":tls,
                "tick_ms":30,"lease_seconds":4,"max_project_tasks":4,"credentials":{"fixture":1},
                "jobs":{"enabled":True,"templates":templates},
                "users":[{"token_file":str(user_token),"owner":"ui","projects":["control"],"credentials":["fixture"],
                          "scopes":["jobs:submit","jobs:read","jobs:cancel","jobs:control"]}],
                "workers":[{"token_file":str(worker_token),"worker_id":"worker-control","projects":["control"],"credentials":["fixture"]}]
            }})
            client_cfg = write("client.json", {"client":{"address":grpc_addr,"http_url":"http://"+http_addr,"token_file":str(user_token),"tls":tls}})
            start("server","server",server_cfg)
            start("worker","worker",worker_cfg)

            def cli(*argv):
                return run(binary,*argv,"--config",str(client_cfg))
            wait_for(lambda: len([w for w in json.loads(cli("workers")).get("workers",[]) if w.get("online")]) == 1)

            spec = {"schema_version":"v0.2","project_id":"control","mode":"single",
                    "workspace":{"repository_ref":"repo","base_commit":commit},"input":{"text":"observe"},
                    "execution":{"engine":"codex","runtime_profile":"codex_exec","model":"fixture","credential_ref":"fixture",
                                 "policy_ref":"review","acceptance_profile":"check"},
                    "limits":{"timeout_seconds":30,"max_attempts_per_task":1}}
            for i in range(3):
                spec_path = write(f"job-{i}.json", spec)
                result = json.loads(cli("job","submit","--key",f"control-e2e-{i}","--file",str(spec_path)))
                job_id = result["job_id"]
                wait_for(lambda jid=job_id: (j if (j:=json.loads(cli("job","get","--id",jid)))["state"] in {"SUCCEEDED","FAILED"} else None))
                state = json.loads(cli("job","get","--id",job_id))["state"]
                if state != "SUCCEEDED":
                    raise AssertionError(f"fixture Job {job_id} ended {state}")

            slow_spec = dict(spec)
            slow_spec["input"] = {"text":"hold-control"}
            slow_path = write("job-write.json", slow_spec)
            slow = json.loads(cli("job","submit","--key","control-e2e-write","--file",str(slow_path)))
            write_job = slow["job_id"]
            wait_for(lambda: (j if (j:=json.loads(cli("job","get","--id",write_job)))["state"] in {"EXECUTING","MAPPING","REDUCING","STOPPING"} else None))

            node = run("node", str(e2e), "--base-url", "http://"+http_addr, "--token-file", str(user_token), "--write-job", write_job, timeout=30)
            evidence = json.loads(node.splitlines()[-1])
            if evidence.get("status") != "PASSED":
                raise AssertionError(evidence)
            wait_for(lambda: (j if (j:=json.loads(cli("job","get","--id",write_job)))["state"] == "CANCELED" else None))
            report.update(status="PASSED", evidence=evidence, topology={"servers":1,"workers":1,"jobs":4,"write_job":write_job})
    except BaseException as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
        raise
    finally:
        stop_all()
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")

if __name__ == "__main__":
    main()
