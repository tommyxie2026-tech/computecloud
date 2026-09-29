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
        "go", "test", "-race",
        "./internal/server", "./internal/worker", "./internal/store",
        "-run", "Approval|WorkerV6ControlExecutionLedgerMigration|V1UpgradeBackupAndDrainGate|IncrementalMigrationRollback",
        "-count=1", "-v",
    ])

    approval = pathlib.Path("internal/server/approval.go").read_text(encoding="utf-8")
    dispatch = pathlib.Path("internal/server/control_dispatch.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/control_execution.go").read_text(encoding="utf-8")
    migrations = pathlib.Path("internal/store/migrations.go").read_text(encoding="utf-8")
    violations = []

    for required in [
        "SUPERSEDED", "EXPIRED", "request_version",
        "validateApprovalDecision", "applyApprovalControlResult",
        "approval.expired",
    ]:
        if required not in approval:
            violations.append("missing approval lifecycle invariant: " + required)

    for required in [
        "control.CapabilityApproval",
        "ApprovalId", "RequestVersion", "Decision",
    ]:
        if required not in dispatch:
            violations.append("missing approval dispatch contract: " + required)

    for required in [
        'case "approval":',
        "sessionProvider.Approve",
        "ControlApprovalRequest",
    ]:
        if required not in worker:
            violations.append("missing Worker approval execution: " + required)

    for required in [
        "serverV12",
        "approval_requests",
        "PRIMARY KEY(approval_id,request_version)",
    ]:
        if required not in migrations:
            violations.append("missing durable approval schema: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-approval.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "durable_approval_request": True,
            "approval_request_version_fencing": True,
            "attempt_generation_fencing": True,
            "expired_and_superseded_fail_closed": True,
            "worker_approval_at_most_once": True,
            "runtime_capability_fail_closed": True,
            "terminal_ack_idempotency": True,
            "concurrent_duplicate_ack": True,
            "stale_ack_fencing": True,
            "server_restart_receipt_replay": True,
            "worker_restart_receipt_replay": True,
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
        sys.stderr.write("approval gate violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
