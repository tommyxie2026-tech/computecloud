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
        "./internal/server", "./internal/worker",
        "-run", "SessionRef|SessionReconnect|Reconnect|Resume",
        "-count=1", "-v",
    ])

    dispatch = pathlib.Path("internal/server/control_dispatch.go").read_text(encoding="utf-8")
    session = pathlib.Path("internal/server/session_runtime.go").read_text(encoding="utf-8")
    http = pathlib.Path("internal/server/control_http.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/control_execution.go").read_text(encoding="utf-8")
    violations = []

    for required in [
        'case "resume":',
        "control.CapabilitySessionResume",
        "explicit session_ref required for resume",
        "nativeSession != payload.SessionRef",
    ]:
        if required not in dispatch:
            violations.append("missing Server resume fence: " + required)

    for required in [
        'event.Type != "session.started"',
        '"session.resumed"',
        "current_generation",
        "event.AttemptId",
    ]:
        if required not in session:
            violations.append("missing live session_ref ownership guard: " + required)

    for required in [
        "httpSessionResume",
        'OperationType: "resume"',
        "ExpectedGeneration",
        "ExpectedResourceVersion",
    ]:
        if required not in http:
            violations.append("missing Resume HTTP contract: " + required)

    for required in [
        'case "resume":',
        "sessionProvider.Resume",
        "runtime resume lost Attempt ownership",
        '"UNKNOWN"',
    ]:
        if required not in worker:
            violations.append("missing Worker reconnect safety: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-resume.v1",
        "status": status,
        "real_model_calls": False,
        "scope": "authoritative in-attempt runtime/session reconnect only",
        "coverage": {
            "explicit_native_session_ref": True,
            "attempt_generation_fencing": True,
            "runtime_capability_fail_closed": True,
            "worker_resume_at_most_once": True,
            "runtime_ref_durable_replacement": True,
            "resume_persistence_failure_unverifiable": True,
            "terminal_job_revival_forbidden": True,
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
        sys.stderr.write("session reconnect gate violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
