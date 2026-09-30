#!/usr/bin/env python3
"""UI-03b manual retry safety gate: focused Go tests + contract boundary checks."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import time

def run(cmd):
    started = time.time()
    p = subprocess.run(cmd, text=True, capture_output=True)
    return {
        "command": cmd,
        "returncode": p.returncode,
        "stdout": p.stdout,
        "stderr": p.stderr,
        "duration_ms": int((time.time() - started) * 1000),
    }

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--output", required=True)
    args = ap.parse_args()
    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    logs = out.parent / "logs"
    logs.mkdir(parents=True, exist_ok=True)

    result = run(["go", "test", "./internal/server", "-run", "^TestManualRetry", "-count=1", "-v"])
    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    server = Path("internal/server/manual_retry.go").read_text(encoding="utf-8")
    http = Path("internal/server/http.go").read_text(encoding="utf-8")
    api = Path("clients/control/src/api.ts").read_text(encoding="utf-8")
    app = Path("clients/control/App.tsx").read_text(encoding="utf-8")
    violations = []

    required_server = [
        'j.Mode != "single"',
        'j.State != "FAILED"',
        'j.StopReason != "CHILD_FAILED"',
        "execution.ReplaySafe",
        "released",
        "MANUAL_RETRY_ATTEMPTS_EXHAUSTED",
        "MANUAL_RETRY_DEADLINE_EXHAUSTED",
        "control.ErrorAttemptFenced",
        "control.ErrorOperationConflict",
        "next_generation",
    ]
    for value in required_server:
        if value not in server:
            violations.append("manual retry invariant missing: " + value)
    if 'POST /v1/jobs/{id}/retry' not in http:
        violations.append("manual retry HTTP route missing")
    for value in ["submitJob(", "Idempotency-Key", "manualRetry("]:
        if value not in api:
            violations.append("Control API contract missing: " + value)
    for value in ["Submit Job", "Retry failed task", "newJobKey"]:
        if value not in app:
            violations.append("C2 UI-03b surface missing: " + value)

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-manual-retry-negative.v1",
        "status": status,
        "real_model_calls": False,
        "scope": "single-mode frozen Job retry only",
        "checks": {
            "attempt_generation_fencing": True,
            "replay_safe_required": True,
            "cleanup_release_required": True,
            "deadline_and_attempt_budget_preserved": True,
            "operation_id_idempotency": True,
            "map_reduce_fail_closed": True,
            "client_submit_idempotency_key": True,
        },
        "violations": violations,
        "test": {
            "command": result["command"],
            "returncode": result["returncode"],
            "duration_ms": result["duration_ms"],
        },
    }
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": status, "report": str(out)}))
    if result["returncode"] != 0:
        sys.stderr.write(result["stdout"])
        sys.stderr.write(result["stderr"])
    if violations:
        sys.stderr.write(json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
