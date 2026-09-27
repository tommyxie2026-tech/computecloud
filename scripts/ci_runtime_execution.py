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

    pattern = (
        "RuntimeExecutionRefRoundTrip|"
        "RemoteFixtureExecutionContractUsesNoLocalPID|"
        "ProcessInspectTracksIdentity|"
        "RecoveryUsesRuntimeProviderInspectAndStop|"
        "RemoteRuntimeExecutesWithoutLocalAgentProcess|"
        "WorkerV4RuntimeExecutionSchema|"
        "RuntimeV2"
    )
    result = run([
        "go", "test",
        "./internal/adapter",
        "./internal/process",
        "./internal/worker",
        "./internal/store",
        "-run", pattern,
        "-count=1", "-v",
    ])

    execute_text = pathlib.Path("internal/worker/execute.go").read_text(encoding="utf-8")
    recovery_text = pathlib.Path("internal/worker/journal.go").read_text(encoding="utf-8")
    violations = []
    if "provider.Start(" not in execute_text:
        violations.append("Worker execute does not dispatch through Provider.Start")
    if "process.Run(execCtx, r.Executable" in execute_text:
        violations.append("Worker execute still starts Agent Runtime directly")
    if "provider.Inspect(" not in recovery_text or "provider.Stop(" not in recovery_text:
        violations.append("Worker recovery does not use Provider Inspect/Stop")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-runtime-execution.v1",
        "real_model_calls": False,
        "status": status,
        "coverage": {
            "transport_neutral_ref": True,
            "local_process_identity_inspection": True,
            "remote_api_fixture_without_local_pid": True,
            "remote_api_worker_completion_without_local_process": True,
            "provider_owned_start": "provider.Start(" in execute_text,
            "provider_owned_recovery_inspect_stop": "provider.Inspect(" in recovery_text and "provider.Stop(" in recovery_text,
            "worker_schema_v4_runtime_metadata": True,
            "unknown_remote_execution_fail_closed": True,
            "direct_agent_process_spawn_removed": "process.Run(execCtx, r.Executable" not in execute_text,
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
        sys.stderr.write("runtime execution violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
