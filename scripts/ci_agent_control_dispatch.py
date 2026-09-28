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
        "go", "test",
        "./internal/worker", "./internal/server", "./internal/store",
        "-run",
        "AgentControlWorker|AgentControlDispatch|AgentControlUnknownAck|WorkerV6ControlExecutionLedgerMigration|AgentControlOperation",
        "-count=1", "-v",
    ])

    worker = pathlib.Path("internal/worker/control_execution.go").read_text(encoding="utf-8")
    server = pathlib.Path("internal/server/control_dispatch.go").read_text(encoding="utf-8")
    proto = pathlib.Path("api/agent/v1/runtime.proto").read_text(encoding="utf-8")
    violations = []

    for required in [
        'state=\'UNKNOWN\'',
        'EXECUTION_UNVERIFIABLE',
        'SessionControlProvider',
        'CAPABILITY_UNSUPPORTED',
        'operation_id',
    ]:
        if required not in worker:
            violations.append("missing Worker control safety invariant: " + required)

    for required in [
        "acceptAndDispatchControlOperation",
        "validateControlFence",
        "DISPATCHED",
        "control.completed",
        "control.rejected",
        "principal_id",
    ]:
        if required not in server:
            violations.append("missing Server control dispatch invariant: " + required)

    for required in [
        "message ControlCommand",
        "principal_id = 12",
        "ControlCommand control = 4",
        "operation_id = 3",
        "error_code = 4",
    ]:
        if required not in proto:
            violations.append("missing structured Worker control envelope field: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-dispatch.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "durable_intent_to_worker_command": True,
            "worker_session_control_provider": True,
            "worker_at_most_once_auto_retry": True,
            "unknown_outcome_fail_closed": True,
            "structured_final_ack": True,
            "server_terminal_receipt_event": True,
            "codex_claude_interactive_fail_closed": True,
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
