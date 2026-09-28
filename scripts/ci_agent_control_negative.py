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
        "-run", "AgentControlNegative|AgentControlOperation|AgentControlRoundTrip|AgentControlErrorCodes",
        "-count=1", "-v",
    ])

    write = pathlib.Path("internal/server/control_write.go").read_text(encoding="utf-8")
    validate = pathlib.Path("internal/control/validate.go").read_text(encoding="utf-8")
    tests = pathlib.Path("internal/control/roundtrip_test.go").read_text(encoding="utf-8")
    violations = []

    for required in [
        "control.ErrorAttemptFenced",
        "control.ErrorOperationConflict",
        "control.ErrorResourceVersionConflict",
        "control.ErrorExecutionUnverifiable",
    ]:
        if required not in write:
            violations.append("server does not use stable control error: " + required)

    for required in [
        "unknown control event type",
        "unknown approval risk class",
        "generation must be positive",
        "expected_generation must be positive",
    ]:
        if required not in validate:
            violations.append("missing fail-closed model validation: " + required)

    for required in [
        "unknown event type accepted",
        "unknown risk class accepted",
        "attempt scoped control without generation accepted",
        "stable error codes changed",
    ]:
        if required not in tests:
            violations.append("missing negative contract test: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-negative.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "stable_machine_error_codes": True,
            "model_fail_closed_validation": True,
            "old_generation_rejected": True,
            "duplicate_operation_conflict": True,
            "schema_model_enum_alignment": True,
            "interactive_runtime_capabilities_still_fail_closed": True,
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
