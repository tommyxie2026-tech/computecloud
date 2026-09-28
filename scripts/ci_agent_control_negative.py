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
        "go", "test", "./internal/control", "./internal/server",
        "-run",
        "AgentControlRejectsUnknownSessionState|AgentControlJSONRoundTrip|AgentControlOperation(IdempotencyAndFencing|ReleasedAttemptFailsClosed|UnsupportedCapabilityFailsClosed)",
        "-count=1", "-v",
    ])

    write = pathlib.Path("internal/server/control_write.go").read_text(encoding="utf-8")
    errors = pathlib.Path("internal/control/errors.go").read_text(encoding="utf-8")
    violations = []

    required_errors = [
        "CAPABILITY_UNSUPPORTED",
        "ATTEMPT_FENCED",
        "OPERATION_CONFLICT",
        "RESOURCE_VERSION_CONFLICT",
        "EVENT_CURSOR_EXPIRED",
        "EXECUTION_UNVERIFIABLE",
    ]
    for value in required_errors:
        if value not in errors:
            violations.append("missing stable control error: " + value)

    for required in [
        "validateControlCapability",
        "CapabilityInteractiveInput",
        "CapabilityInterrupt",
        "CapabilityApproval",
        "CapabilitySessionResume",
    ]:
        if required not in write:
            violations.append("missing fail-closed capability gate: " + required)

    capability_pos = write.find("validateControlCapability(ctx, q, in)")
    insert_pos = write.find("INSERT INTO control_operations")
    if capability_pos < 0 or insert_pos < 0 or capability_pos > insert_pos:
        violations.append("capability gate must execute before durable acceptance")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-negative.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "stable_protocol_errors": True,
            "model_round_trip": True,
            "unknown_session_state_rejected": True,
            "unsupported_interactive_control_rejected": True,
            "unsupported_control_not_persisted": True,
            "stale_attempt_rejected": True,
            "released_attempt_rejected": True,
            "duplicate_operation_conflict": True,
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
        sys.stderr.write("agent control negative violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
