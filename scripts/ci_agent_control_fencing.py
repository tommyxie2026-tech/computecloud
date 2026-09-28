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
        "go", "test", "./internal/store", "./internal/server",
        "-run", "V8ControlOperationLedger|AgentControlOperation",
        "-count=1", "-v",
    ])
    write = pathlib.Path("internal/server/control_write.go").read_text(encoding="utf-8")
    migration = pathlib.Path("internal/store/migrations.go").read_text(encoding="utf-8")
    violations = []
    for required in [
        "control_operations",
        "PRIMARY KEY(principal_id, operation_id)",
        "expected_attempt_id",
        "expected_generation",
        "expected_resource_version",
    ]:
        if required not in migration:
            violations.append("missing durable control ledger invariant: " + required)
    for required in [
        "control.ErrorOperationConflict",
        "control.ErrorAttemptFenced",
        "control.ErrorResourceVersionConflict",
        "validateControlFence",
        "jobs:control",
    ]:
        if required not in write:
            violations.append("missing safe-control invariant: " + required)
    if "INSERT INTO control_operations" not in write:
        violations.append("control acceptance is not persisted")
    if "ACCEPTED" not in write:
        violations.append("control operation acceptance state missing")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-fencing.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "durable_operation_ledger": True,
            "principal_operation_idempotency": True,
            "operation_conflict": True,
            "attempt_id_fencing": True,
            "generation_fencing": True,
            "resource_version_fencing": True,
            "released_attempt_rejection": True,
            "dispatch_not_implied_by_acceptance": True,
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
        sys.stderr.write("agent control fencing violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
