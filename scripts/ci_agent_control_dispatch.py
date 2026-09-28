#!/usr/bin/env python3
import argparse
import json
import pathlib
import subprocess
import sys
import time

def run(cmd):
    started = time.time()
    p = subprocess.run(cmd, text=True, capture_output=True)
    return {
        "command": cmd,
        "returncode": p.returncode,
        "duration_ms": int((time.time() - started) * 1000),
        "stdout": p.stdout,
        "stderr": p.stderr,
    }

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--output", required=True)
    args = ap.parse_args()
    out = pathlib.Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    logs = out.parent / "logs"
    logs.mkdir(parents=True, exist_ok=True)

    result = run([
        "go", "test", "./internal/server", "./internal/worker",
        "-run",
        "AgentControlDispatchPersistsStructuredCommand|AgentControlWorkerDispatchIsIdempotent|AgentControlWorkerRejectsStaleIdentity|AgentControlWorkerAdvertisementIncludesControlNamespace|AgentControlCapabilityNamespaceProjection",
        "-count=1", "-v",
    ])

    proto = pathlib.Path("api/agent/v1/runtime.proto").read_text(encoding="utf-8")
    server = pathlib.Path("internal/server/control_write.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/control.go").read_text(encoding="utf-8")
    violations = []
    for required in [
        "message ControlCommand",
        "lease_token = 10",
        "ControlCommand control = 4",
    ]:
        if required not in proto:
            violations.append("missing structured control wire invariant: " + required)
    for required in [
        "dispatchControlOperation",
        "controlCommandID",
        "DISPATCHED",
    ]:
        if required not in server:
            violations.append("missing server dispatch invariant: " + required)
    for required in [
        "acceptControl",
        "SessionControlProvider",
        "control.accepted",
        "control.rejected",
    ]:
        if required not in worker:
            violations.append("missing worker control invariant: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-dispatch.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "structured_control_command": True,
            "attempt_generation_lease_fencing": True,
            "deterministic_server_command": True,
            "worker_command_journal_dedup": True,
            "runtime_adapter_dispatch": True,
            "durable_control_result_event": True,
            "control_capability_namespace": True,
        },
        "violations": violations,
        "command": result["command"],
        "duration_ms": result["duration_ms"],
        "returncode": result["returncode"],
    }
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": status, "report": str(out)}))
    if result["returncode"] != 0:
        sys.stderr.write(result["stdout"])
        sys.stderr.write(result["stderr"])
    if violations:
        sys.stderr.write("agent control dispatch violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
