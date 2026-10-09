#!/usr/bin/env python3
"""Aggregate v0.4.7 real evidence and exact-SHA CI metadata."""

import argparse
import hashlib
import json
import pathlib
import re
import sys

try:
    from scripts.validate_v047_integration_results import validate_report as validate_integration
    from scripts.validate_v047_performance_results import validate_report as validate_performance
    from scripts.validate_v047_production_results import validate_report as validate_production
    from scripts.validate_v047_runtime_results import validate_report as validate_runtime
except ModuleNotFoundError:  # Direct execution adds scripts/, not the repository root.
    from validate_v047_integration_results import validate_report as validate_integration
    from validate_v047_performance_results import validate_report as validate_performance
    from validate_v047_production_results import validate_report as validate_production
    from validate_v047_runtime_results import validate_report as validate_runtime


SHA_RE = re.compile(r"^[0-9a-f]{40}$")
REPORT_VALIDATORS = {
    "runtime": validate_runtime,
    "integration": validate_integration,
    "production": validate_production,
    "performance": validate_performance,
}
REQUIRED_CI_JOBS = (
    "relay-fixture",
    "v047-stable-gate-contract",
    "v047-release-transition",
    "transport-seam",
    "control-review",
    "goal-governance",
    "goal-execution",
    "scheduler-readiness",
    "prepared-workspace-cache",
    "verify-macos",
    "verify",
    "task-flow",
    "retry-flow",
    "manual-retry-negative",
    "artifact-flow",
    "workspace-flow",
    "prepared-workspace-contract",
    "prepared-workspace-recovery",
    "long-run-flow",
    "fair-flow",
    "runtime-contract-flow",
    "runtime-execution-flow",
    "tool-contract-flow",
    "environment-contract-flow",
    "environment-execution-flow",
    "agent-control-schema",
    "agent-control-read",
    "runtime-adapter-contract",
    "runtime-budget",
    "agent-control-fencing",
    "agent-control-negative",
    "agent-control-dispatch",
    "agent-control-approval",
    "agent-control-resume",
    "control-client-check",
    "control-client-e2e",
    "control-mobile-check",
    "container-image",
)


class GateError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise GateError(message)


def canonical_hash(value):
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def validate_ci(candidate_sha, ci):
    require(isinstance(ci, dict), "CI evidence must be an object")
    require(ci.get("schema_version") == "computecloud.v0.4.7.ci-evidence.v1", "CI schema_version is unsupported")
    require(ci.get("source_sha") == candidate_sha, "CI source_sha does not match candidate")
    require(ci.get("event") == "push", "CI event must be push on the candidate commit")
    require(ci.get("status") == "completed", "CI status must be completed")
    require(ci.get("conclusion") == "success", "CI conclusion must be success")
    require(isinstance(ci.get("workflow_run_id"), int) and ci["workflow_run_id"] > 0, "CI workflow_run_id is required")
    require(isinstance(ci.get("workflow_url"), str) and ci["workflow_url"].startswith("https://github.com/"), "CI workflow_url is invalid")
    jobs = ci.get("jobs")
    require(isinstance(jobs, dict), "CI jobs must be an object")
    for name in REQUIRED_CI_JOBS:
        require(jobs.get(name) == "success", f"CI job {name} must be success")
    relay = ci.get("relay_contracts")
    require(isinstance(relay, dict), "CI relay_contracts are required")
    for field in ("control_bulk_isolation", "bounded_metrics", "operations_runbook"):
        require(relay.get(field) is True, f"CI relay_contracts.{field} must be true")


def evaluate_gate(candidate_sha, reports, ci):
    require(SHA_RE.fullmatch(candidate_sha or ""), "candidate SHA must be 40 lowercase hex characters")
    require(isinstance(reports, dict), "reports must be an object")
    require(set(reports) == set(REPORT_VALIDATORS), f"reports must contain exactly {sorted(REPORT_VALIDATORS)}")
    summaries = {}
    for name, validator in REPORT_VALIDATORS.items():
        report = reports[name]
        try:
            validator(report)
        except (TypeError, ValueError) as exc:
            raise GateError(f"{name} report rejected: {exc}") from exc
        require(report.get("status") == "PASSED", f"{name} report status must be PASSED")
        require(report.get("candidate", {}).get("source_sha") == candidate_sha, f"{name} source_sha does not match candidate")
        case_values = report.get("cases")
        if case_values is None:
            case_values = list(report.get("performance_cases", [])) + list(report.get("relay_cases", []))
        require(isinstance(case_values, list) and case_values, f"{name} report has no cases")
        require(all(isinstance(item, dict) and item.get("status") == "PASSED" for item in case_values), f"{name} contains a non-PASSED case")
        summaries[name] = {
            "schema_version": report.get("schema_version"),
            "status": "PASSED",
            "case_count": len(case_values),
            "sha256": canonical_hash(report),
        }
    validate_ci(candidate_sha, ci)
    return {
        "schema_version": "computecloud.v0.4.7.stable-gate.v1",
        "version": "0.4.7",
        "status": "PASSED",
        "candidate_sha": candidate_sha,
        "reports": summaries,
        "ci": {
            "workflow_run_id": ci["workflow_run_id"],
            "workflow_url": ci["workflow_url"],
            "source_sha": ci["source_sha"],
            "conclusion": ci["conclusion"],
            "required_job_count": len(REQUIRED_CI_JOBS),
            "evidence_sha256": canonical_hash(ci),
        },
        "release_request_allowed": True,
    }


def load_json(path):
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-sha", required=True)
    parser.add_argument("--runtime", type=pathlib.Path, required=True)
    parser.add_argument("--integration", type=pathlib.Path, required=True)
    parser.add_argument("--production", type=pathlib.Path, required=True)
    parser.add_argument("--performance", type=pathlib.Path, required=True)
    parser.add_argument("--ci-evidence", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args(argv)
    try:
        reports = {name: load_json(getattr(args, name)) for name in REPORT_VALIDATORS}
        result = evaluate_gate(args.candidate_sha, reports, load_json(args.ci_evidence))
        exit_code = 0
    except (OSError, json.JSONDecodeError, GateError) as exc:
        result = {
            "schema_version": "computecloud.v0.4.7.stable-gate.v1",
            "version": "0.4.7",
            "status": "FAILED",
            "candidate_sha": args.candidate_sha,
            "error": str(exc),
            "release_request_allowed": False,
        }
        exit_code = 1
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": result["status"], "report": str(args.output)}, sort_keys=True))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
