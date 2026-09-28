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
        "go", "test", "./internal/store", "./internal/control", "./internal/server",
        "-run",
        "V8ControlOperationsSchema|AgentControlOperationReceiptValidation|AgentControlSafeCancelIdempotentAndFenced|AgentControlRejectsStaleGenerationBeforeMutation|AgentControlUnsupportedInteractiveActionsFailClosed",
        "-count=1", "-v",
    ])

    source = pathlib.Path("internal/server/control_write.go").read_text(encoding="utf-8")
    migration = pathlib.Path("internal/store/migrations.go").read_text(encoding="utf-8")
    violations = []
    for required in [
        "control_operations",
        "ATTEMPT_FENCED",
        "OPERATION_CONFLICT",
        "CAPABILITY_UNSUPPORTED",
        "ExpectedGeneration",
        "ExpectedAttemptID",
        "jobState(ctx, q, j, \"STOPPING\", \"USER_CANCEL\"",
    ]:
        if required not in source:
            violations.append("safe control source missing: " + required)
    for required in [
        "PRIMARY KEY(principal_id,operation_id)",
        "attempt_id TEXT NOT NULL REFERENCES attempts(id)",
        "generation INTEGER NOT NULL",
    ]:
        if required not in migration:
            violations.append("control operation migration missing: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-safe-write.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "durable_operation_receipt": True,
            "same_request_replay": True,
            "different_payload_conflict": True,
            "attempt_generation_fencing": True,
            "cancel_only_certified_write": True,
            "unsupported_interactive_actions_fail_closed": True,
            "job_cancel_semantics_preserved": True,
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
        sys.stderr.write("agent control safe-write violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
