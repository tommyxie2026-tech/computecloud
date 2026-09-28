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
        "./internal/server", "./internal/worker", "./internal/store", "./internal/control",
        "-run", "ACP4|AgentControlWorkerExecutesCommandAtMostOnce|V12ApprovalAndSessionSchema|AgentControlSchemaEnumsStayAligned",
        "-count=1", "-v",
    ])

    approval = pathlib.Path("internal/server/control_approval.go").read_text(encoding="utf-8")
    dispatch = pathlib.Path("internal/server/control_dispatch.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/control_execution.go").read_text(encoding="utf-8")
    migrations = pathlib.Path("internal/store/migrations.go").read_text(encoding="utf-8")

    violations = []
    for required in [
        "approval_requests",
        "SUPERSEDED",
        "decision_operation_id",
        "runtime_session_ref",
        "request_version",
    ]:
        if required not in migrations:
            violations.append("missing ACP-4 durable schema invariant: " + required)
    for required in [
        "expireApprovals",
        "validateApprovalDecisionControl",
        "validateResumeControl",
        "explicit session_ref required",
    ]:
        if required not in approval:
            violations.append("missing ACP-4 server invariant: " + required)
    for required in [
        'case "approval"',
        'case "resume"',
        "approval.accepted",
        "approval.superseded",
        "session.resumed",
    ]:
        if required not in dispatch:
            violations.append("missing ACP-4 dispatch/result invariant: " + required)
    for required in [
        'case "approval"',
        'case "resume"',
        'workspaceState != "IN_USE"',
        'environmentState != "ACTIVE"',
        "SessionControlProvider",
    ]:
        if required not in worker:
            violations.append("missing ACP-4 Worker invariant: " + required)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-approval-resume.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "durable_approval_versions": True,
            "approval_expiry": True,
            "approval_decision_lock": True,
            "approval_unknown_superseded": True,
            "explicit_session_ref": True,
            "resume_workspace_environment_compatibility": True,
            "worker_at_most_once": True,
            "runtime_capability_fail_closed": True,
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
        sys.stderr.write("ACP-4 violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
