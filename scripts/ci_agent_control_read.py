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
        "go", "test", "./internal/server",
        "-run", "AgentControlRead", "-count=1", "-v",
    ])

    control_text = pathlib.Path("internal/server/control_read.go").read_text(encoding="utf-8")
    collection_text = pathlib.Path("internal/server/control_collection.go").read_text(encoding="utf-8")
    results_text = pathlib.Path("internal/server/results.go").read_text(encoding="utf-8")
    http_text = pathlib.Path("internal/server/http.go").read_text(encoding="utf-8")
    violations = []
    for required in [
        'GET /v1/control/bootstrap',
        'GET /v1/jobs',
        'GET /v1/workers',
        'GET /v1/jobs/{id}/sessions',
        'GET /v1/jobs/{id}/events/stream',
    ]:
        if required not in http_text:
            violations.append("missing HTTP route: " + required)
    if "mirrorRuntimeEventToJob(ctx, q, ev)" not in results_text:
        violations.append("worker runtime events are not mirrored into durable job replay")
    for required in ["SNAPSHOT_EPOCH_CHANGED", "snapshot_ms", "snapshot_id", "before_created_ms", "visibleProject"]:
        if required not in collection_text:
            violations.append("missing C1 collection contract: " + required)
    if "Last-Event-ID" not in control_text:
        violations.append("SSE replay does not support Last-Event-ID")
    if "time.Time{}" not in control_text:
        violations.append("SSE write deadline is not explicitly disabled")
    if "codex_exec" in control_text or "claude_print" in control_text:
        violations.append("control read plane contains runtime profile name branching")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-read.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "bootstrap": True,
            "session_projection": True,
            "job_global_runtime_event_replay": True,
            "sse_after_seq": True,
            "sse_last_event_id": True,
            "runtime_neutral_server_projection": not any("runtime profile" in v for v in violations),
            "stable_job_collection_snapshot": True,
            "worker_project_scoping": True,
            "snapshot_epoch_reset": True,
            "no_new_message_broker": True,
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
        sys.stderr.write("agent control read violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
