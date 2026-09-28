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
        "./internal/server", "./internal/worker", "./internal/adapter",
        "-run", "AgentControlDispatchResume|AgentControlWorkerResumesSession|AgentControlWorkerRejectsResume|AgentControlPersistsRuntimeSessionRefEarly|RuntimeParserEmitsNormalizedSessionStarted",
        "-count=1", "-v",
    ])

    server = pathlib.Path("internal/server/control_dispatch.go").read_text(encoding="utf-8")
    http = pathlib.Path("internal/server/control_http.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/control_execution.go").read_text(encoding="utf-8")
    adapter = pathlib.Path("internal/adapter/control_contract.go").read_text(encoding="utf-8")
    parser = pathlib.Path("internal/adapter/adapter.go").read_text(encoding="utf-8")
    results = pathlib.Path("internal/server/results.go").read_text(encoding="utf-8")
    violations = []

    for required in [
        'case "resume":',
        "control.CapabilitySessionResume",
        "native_session",
    ]:
        if required not in server:
            violations.append("missing Server resume invariant: " + required)

    for required in [
        "httpSessionResume",
        'OperationType: "resume"',
        "ExpectedAttemptID",
        "ExpectedGeneration",
    ]:
        if required not in http:
            violations.append("missing Resume HTTP fencing invariant: " + required)

    for required in [
        'case "resume":',
        "sessionProvider.Resume",
        'workspaceState != "IN_USE"',
        'environmentState != "ACTIVE"',
        "adapter.RuntimeUnknown",
        "runtime_transport",
        "runtime_ref",
    ]:
        if required not in worker:
            violations.append("missing Worker resume compatibility invariant: " + required)

    for required in [
        "SessionControlProvider",
        "ControlResumeRequest",
        "Resume(context.Context",
    ]:
        if required not in adapter:
            violations.append("missing Runtime resume contract: " + required)

    for required in ["session.started", "session_ref"]:
        if required not in parser:
            violations.append("missing normalized early session event: " + required)

    for required in ["persistRuntimeSessionRef", "native_session", "SESSION_REF_CONFLICT"]:
        if required not in results:
            violations.append("missing durable early session persistence: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-resume.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "early_runtime_session_persistence": True,
            "explicit_session_ref": True,
            "current_attempt_generation_fencing": True,
            "workspace_in_use_required": True,
            "environment_active_required": True,
            "runtime_rebind_persisted": True,
            "worker_resume_at_most_once": True,
            "released_attempt_resume_forbidden_by_control_fence": True,
            "post_completion_continuation_out_of_scope": True,
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
        sys.stderr.write("resume gate violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
