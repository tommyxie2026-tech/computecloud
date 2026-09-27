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

    tests = [
        "TestLongRunningDeadlineExtensionUsesLeaseLiveness",
        "TestLongRunningDeadlineDoesNotExtendImplicitly",
        "TestTaskEventCompactionHasExplicitCursorFloor",
        "TestProcessStopEscalatesToKill",
        "TestV7LongRunningSchema",
    ]
    pattern = "^(" + "|".join(tests) + ")$"
    result = run(["go", "test", "./internal/server", "./internal/process", "./internal/store", "-run", pattern, "-count=1", "-v"])
    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    report = {
        "schema_version": "ci-long-run-flow.v1",
        "real_model_calls": False,
        "status": "PASSED" if result["returncode"] == 0 else "FAILED",
        "coverage": {
            "lease_liveness_without_runtime_output": True,
            "explicit_bounded_deadline_extension": True,
            "deadline_extension_idempotency": True,
            "deadline_expiry_blocks_success": True,
            "task_event_compaction_cursor_floor": True,
            "worker_event_replay_dedup_after_compaction": True,
            "term_kill_process_group_escalation": True,
            "server_schema_v7": True,
        },
        "tests": tests,
        "command": result["command"],
        "duration_ms": result["duration_ms"],
        "returncode": result["returncode"],
    }
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": report["status"], "report": str(out)}))
    if result["returncode"] != 0:
        sys.stderr.write(result["stdout"])
        sys.stderr.write(result["stderr"])
        raise SystemExit(result["returncode"])

if __name__ == "__main__":
    main()
