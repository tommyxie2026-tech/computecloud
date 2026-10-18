#!/usr/bin/env python3
"""CI end-to-end task flow using protocol fixtures; never calls a model provider."""

import argparse
import base64
import platform
from process_utils import process_alive
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
try:
    context = json.loads(prompt)
except ValueError:
    context = {}
if isinstance(context, dict) and context.get('version') == 'iu-context-v1':
    import re
    ports = json.loads(context['instructions'].split('Declared output ports: ')[-1])
    if 'fixture-coverage-v5' in context['instructions']:
        seen, customers, orders = {}, {}, []
        rule = {'threshold_cents':10000,'discount_cents':1000}
        exclude = False
        for segment in (context.get('local') or [])+(context.get('rules') or []):
            value = json.loads(segment['text'])
            if 'order_id' in value:
                identity = value['order_id']
                if identity in seen and seen[identity] != value: raise ValueError('conflicting original order')
                seen[identity] = value
            if 'threshold_cents' in value: rule = value
            if value.get('type') == 'special_no_discount': exclude = True
        total = sum(order['paid_cents'] for order in seen.values())
        for order in seen.values():
            if not exclude or not order['is_special']:
                customers[order['customer_id']] = customers.get(order['customer_id'],0)+order['paid_cents']
        for value in customers.values():
            if value >= rule['threshold_cents']: total -= min(value,rule['discount_cents'])
        if 'fixture-coverage-repaired' not in context['instructions']: total += 1000
        amount = {'orders':list(seen.values()),'total_cents':total,'currency':'CNY','unit':'cent'}
        source = context['local'][0]['source']
    elif context['inputs']:
        amounts = json.loads(context['inputs'][0]['value'])
        rule = json.loads(context['rules'][0]['text'])
        applied = rule['discount_cents']
        marker = re.search(r'fixture-applied-discount:(\d+)', context['instructions'])
        if marker:
            applied = int(marker.group(1))
        amount = sum(amounts.values())
        if amount >= rule['threshold_cents']:
            amount = max(0, amount - applied)
        source = context['rules'][0]['source']
    else:
        amount = json.loads(context['local'][0]['text'])['paid_cents']
        source = context['local'][0]['source']
    records = [{'id':source['source'], 'kind':'observation', 'payload':{'value':amount},
                'sources':[{'id':source['source'],'sha256':source['sha256']}],
                'validation':'unchecked','validity':'VALID'}]
    result = json.dumps({'version':'iu-node-output-v1', 'values':{p['name']:amount for p in ports}, 'records':records})

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
    parser.add_argument("--workspace-benchmark", action="store_true")
    parser.add_argument("--readiness-check", action="store_true")
    parser.add_argument("--iu-graph-check", action="store_true", help="real Server/two Worker processes with mock graph agents")
    args = parser.parse_args()
    if args.timeout < 20 or args.timeout > 300:
        parser.error("timeout must be 20..300 seconds")

    binary = str(Path(args.binary).resolve())
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    logs_output = output.parent / "logs"
    logs_output.mkdir(parents=True, exist_ok=True)
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
        mode = "a" if name in processes else "w"
        log = open(logs_output / f"{name}.log", mode)
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
        wait_for(lambda: not process_alive(int(pid)))
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
                if args.iu_graph_check:
                    verifier = root / "verify-orders"
                    verifier.write_text((Path(__file__).resolve().parents[1] / "examples/iu-graph/verify_orders.py").read_text())
                    verifier.chmod(0o700)
                    config["worker"]["runtimes"]["command_exec"] = {"executable": str(verifier),
                        "version": "iu-orders-verifier-1", "models": ["orders-proof"], "credentials": ["fixture"]}
                worker_configs[name] = write(f"{name}.json", config)
            template_metadata=json.loads(run_command(binary,"templates","--config",worker_configs["worker-a"]))
            templates=template_metadata["templates"]
            if args.iu_graph_check:
                replay_binding={"version":"1","runtime_profile":"codex_exec","runtime_version":"ci-flow-fixture-1",
                    "model":"fixture-codex","environment_sha256":hashlib.sha256(b"CI immutable fixture environment v1").hexdigest(),
                    "runtime_config_sha256":template_metadata["runtime_config_hashes"]["codex_exec"],
                    "policy_sha256":template_metadata["policy_hashes"]["review"],"read_only":True}
                for path in worker_configs.values():
                    data=json.loads(Path(path).read_text());data["worker"]["graph_replay_bindings"]={"fixture":replay_binding}
                    Path(path).write_text(json.dumps(data))
            server_config = write("server.json", {"server": {"listen": address,
                # This fixture validates job lifecycle, not the separately tested
                # public HTTP limiter. Keep fast polling deterministic on CI hosts.
                "http": {"listen": http_address, "rate_limit": {"rate_per_minute": 1000000,
                    "burst": 100000, "max_entries": 10000, "idle_timeout_seconds": 600}},
                "data_dir": str(root / "server"), "tls": tls,
                "tick_ms": 50, "lease_seconds": 4, "max_project_tasks": 2,
                "credentials": {"fixture": 2}, "jobs": {"enabled": True, "max_parallelism": 2,
                    "recommended_parallelism": 2, "templates": templates},
                "users": [{"token_file": user_token, "owner": "ci", "projects": ["ci-flow"],
                    "credentials": ["fixture"], "scopes": ["jobs:submit", "jobs:read", "jobs:cancel"]}],
                "workers": worker_identities}})
            write("client.json", {"client": {"address": address, "http_url": "http://" + http_address,
                                               "token_file": user_token, "tls": tls}})
            if args.iu_graph_check:
                server_data = json.loads(Path(server_config).read_text())
                server_data["server"]["jobs"].update(graphs_enabled=True, graph_commands={"orders-proof": {
                    "execution": {"runtime_profile":"command_exec","model":"orders-proof","credential_ref":"fixture",
                                  "policy_ref":"review","acceptance_profile":"check"},
                    "parameters_schema": '{"type":"object","additionalProperties":false}'}})
                server_data["server"]["jobs"]["graph_replay_bindings"]={"fixture":replay_binding}
                server_data["server"]["jobs"]["graph_record_verifiers"]={"order-source":{
                    "version":"1","kind":"source_json_equal","scope":"source-paid","source_pointer":"/paid_cents",
                    "claim_pointer":"/value","record_kinds":["observation"],"timeout_ms":100}}
                eval_token = write("evaluator.token", secrets.token_hex(32))
                server_data["server"]["users"].append({"token_file":eval_token,"owner":"ci","projects":["ci-flow"],
                    "credentials":["fixture"],"scopes":["jobs:read","jobs:submit","goals:evaluate","goals:invalidate","goals:import","goals:verify","goals:propose","jobs:control"]})
                write("server.json", server_data)
                eval_config = write("evaluator.json", {"client":{"http_url":"http://"+http_address,"token_file":eval_token,"tls":tls}})


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
            if args.iu_graph_check:
                def native_graph_flow():
                    template = json.loads((Path(__file__).resolve().parents[1] / "examples/iu-graph/orders.json").read_text())
                    def attempt_snapshot(job_id):
                        with sqlite3.connect(f"file:{root / 'server' / 'state.db'}?mode=ro", uri=True) as db:
                            rows = db.execute(
                                "SELECT t.id,t.attempt,t.current_generation,a.id,a.generation,a.released,a.worker "
                                "FROM tasks t JOIN attempts a ON a.task=t.id WHERE t.job_id=? ORDER BY t.id,a.generation,a.id",
                                (job_id,)).fetchall()
                        fields = ("task_id", "current_attempt_id", "current_generation", "attempt_id", "generation", "released", "worker_id")
                        return [dict(zip(fields, row)) for row in rows]
                    results = []
                    for index, (name, discount, applied, expected, verdict) in enumerate([
                        ("discount_1000",1000,1000,11000,"PASS"), ("revised_discount_2000",2000,2000,10000,"PASS"),
                        ("stale_rule_rejected",2000,1000,11000,"FAIL"), ("missing_rule_rejected",1000,0,12000,"FAIL")]):
                        spec = json.loads(json.dumps(template))
                        spec.update(project_id="ci-flow",workspace={"repository_ref":"repo","base_commit":commit})
                        spec["limits"]["max_replans"] = 0
                        for op in spec["operators"]:
                            if op["kind"] == "agent":
                                op["execution"] = execution("codex")
                        rule = next(s for s in spec["sources"] if s["id"]=="discount_rule")
                        rule["text"] = json.dumps({"threshold_cents":10000,"discount_cents":discount},separators=(",",":"))
                        rule["sha256"] = hashlib.sha256(rule["text"].encode()).hexdigest()
                        for span in spec["rules"] + [span for n in spec["nodes"] for span in n.get("spans",[])]:
                            if span["source"] == rule["id"]:
                                span.update(start=0,end=len(rule["text"].encode()),sha256=rule["sha256"])
                        next(n for n in spec["nodes"] if n["id"]=="discount")["instructions"] += f" fixture-applied-discount:{applied}"
                        key = "ci-graph-" + name
                        request = write(key+".json",spec)
                        job_id = json.loads(cli("job","submit-graph","--key",key,"--file",request))["job_id"]
                        current = wait_job(job_id)
                        if current["state"] != "SUCCEEDED":
                            raise AssertionError(f"graph execution failed: {current}")
                        result = json.loads(cli("job","result","--id",job_id))
                        if result["outputs"]["discount/result"] != expected or result["outputs"]["verify/result"] != verdict:
                            raise AssertionError(f"independent verifier mismatch: {result}")
                        status = json.loads(cli("job","graph","--id",job_id))
                        tasks_before = job_tasks(job_id)
                        if status["goal_state"]!="EVALUATING" or len(tasks_before)!=4:
                            raise AssertionError("execution fact prematurely completed Goal or Task count is wrong")
                        roots = [t for t in tasks_before if t["partition_key"] in ("a","b")]
                        if len({t["worker_id"] for t in roots})!=2:
                            raise AssertionError("graph roots did not span two Worker processes")
                        if len(status["admissions"])!=4 or any(a["state"]!="ADMITTED" for a in status["admissions"]):
                            raise AssertionError("output admission is incomplete")
                        attempts_before = attempt_snapshot(job_id)
                        if len(attempts_before) != 4 or any(a["released"] != 1 for a in attempts_before):
                            raise AssertionError("expected exactly four released graph attempts")
                        attempts_after = attempts_before
                        restarted = False
                        if index==0:
                            old_pid = processes["server"].pid
                            stop("server",hard=True)
                            start("server","server",server_config)
                            restored = wait_for(lambda: json.loads(cli("job","graph","--id",job_id)))
                            if restored["output_sha256"]!=status["output_sha256"] or restored["admissions"]!=status["admissions"]:
                                raise AssertionError("Server restart changed accepted output or receipts")
                            if {t["task_id"] for t in job_tasks(job_id)}!={t["task_id"] for t in tasks_before}:
                                raise AssertionError("Server restart created new tasks")
                            wait_for(lambda: len([w for w in json.loads(cli("workers")).get("workers", []) if w.get("online")]) == 2)
                            stable_until = time.monotonic() + 0.3
                            while time.monotonic() < stable_until:
                                attempts_after = attempt_snapshot(job_id)
                                if attempts_after != attempts_before:
                                    raise AssertionError("Server restart created or rebound an attempt")
                                time.sleep(0.05)
                            if processes["server"].pid==old_pid:
                                raise AssertionError("Server was not actually restarted")
                            if json.loads(cli("job","submit-graph","--key",key,"--file",request))["job_id"]!=job_id:
                                raise AssertionError("idempotency did not survive restart")
                            restarted = True
                        evaluation = write(key+"-evaluation.json",{"id":key,"expected_version":0,
                            "output_sha256":status["output_sha256"],"verifier_profile":"orders-proof","verifier_version":"1",
                            "verdict":verdict,"evidence_sha256":hashlib.sha256(json.dumps(result,sort_keys=True).encode()).hexdigest()})
                        denied = False
                        try:
                            cli("job","evaluate-graph","--id",job_id,"--file",evaluation)
                        except RuntimeError as error:
                            denied = "403" in str(error)
                        if not denied:
                            raise AssertionError("submitter could self-evaluate without goals:evaluate")
                        run_command(binary,"job","evaluate-graph","--id",job_id,"--file",evaluation,"--config",eval_config)
                        final = json.loads(cli("job","graph","--id",job_id))
                        want_goal = "SUCCEEDED" if verdict=="PASS" else "FAILED"
                        if final["goal_state"]!=want_goal:
                            raise AssertionError(f"business evaluation did not produce {want_goal}: {final}")
                        results.append({"case":name,"job_id":job_id,"amount":expected,"verdict":verdict,
                            "goal_state":want_goal,"workers":sorted({t["worker_id"] for t in roots}),
                            "task_count":len(tasks_before),"admitted_receipts":len(status["admissions"]),
                            "server_process_restart":restarted,"submitter_evaluation_denied":denied,
                            "attempts_before_restart":attempts_before,"attempts_after_restart":attempts_after,
                            "server_pid_before_restart":old_pid if restarted else None,
                            "server_pid_after_restart":processes["server"].pid if restarted else None,
                            "worker_process_pids":{n:processes[n].pid for n in worker_configs}})
                    spec = json.loads(json.dumps(template))
                    spec.update(project_id="ci-flow",workspace={"repository_ref":"repo","base_commit":commit})
                    spec["limits"]["max_replans"] = 0
                    for op in spec["operators"]:
                        if op["kind"] == "agent":
                            op["execution"] = execution("codex")
                    request = write("invalidated-graph.json",spec)
                    job_id = json.loads(cli("job","submit-graph","--key","ci-invalidated","--file",request))["job_id"]
                    if wait_job(job_id)["state"] != "SUCCEEDED":
                        raise AssertionError("invalidation graph did not finish execution")
                    status = json.loads(cli("job","graph","--id",job_id))
                    attempts_before = attempt_snapshot(job_id)
                    rule = next(s for s in spec["sources"] if s["id"] == "discount_rule")
                    event = write("invalidation.json",{"id":"rule-revised","goal_id":status["goal_id"],
                        "graph_generation":status["graph_generation"],"expected_revision":0,"reason":"RULE_CHANGED",
                        "evidence_sha256":hashlib.sha256(b"discount=2000").hexdigest(),
                        "sources":[{"id":rule["id"],"sha256":hashlib.sha256(rule["text"].encode()).hexdigest()}]})
                    denied = False
                    try:
                        cli("job","invalidate-graph","--id",job_id,"--file",event)
                    except RuntimeError as error:
                        denied = "403" in str(error)
                    if not denied:
                        raise AssertionError("submitter could invalidate without separate permission")
                    run_command(binary,"job","invalidate-graph","--id",job_id,"--file",event,"--config",eval_config)
                    old_pid = processes["server"].pid
                    stop("server",hard=True)
                    start("server","server",server_config)
                    def applied():
                        state = json.loads(cli("job","graph-validity","--id",job_id))
                        return state if state["revision"] == 1 and len(state["events"]) == 1 and state["events"][0]["state"] == "APPLIED" else None
                    validity = wait_for(applied)
                    attempts_after = attempt_snapshot(job_id)
                    if attempts_before != attempts_after or processes["server"].pid == old_pid:
                        raise AssertionError("invalidation restart changed attempts or did not restart")
                    evaluation = {"id":"stale-pass","expected_version":0,"expected_validity_revision":1,
                        "output_sha256":status["output_sha256"],"verifier_profile":"orders-proof","verifier_version":"1",
                        "verdict":"PASS","evidence_sha256":hashlib.sha256(b"old proof").hexdigest()}
                    stale = write("invalidated-evaluation.json",evaluation)
                    pass_denied = False
                    try:
                        run_command(binary,"job","evaluate-graph","--id",job_id,"--file",stale,"--config",eval_config)
                    except RuntimeError as error:
                        pass_denied = "IU_INPUT_INVALIDATED" in str(error)
                    if not pass_denied:
                        raise AssertionError("invalidated evidence could PASS")
                    evaluation.update(id="current-fail",verdict="FAIL")
                    failure = write("invalidated-fail.json",evaluation)
                    run_command(binary,"job","evaluate-graph","--id",job_id,"--file",failure,"--config",eval_config)
                    final = json.loads(cli("job","graph","--id",job_id))
                    if final["goal_state"] != "FAILED":
                        raise AssertionError("invalidated Goal did not terminate after FAIL")
                    invalidation = {"revision":validity["revision"],"applied":True,"server_process_restart":True,
                        "attempts_unchanged":True,"unauthorized_denied":denied,"stale_pass_denied":pass_denied,
                        "job_id":job_id,"goal_state":final["goal_state"],"attempts_before":attempts_before,"attempts_after":attempts_after}
                    import_recovery=[]
                    for mode in ("memory","node_result"):
                        # Re-enable new publications for the next isolated source Goal.
                        server_data["server"]["jobs"]["graphs_enabled"]=True
                        Path(server_config).write_text(json.dumps(server_data))
                        stop("server",hard=True);start("server","server",server_config)
                        wait_for(lambda: json.loads(cli("workers")))
                        source_spec=json.loads(json.dumps(template))
                        source_spec.update(version="iu-graph-v3",project_id="ci-flow",workspace={"repository_ref":"repo","base_commit":commit})
                        source_spec["evaluator"]={"profile":"source-proof","version":"1"}
                        source_spec["limits"].update(max_replans=1,timeout_seconds=300)
                        source_spec["nodes"]=[dict(next(n for n in source_spec["nodes"] if n["id"]=="a"),replay_profile="fixture")]
                        source_spec["operators"]=[dict(next(o for o in source_spec["operators"] if o["id"]=="extract"),execution=dict(execution("codex"),replay_safe=True))]
                        source_spec["edges"]=[];source_spec["outputs"]=[{"node":"a","port":"result"}]
                        source_request=write("source-"+mode+".json",source_spec)
                        source_id=json.loads(cli("job","submit-graph","--key","source-"+mode,"--file",source_request))["job_id"]
                        if wait_job(source_id)["state"]!="SUCCEEDED":raise AssertionError("import source execution failed")
                        source_status=json.loads(cli("job","graph","--id",source_id))
                        record_id=next(s["id"] for s in source_spec["sources"] if s["id"]=="order_a")
                        snapshot=json.loads(cli("job","graph-record","--id",source_id,"--node","a","--record",record_id))
                        verify_request=write("verify-"+mode+".json",{"id":"source-proof","node_id":"a","record_id":record_id,
                            "record_sha256":snapshot["record_sha256"],"view_sha256":snapshot["view_sha256"],"expected_revision":0,
                            "profile":"order-source","profile_version":"1","source_id":record_id})
                        verify_denied=False
                        try:cli("job","verify-record","--id",source_id,"--file",verify_request)
                        except RuntimeError as error:verify_denied="403" in str(error)
                        if not verify_denied:raise AssertionError("submitter could self-verify memory")
                        proof=json.loads(run_command(binary,"job","verify-record","--id",source_id,"--file",verify_request,"--config",eval_config))
                        if proof["verdict"]!="PASS":raise AssertionError("independent source proof failed")
                        span=next(n for n in source_spec["nodes"] if n["id"]=="a")["spans"][0]
                        read_body={"id":"bounded-read","node_id":"a","plan_sha256":source_status["plan"]["sha256"],"expected_reads":0,"span":span}
                        read_file=write("read-"+mode+".json",read_body)
                        read_before=json.loads(cli("job","read-source","--id",source_id,"--file",read_file))
                        evidence=hashlib.sha256(b"fixture requires second-generation review").hexdigest()
                        failure_body={"id":"new-review","expected_version":0,"output_sha256":source_status["output_sha256"],
                            "verifier_profile":"source-proof","verifier_version":"1","verdict":"FAIL","evidence_sha256":evidence}
                        failure_file=write("source-eval-"+mode+".json",failure_body)
                        run_command(binary,"job","evaluate-graph","--id",source_id,"--file",failure_file,"--config",eval_config)
                        source_status=json.loads(cli("job","graph","--id",source_id))
                        target_spec=json.loads(json.dumps(source_spec))
                        grant_id="approved_"+mode
                        if mode=="memory":target_spec["imports"]=[{"grant_id":grant_id,"node_ids":["a"]}]
                        else:target_spec["reuse"]=[{"grant_id":grant_id,"node_id":"a"}]
                        def prepare_publication(spec,grant_id,proposal_id):
                            target_file=write("target-"+proposal_id+".json",spec)
                            plan=json.loads(cli("job","compile-graph","--file",target_file))
                            grant={"id":grant_id,"node_id":"a","view_sha256":snapshot["view_sha256"],"record_ids":[record_id],
                                "expected_revision":0,"target_generation":2,"target_plan_sha256":plan["sha256"],"expires_at":int(time.time()*1000)+60000}
                            if mode=="node_result":grant["purpose"]="node_result"
                            grant_file=write("grant-"+proposal_id+".json",grant)
                            run_command(binary,"job","issue-import","--id",source_id,"--file",grant_file,"--config",eval_config)
                            proposal={"proposal":{"id":proposal_id,"goal_id":source_status["goal_id"],"evaluation_id":source_status["evaluation_id"],
                                "expected_plan_revision":1,"expected_graph_generation":1,"reason_code":"NEW_EVIDENCE",
                                "evidence":{"failure_class":"TEST_FAILURE","evidence":[{"type":"iu_graph_evaluation","fact":"output="+source_status["output_sha256"]+";evidence="+evidence+";verdict=FAIL"}]},
                                "proposed_plan":{"strategy":"review-frozen-evidence","strategy_class":"bounded-retry","dependency_signature":"single",
                                    "required_capabilities":["runtime:codex_exec"],"key_assumptions":["fixture source unchanged"],"evaluation_strategy":"source-proof",
                                    "side_effect_class":"workspace","job_spec_hash":plan["sha256"]},
                                "strategy_delta":{"changed_dimensions":["strategy"],"summary":"explicit authorized next generation"},
                                "progress":{"accepted_checks":1,"failed_checks":1,"unknown_checks":1,"resolved_assumptions":1,"unresolved_blockers":1}},"job_spec":spec}
                            return write("proposal-"+proposal_id+".json",proposal)
                        revoked_id="revoked_"+mode
                        revoked_spec=json.loads(json.dumps(target_spec))
                        revoked_spec["imports" if mode=="memory" else "reuse"][0]["grant_id"]=revoked_id
                        revoked_proposal=prepare_publication(revoked_spec,revoked_id,"rejected-"+mode)
                        run_command(binary,"job","revoke-import","--id",source_id,"--key",revoked_id,"--config",eval_config)
                        revoked_denied=False;revoked_denial=""
                        try:run_command(binary,"job","propose-graph","--id",source_id,"--file",revoked_proposal,"--config",eval_config)
                        except RuntimeError as error:
                            if "IMPORT_GRANT_NOT_USABLE" not in str(error):raise
                            revoked_denied=True;revoked_denial=str(error)
                        if not revoked_denied:raise AssertionError("revoked grant published")
                        with sqlite3.connect(f"file:{root/'server'/'state.db'}?mode=ro",uri=True) as db:
                            if db.execute("SELECT active_graph_generation FROM goals WHERE id=?",(source_status["goal_id"],)).fetchone()[0]!=1:raise AssertionError("rejected import partially published")
                        proposal_file=prepare_publication(target_spec,grant_id,"accepted-"+mode)
                        stopped=[]
                        try:
                            if mode=="memory":
                                for worker_name in worker_configs:
                                    stop(worker_name);stopped.append(worker_name)
                            publication=json.loads(run_command(binary,"job","propose-graph","--id",source_id,"--file",proposal_file,"--config",eval_config))
                            target_id=publication["job"]["job_id"]
                            replay=json.loads(run_command(binary,"job","propose-graph","--id",source_id,"--file",proposal_file,"--config",eval_config))
                            if replay["job"]["job_id"]!=target_id or replay["decision"]["existing"] is not True:raise AssertionError("publication replay duplicated a job")
                            before_restart=attempt_snapshot(target_id)
                            if before_restart:raise AssertionError("target executed before the recovery boundary")
                            # Disable new graph submissions, then hard-restart the Server.
                            # The already accepted graph must still finish without new duplicate attempts.
                            server_data["server"]["jobs"]["graphs_enabled"]=False
                            Path(server_config).write_text(json.dumps(server_data));old_pid=processes["server"].pid
                            stop("server",hard=True);start("server","server",server_config)
                        finally:
                            for worker_name in stopped:
                                start(worker_name,"worker",worker_configs[worker_name])
                        if wait_job(target_id)["state"]!="SUCCEEDED":raise AssertionError("accepted graph did not recover with submissions disabled")
                        target_status=json.loads(cli("job","graph","--id",target_id));attempts=attempt_snapshot(target_id)
                        expected_attempts=1 if mode=="memory" else 0
                        if len(attempts)!=expected_attempts or len(job_tasks(target_id))!=expected_attempts:raise AssertionError("recovery invented or duplicated execution")
                        grant=json.loads(run_command(binary,"job","get-import","--id",source_id,"--key",grant_id,"--config",eval_config))
                        if grant["state"]!="CONSUMED" or grant["target_job"]!=target_id:raise AssertionError("grant consumption was not durable")
                        read_after=json.loads(cli("job","read-source","--id",source_id,"--file",read_file))
                        if read_after!=read_before:raise AssertionError("read receipt changed across restart")
                        with sqlite3.connect(f"file:{root/'server'/'state.db'}?mode=ro",uri=True) as db:
                            prefix="import/%" if mode=="memory" else "reuse/%"
                            pins=db.execute("SELECT count(*) FROM artifact_refs WHERE ref_type='iu_memory' AND ref_id LIKE ?",(prefix,)).fetchone()[0]
                        if pins<1:raise AssertionError("origin evidence lost its pin")
                        with sqlite3.connect(f"file:{root/'server'/'state.db'}?mode=ro",uri=True) as db:
                            node_row=db.execute("SELECT input_view,output_view,task_id FROM iu_nodes WHERE goal_id=? AND generation=2 AND node_id='a'",(source_status["goal_id"],)).fetchone()
                            carried=json.loads(node_row[0] if mode=="memory" else node_row[1])
                            origin=carried["records"][0].get("origin",{})
                            if carried["version"]!="iu-memory-view-v2" or origin.get("source",{}).get("job_id")!=source_id or origin.get("record_id")!=record_id or origin.get("record_sha256")!=snapshot["record_sha256"]:
                                raise AssertionError("inline original record identity did not survive transmission/recovery")
                            if mode=="memory":
                                task_spec=json.loads(db.execute("SELECT spec FROM tasks WHERE id=?",(node_row[2],)).fetchone()[0])
                                if "iu_memory_origin_v1" not in task_spec.get("required_capabilities",[]):raise AssertionError("new memory wire was not capability fenced")

                        if mode=="node_result" and target_status["nodes"][0]["execution_kind"]!="REUSED":raise AssertionError("reuse not exposed as separate execution fact")
                        output=json.loads(cli("job","result","--id",target_id))
                        if output["outputs"]["a/result"]!=6000:raise AssertionError("import/reuse changed source result")
                        evaluation=write("target-eval-"+mode+".json",{"id":"current-pass","expected_version":0,
                            "output_sha256":target_status["output_sha256"],"verifier_profile":"source-proof","verifier_version":"1",
                            "verdict":"PASS","evidence_sha256":hashlib.sha256(b"fixture current source independently checked").hexdigest()})
                        run_command(binary,"job","evaluate-graph","--id",target_id,"--file",evaluation,"--config",eval_config)
                        import_recovery.append({"mode":mode,"source_job":source_id,"target_job":target_id,"generation":2,"grant_consumed":True,
                            "server_process_restart":processes["server"].pid!=old_pid,"submissions_disabled_recovery":True,"publication_idempotent":True,
                            "origin_pins":pins,"attempts_before_restart":len(before_restart),"revoked_denial":revoked_denial,"attempts":len(attempts),"tasks":len(job_tasks(target_id)),"revoked_denied":revoked_denied,
                            "verification_authorized":True,"submitter_verification_denied":verify_denied,"source_read_receipt_preserved":True,
                            "origin_forwarded":True,"source_output":6000,"recovered_output":output["outputs"]["a/result"]})
                    # v5 acceptance and bounded recovery through real CLI / Server / Workers.
                    server_data["server"]["jobs"]["graphs_enabled"]=True
                    server_data["server"]["jobs"]["graph_coverage_verifiers"]={"orders":{"version":"1","kind":"orders-v1","scope":"orders","timeout_ms":1000}}
                    Path(server_config).write_text(json.dumps(server_data))
                    stop("server",hard=True);start("server","server",server_config)
                    wait_for(lambda: json.loads(cli("workers")))
                    coverage_spec=json.loads((Path(__file__).resolve().parents[1]/"examples/iu-graph/coverage-v5/orders.json").read_text())
                    coverage_spec.update(project_id="ci-flow",workspace={"repository_ref":"repo","base_commit":commit})
                    coverage_spec["operators"][0]["execution"]=execution("codex")
                    coverage_spec["nodes"][0]["instructions"]+=" fixture-coverage-v5"
                    coverage_file=write("coverage-v5.json",coverage_spec)
                    coverage_source=json.loads(cli("job","submit-graph","--key","coverage-v5","--file",coverage_file))["job_id"]
                    if wait_job(coverage_source)["state"]!="SUCCEEDED":raise AssertionError("v5 initial execution failed")
                    status_v5=json.loads(cli("job","graph","--id",coverage_source))
                    def compact_hash(value):return hashlib.sha256(json.dumps(value,separators=(",",":"),ensure_ascii=False).encode()).hexdigest()
                    def coverage_proof(job_id,status_v5,name):
                        request=write(name+"-request.json",{"id":name,"expected_validity_revision":status_v5["validity_revision"],"output_sha256":status_v5["output_sha256"],"profile":"orders","profile_version":"1"})
                        return json.loads(run_command(binary,"job","verify-coverage","--id",job_id,"--file",request,"--config",eval_config))
                    first_proof=coverage_proof(coverage_source,status_v5,"initial-proof")
                    if first_proof["verdict"]!="FAIL":raise AssertionError("fixture violation was not detected")
                    evaluation={"id":"v5-initial-fail","expected_version":status_v5["evaluation_version"],"output_sha256":status_v5["output_sha256"],"verifier_profile":coverage_spec["evaluator"]["profile"],"verifier_version":coverage_spec["evaluator"]["version"],"verdict":"FAIL","evidence_sha256":first_proof["evidence_sha256"],"coverage_proof":{"receipt_id":"initial-proof","sha256":compact_hash(first_proof)}}
                    run_command(binary,"job","evaluate-graph","--id",coverage_source,"--file",write("v5-initial-fail.json",evaluation),"--config",eval_config)
                    status_v5=json.loads(cli("job","graph","--id",coverage_source))
                    span=status_v5["plan"]["spec"]["nodes"][0]["spans"][0]
                    recipe={"version":"iu-recovery-recipe-v1","id":"ci-v5-recovery","prior_plan_sha256":status_v5["plan"]["sha256"],"evaluation_id":status_v5["evaluation_id"],"evaluation_sha256":compact_hash(status_v5["evaluation"]),"reads":[{"node_id":"extract","span":span}],"instructions":{"extract":"\nfixture-coverage-repaired: recheck frozen original orders and global rules."}}
                    recipe_file=write("v5-recovery-recipe.json",recipe)
                    with sqlite3.connect(f"file:{root/'server'/'state.db'}?mode=ro",uri=True) as db:
                        frozen_before=db.execute("SELECT context_json FROM iu_nodes WHERE goal_id=? AND generation=1 AND node_id='extract'",(status_v5["goal_id"],)).fetchone()[0]
                    publication=json.loads(run_command(binary,"job","recover-graph","--id",coverage_source,"--file",recipe_file,"--config",eval_config))
                    coverage_target=publication["job"]["job_id"]
                    if wait_job(coverage_target)["state"]!="SUCCEEDED":raise AssertionError("v5 corrected execution failed")
                    target_v5=json.loads(cli("job","graph","--id",coverage_target))
                    if target_v5["graph_generation"]!=2:raise AssertionError("v5 recovery generation mismatch")
                    final_proof=coverage_proof(coverage_target,target_v5,"current-proof")
                    if final_proof["verdict"]!="PASS":raise AssertionError("corrected output did not pass independent verification")
                    attempts_before=attempt_snapshot(coverage_source)+attempt_snapshot(coverage_target)
                    reads_before=json.loads(run_command(binary,"job","read-source-status","--id",coverage_source,"--node","extract","--config",eval_config))
                    old_pid=processes["server"].pid
                    stop("server",hard=True);start("server","server",server_config)
                    wait_for(lambda: json.loads(cli("workers")))
                    replay=json.loads(run_command(binary,"job","recover-graph","--id",coverage_source,"--file",recipe_file,"--config",eval_config))
                    if replay["job"]["job_id"]!=coverage_target or not replay["decision"]["existing"]:raise AssertionError("v5 recovery replay changed publication")
                    reads_after=json.loads(run_command(binary,"job","read-source-status","--id",coverage_source,"--node","extract","--config",eval_config))
                    attempts_after=attempt_snapshot(coverage_source)+attempt_snapshot(coverage_target)
                    with sqlite3.connect(f"file:{root/'server'/'state.db'}?mode=ro",uri=True) as db:
                        frozen_after=db.execute("SELECT context_json FROM iu_nodes WHERE goal_id=? AND generation=1 AND node_id='extract'",(status_v5["goal_id"],)).fetchone()[0]
                        publications=db.execute("SELECT count(*) FROM iu_recovery_publications WHERE goal_id=?",(status_v5["goal_id"],)).fetchone()[0]
                        pins=db.execute("SELECT count(*) FROM artifact_refs WHERE ref_id LIKE ?",("coverage/"+status_v5["goal_id"]+"/%",)).fetchone()[0]
                    if attempts_before!=attempts_after or len(attempts_after)!=2 or frozen_before!=frozen_after or publications!=1 or pins<2 or reads_before!=reads_after or reads_after["reads_used"]!=1:raise AssertionError("v5 restart lost provenance, counts or frozen inputs")
                    evaluation.update(id="v5-current-pass",expected_version=target_v5["evaluation_version"],output_sha256=target_v5["output_sha256"],verdict="PASS",evidence_sha256=final_proof["evidence_sha256"],coverage_proof={"receipt_id":"current-proof","sha256":compact_hash(final_proof)})
                    run_command(binary,"job","evaluate-graph","--id",coverage_target,"--file",write("v5-current-pass.json",evaluation),"--config",eval_config)
                    final_v5=json.loads(cli("job","graph","--id",coverage_target))
                    if final_v5["goal_state"]!="SUCCEEDED":raise AssertionError("v5 Goal did not independently pass")
                    coverage_recovery={"source_job":coverage_source,"target_job":coverage_target,"generation":2,"initial_verdict":"FAIL","final_verdict":"PASS","goal_state":final_v5["goal_state"],"server_process_restart":processes["server"].pid!=old_pid,"publication_idempotent":True,"publications":publications,"attempts_before":len(attempts_before),"attempts_after":len(attempts_after),"reads_before":reads_before["reads_used"],"reads_after":reads_after["reads_used"],"old_context_unchanged":frozen_before==frozen_after,"origin_pins":pins}
                    return {"real_model_calls":False,"cases":results,"server_processes":1,"worker_processes":2,
                            "invalidation":invalidation,"import_recovery":import_recovery,"coverage_recovery":coverage_recovery,
                            "restart_scope":"completed execution waiting for authorized business evaluation"}
                record("native_iu_graphs", native_graph_flow)


            if args.workspace_benchmark:
                def workspace_benchmark():
                    samples = []
                    for index in range(10):
                        started = time.monotonic()
                        job_id = submit(f"workspace-benchmark-{index}", base_spec())
                        current = wait_job(job_id)
                        if current["state"] != "SUCCEEDED":
                            raise AssertionError(f"workspace benchmark Job failed: {current}")
                        task = job_tasks(job_id)[0]
                        with sqlite3.connect(root / "server" / "state.db") as database:
                            rows = database.execute("SELECT body FROM events WHERE task=? ORDER BY seq", (task["task_id"],)).fetchall()
                        measured = []
                        for (body,) in rows:
                            event = json.loads(body)
                            if event.get("type") == "workspace.prepared":
                                measured.append(json.loads(base64.b64decode(event["payload_json"])))
                        if len(measured) != 1:
                            raise AssertionError("missing or duplicate accepted workspace measurement")
                        samples.append(dict(measured[0], job_id=job_id, worker_id=task["worker_id"], job_ms=round((time.monotonic()-started)*1000, 3)))
                    ratio = sum(item["cache_hit"] for item in samples) / len(samples)
                    if ratio < 0.9:
                        raise AssertionError(f"template reuse ratio {ratio} below 90%")
                    if len({item["template_fingerprint"] for item in samples}) != 1:
                        raise AssertionError("benchmark did not reuse one template identity")
                    latency = sorted(item["job_ms"] for item in samples)
                    return {"scope": "ten_complete_fixture_jobs_after_single_mapreduce_warmup", "real_model_calls": False,
                            "os": platform.system(), "arch": platform.machine(), "samples": samples,
                            "hit_ratio": ratio, "job_p50_ms": latency[5], "job_p95_ms": latency[9]}
                record("workspace_ten_jobs", workspace_benchmark)
            if args.readiness_check:
                def readiness_check():
                    def signaled_workers():
                        items = json.loads(cli("workers")).get("workers", [])
                        return items if any(w.get("readiness") for w in items) else None
                    workers = wait_for(signaled_workers)
                    observed = [w["worker_id"] for w in workers if w.get("readiness")]
                    with sqlite3.connect(root / "server" / "state.db") as database:
                        rows = database.execute("SELECT body FROM events ORDER BY rowid").fetchall()
                    evidence = []
                    for (body,) in rows:
                        event = json.loads(body)
                        if event.get("type") == "scheduler.observation":
                            evidence.append(json.loads(base64.b64decode(event["payload_json"])))
                    if not evidence or any(item["scoring_enabled"] for item in evidence):
                        raise AssertionError("missing observation or unexpectedly enabled scoring")
                    if args.workspace_benchmark and not any(item["selected"]["signal_state"] == "FRESH" for item in evidence):
                        raise AssertionError("fresh Worker hints never reached scheduler observation")
                    return {"workers_with_signals": observed, "dispatch_observations": len(evidence), "scoring_enabled": False}
                record("readiness_delivery", readiness_check)

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
                expected_jobs = {"CANCELED": 1, "FAILED": 1, "SUCCEEDED": (12 if args.workspace_benchmark else 2) + (9 if args.iu_graph_check else 0)}
                if jobs != expected_jobs:
                    raise AssertionError(f"unexpected Job state totals: {jobs}")
                return {"sqlite_integrity": integrity, "job_states": jobs, "task_states": tasks}

            record("shutdown_and_integrity", shutdown_and_integrity)
            report["summary"] = {"status": "PASSED", "steps_passed": len(report["steps"]),
                                 "successful_jobs": (12 if args.workspace_benchmark else 2) + (9 if args.iu_graph_check else 0), "canceled_jobs": 1, "failed_jobs": 1}
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
        if failure:
            report["log_tails"] = {source.name: source.read_text(errors="replace")[-4000:]
                                   for source in logs_output.glob("*.log")}
        report["finished_at"] = utc_now()
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print(json.dumps(report["summary"], sort_keys=True), flush=True)
        print(f"Report: {output}", flush=True)
    if failure:
        raise failure


if __name__ == "__main__":
    main()
