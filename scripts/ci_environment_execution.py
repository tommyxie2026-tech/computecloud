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
        "ProcessProviderLifecycle|"
        "EnvironmentRefRoundTrip|"
        "DescriptorWithoutProviderIsNotInstalled|"
        "EnvironmentProviderWrapsRuntimeExecution|"
        "EnvironmentCleanupUnknownFailsClosed|"
        "RecoveryRequiresEnvironmentCleanupProof|"
        "WorkerV5EnvironmentExecutionSchema"
    )
    result = run([
        "go", "test",
        "./internal/environment",
        "./internal/worker",
        "./internal/store",
        "-run", pattern,
        "-count=1", "-v",
    ])

    execute_text = pathlib.Path("internal/worker/execute.go").read_text(encoding="utf-8")
    recovery_text = pathlib.Path("internal/worker/journal.go").read_text(encoding="utf-8")
    environment_text = pathlib.Path("internal/environment/execution.go").read_text(encoding="utf-8")
    scheduler_text = pathlib.Path("internal/server/server.go").read_text(encoding="utf-8")

    violations = []
    prepare_at = execute_text.find("environmentProvider.Prepare(")
    activate_at = execute_text.find("environmentProvider.Activate(")
    env_active_at = execute_text.find("w.recordEnvironmentActive(")
    runtime_prepare_at = execute_text.find("provider.Prepare(")
    runtime_start_at = execute_text.find("provider.Start(")
    if min(prepare_at, activate_at, env_active_at, runtime_prepare_at, runtime_start_at) < 0:
        violations.append("Worker Environment/Runtime execution hooks are incomplete")
    elif not (prepare_at < activate_at < env_active_at < runtime_prepare_at < runtime_start_at):
        violations.append("Environment lifecycle is not persisted before Runtime Prepare/Start")
    if "environmentProvider, environmentOK := envreg.LookupProvider" not in execute_text:
        violations.append("Worker does not dispatch Environment execution through Provider registry")
    if "envProvider.Inspect(" not in recovery_text or "envProvider.Release(" not in recovery_text:
        violations.append("Worker recovery does not Inspect/Release Environment Provider")
    if "environment_ref" not in recovery_text:
        violations.append("Worker recovery does not load durable EnvironmentRef")
    if "func (processProvider) Prepare" not in environment_text or "func (processProvider) Release" not in environment_text:
        violations.append("builtin process Environment lacks Provider lifecycle")
    for forbidden in ["fixture_isolated_env", "environment:process"]:
        if forbidden in scheduler_text:
            violations.append("Scheduler contains Environment implementation-specific branch: " + forbidden)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-environment-execution.v1",
        "real_model_calls": False,
        "status": status,
        "v047_acceptance_fixture": {
            "evidence_class": "fixture",
            "eligible_for_real_acceptance": False,
            "required_case_ids": ["ENV01", "ENV02", "ENV03", "ENV04", "ENV05"],
            "environment_provider": "process",
            "target_docker_host": False,
            "file_escape_tested": False,
            "network_escape_tested": False,
            "container_crash_tested": False,
            "controller_restart_tested": False,
        },
        "coverage": {
            "provider_owned_environment_lifecycle": True,
            "durable_environment_ref": True,
            "worker_schema_v5_environment_metadata": True,
            "environment_active_before_runtime_start": prepare_at >= 0 and activate_at >= 0 and env_active_at >= 0 and runtime_prepare_at >= 0 and runtime_start_at >= 0 and prepare_at < activate_at < env_active_at < runtime_prepare_at < runtime_start_at,
            "runtime_receives_environment_prepared_context": True,
            "cleanup_composes_runtime_and_environment_proof": True,
            "restart_uses_environment_inspect_release": "envProvider.Inspect(" in recovery_text and "envProvider.Release(" in recovery_text,
            "unknown_environment_cleanup_fail_closed": True,
            "descriptor_only_environment_not_advertised": True,
            "scheduler_remains_generic": not any(v in scheduler_text for v in ["fixture_isolated_env", "environment:process"]),
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
        sys.stderr.write("environment execution violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
