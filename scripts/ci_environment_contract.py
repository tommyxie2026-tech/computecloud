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
        "BuiltinProcessAndInstalledCompatible|"
        "RegistryValidationAndFixtureExtension|"
        "AuthorizeRequired|"
        "ExecutionEnvironment|"
        "AllowedEnvironmentsAffectTemplateDigest|"
        "WorkerPolicyEnvironmentNameValidation|"
        "WorkerAdvertisesOnlyRegisteredCompatibleEnvironments|"
        "RegisteredFixtureEnvironmentCanBeAdvertisedWithoutSchedulerChange|"
        "WorkerRejectsDeniedEnvironmentBeforeWorkspaceOrRuntimeStart"
    )
    result = run([
        "go", "test",
        "./internal/environment",
        "./internal/job",
        "./internal/config",
        "./internal/worker",
        "-run", pattern,
        "-count=1", "-v",
    ])

    worker_probe = pathlib.Path("internal/worker/worker.go").read_text(encoding="utf-8")
    worker_execute = pathlib.Path("internal/worker/execute.go").read_text(encoding="utf-8")
    scheduler = pathlib.Path("internal/server/server.go").read_text(encoding="utf-8")
    spec = pathlib.Path("internal/job/spec.go").read_text(encoding="utf-8")

    violations = []
    if "envreg.InstalledCompatible" not in worker_probe:
        violations.append("Worker capability advertisement bypasses Environment registry")
    auth_at = worker_execute.find("envreg.AuthorizeRequired(")
    workspace_at = worker_execute.find("w.prepareWorkspace(")
    prepare_at = worker_execute.find("provider.Prepare(")
    start_at = worker_execute.find("provider.Start(")
    if auth_at < 0 or workspace_at < 0 or prepare_at < 0 or start_at < 0 or not (auth_at < workspace_at < prepare_at < start_at):
        violations.append("Environment policy gate is not before Workspace/Runtime preparation")
    if '"environment:"+e.EnvironmentName()' not in spec:
        violations.append("Job environment is not frozen into namespaced Task requirement")
    if "t.Spec.RequiredCapabilities" not in scheduler:
        violations.append("Scheduler no longer uses generic capability matching")
    for forbidden in ["environment:process", "fixture_container_environment"]:
        if forbidden in scheduler:
            violations.append("Scheduler contains Environment-specific matching logic: " + forbidden)

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-environment-contract.v1",
        "real_model_calls": False,
        "status": status,
        "coverage": {
            "independent_environment_registry": True,
            "builtin_process_descriptor": True,
            "registered_runtime_compatible_intersection": True,
            "namespaced_job_environment_requirement": True,
            "legacy_job_defaults_to_process": True,
            "policy_digest_binding": True,
            "policy_gate_before_workspace_runtime_prepare": auth_at >= 0 and workspace_at >= 0 and prepare_at >= 0 and start_at >= 0 and auth_at < workspace_at < prepare_at < start_at,
            "unregistered_environment_not_advertised": True,
            "scheduler_remains_generic": not any(v in scheduler for v in ["environment:process", "fixture_container_environment"]),
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
        sys.stderr.write("environment contract violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
