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
        "BuiltinsAndInstalledCompatible|"
        "RegistryValidationAndFixtureExtension|"
        "AuthorizeRequired|"
        "ExecutionTools|"
        "AllowedToolsAffectTemplateDigest|"
        "WorkerPolicyToolNameValidation|"
        "WorkerAdvertisesOnlyInstalledCompatibleTools|"
        "RegisteredFixtureToolCanBeAdvertisedWithoutSchedulerChange|"
        "WorkerRejectsDeniedToolBeforeRuntimeStart"
    )
    result = run([
        "go", "test",
        "./internal/tool",
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
    if "toolreg.InstalledCompatible" not in worker_probe:
        violations.append("Worker capability advertisement bypasses Tool registry")
    auth_at = worker_execute.find("toolreg.AuthorizeRequired(")
    prepare_at = worker_execute.find("provider.Prepare(")
    start_at = worker_execute.find("provider.Start(")
    if auth_at < 0 or prepare_at < 0 or start_at < 0 or not (auth_at < prepare_at < start_at):
        violations.append("Tool policy gate is not before Runtime Prepare/Start")
    if '"tool:"+name' not in spec:
        violations.append("Job tools are not frozen into namespaced Task requirements")
    if "t.Spec.RequiredCapabilities" not in scheduler:
        violations.append("Scheduler no longer uses generic capability matching")
    if "job_io_v1" in scheduler or "artifact_inputs_v1" in scheduler:
        violations.append("Scheduler contains Tool-specific matching logic")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-tool-contract.v1",
        "real_model_calls": False,
        "status": status,
        "coverage": {
            "independent_tool_registry": True,
            "builtin_tool_descriptors": True,
            "installed_runtime_compatible_intersection": True,
            "namespaced_job_tool_requirements": True,
            "policy_digest_binding": True,
            "policy_gate_before_runtime_start": auth_at >= 0 and prepare_at >= 0 and start_at >= 0 and auth_at < prepare_at < start_at,
            "unregistered_tool_not_advertised": True,
            "scheduler_remains_generic": not ("job_io_v1" in scheduler or "artifact_inputs_v1" in scheduler),
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
        sys.stderr.write("tool contract violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
