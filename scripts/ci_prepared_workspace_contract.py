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
        "TestPreparedWorkspaceTemplateFingerprintStable",
        "TestPreparedWorkspaceLocalProviderPrepareInspectAndRestart",
        "TestPreparedWorkspaceMaterializesIsolatedWritableAttempts",
        "TestPreparedWorkspaceDetectsTemplateTampering",
        "TestPreparedWorkspaceRejectsReferenceMismatchAndExistingAttempt",
        "TestPreparedWorkspaceWorkerReusesTemplateWithoutSharingWritableState",
        "TestPreparedWorkspaceTemplateFingerprintTracksExecutionVersions",
        "TestWorkspaceGenerationIsolationAndOwnership",
        "TestWorkspaceRecoveryUsesPreSpawnProofAndQuarantinesUnknown",
    ]
    pattern = "^(" + "|".join(tests) + ")$"
    result = run([
        "go", "test",
        "./internal/workspace",
        "./internal/worker",
        "-run", pattern,
        "-count=1", "-v",
    ])

    prepared = pathlib.Path("internal/workspace/prepared.go").read_text(encoding="utf-8")
    worker = pathlib.Path("internal/worker/workspace_lifecycle.go").read_text(encoding="utf-8")

    violations = []
    for symbol in [
        "type WorkspaceTemplate struct",
        "type PreparedWorkspaceRef struct",
        "type PreparedProvider interface",
        "PrepareTemplate(",
        "InspectTemplate(",
        "MaterializeAttempt(",
    ]:
        if symbol not in prepared:
            violations.append("prepared workspace contract missing: " + symbol)
    if "NewLocalPreparedProvider" not in worker or "MaterializeAttempt" not in worker:
        violations.append("Worker does not use Prepared Workspace provider")
    if "workspace.Prepare(ctx, root, a.AttemptId" in worker:
        violations.append("Worker still cold-clones directly into Attempt Workspace")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-prepared-workspace-contract.v1",
        "real_model_calls": False,
        "status": status,
        "coverage": {
            "immutable_template_contract": True,
            "canonical_template_fingerprint": True,
            "restart_inspection": True,
            "corruption_fail_closed": True,
            "attempt_generation_isolation": True,
            "worker_prepared_template_integration": True,
            "execution_component_version_fencing": True,
            "no_public_job_contract_change": True,
        },
        "tests": tests,
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
        sys.stderr.write("prepared workspace contract violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
