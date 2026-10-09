#!/usr/bin/env python3
"""Fail-closed validator for the v0.4.7 real Runtime acceptance report."""

import argparse
import json
import math
import pathlib
import re
import sys


SCHEMA_VERSION = "computecloud.v0.4.7.runtime-acceptance.v1"
REQUIRED_CASES = tuple(f"RT{i:02d}" for i in range(1, 10))
EXPECTED_VERSIONS = {"codex_http": "0.160.1", "claude_http": "2.1.292"}
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
SENSITIVE_KEY_RE = re.compile(
    r"(?:^|_)(?:api_?key|secret|password|authorization|bearer|credential(?:s|_ref)?|access_token|refresh_token)(?:$|_)",
    re.IGNORECASE,
)
SENSITIVE_VALUE_RE = re.compile(
    r"(?:\bBearer\s+[A-Za-z0-9._~+/=-]{12,}|\b(?:sk|ghp|github_pat|xox[baprs])[-_][A-Za-z0-9_-]{12,})",
    re.IGNORECASE,
)


class ValidationError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise ValidationError(message)


def require_keys(value, keys, path):
    require(isinstance(value, dict), f"{path} must be an object")
    for key in keys:
        require(key in value, f"{path}.{key} is required")


def scan_sensitive(value, path="$"):
    if isinstance(value, dict):
        for key, child in value.items():
            require(not SENSITIVE_KEY_RE.search(key), f"credential-shaped field is forbidden: {path}.{key}")
            scan_sensitive(child, f"{path}.{key}")
    elif isinstance(value, list):
        for index, child in enumerate(value):
            scan_sensitive(child, f"{path}[{index}]")
    elif isinstance(value, str):
        require(not SENSITIVE_VALUE_RE.search(value), f"credential-shaped value is forbidden at {path}")


def validate_runtime(runtime):
    require_keys(
        runtime,
        ("profile", "version", "image_digest", "deployed_image_digest", "real_provider"),
        "runtime",
    )
    profile = runtime["profile"]
    require(profile in EXPECTED_VERSIONS, f"unexpected runtime profile: {profile}")
    require(runtime["version"] == EXPECTED_VERSIONS[profile], f"{profile} version is not pinned")
    require(DIGEST_RE.fullmatch(runtime["image_digest"] or ""), f"{profile} image_digest is invalid")
    require(
        runtime["deployed_image_digest"] == runtime["image_digest"],
        f"{profile} deployed_image_digest does not match image_digest",
    )
    require(runtime["real_provider"] is True, f"{profile} real_provider must be true")


CASE_EVIDENCE = {
    "RT01": ("start_created", "inspect_observed", "stop_confirmed"),
    "RT02": ("cancellation_requested", "terminal_cancelled"),
    "RT03": ("timeout_configured", "terminal_timed_out"),
    "RT04": ("provider_error_observed", "terminal_provider_error"),
    "RT05": ("event_continuity_verified",),
    "RT06": ("artifact_provenance_verified",),
    "RT07": ("service_restarted", "no_replay", "cleanup_confirmed"),
    "RT08": ("budget_exhausted", "terminal_budget_exhausted"),
    "RT09": ("finite_goal_budget", "one_in_flight", "usage_settled_before_next", "exhausted_blocks_next"),
}


def validate_case(item, source_sha, digests):
    require_keys(
        item,
        (
            "id",
            "status",
            "profile",
            "task_id",
            "attempt_id",
            "source_sha",
            "image_digest",
            "native_final",
            "events",
            "usage",
            "evidence",
        ),
        "case",
    )
    case_id = item["id"]
    require(case_id in REQUIRED_CASES, f"unexpected case id: {case_id}")
    require(item["status"] == "PASSED", f"{case_id} status must be PASSED")
    require(item["profile"] in digests, f"{case_id} references an unknown runtime profile")
    require(isinstance(item["task_id"], str) and item["task_id"], f"{case_id} task_id is required")
    require(isinstance(item["attempt_id"], str) and item["attempt_id"], f"{case_id} attempt_id is required")
    require(item["source_sha"] == source_sha, f"{case_id} source_sha does not match candidate")
    require(item["image_digest"] == digests[item["profile"]], f"{case_id} image_digest does not match pinned runtime")

    final = item["native_final"]
    require_keys(final, ("present", "sha256", "terminal_state"), f"{case_id}.native_final")
    require(final["present"] is True, f"{case_id} native_final must be present")
    require(DIGEST_RE.fullmatch(final["sha256"] or ""), f"{case_id} native_final sha256 is invalid")
    require(isinstance(final["terminal_state"], str) and final["terminal_state"], f"{case_id} terminal_state is required")

    events = item["events"]
    require_keys(events, ("first_sequence", "last_sequence", "contiguous"), f"{case_id}.events")
    require(events["contiguous"] is True, f"{case_id} event sequence is not contiguous")
    require(
        isinstance(events["first_sequence"], int)
        and isinstance(events["last_sequence"], int)
        and events["first_sequence"] >= 0
        and events["last_sequence"] >= events["first_sequence"],
        f"{case_id} event sequence bounds are invalid",
    )

    usage = item["usage"]
    require_keys(usage, ("complete", "input_tokens", "output_tokens", "total_tokens", "cost_usd"), f"{case_id}.usage")
    require(usage["complete"] is True, f"{case_id} usage must be complete")
    for name in ("input_tokens", "output_tokens", "total_tokens"):
        require(isinstance(usage[name], int) and not isinstance(usage[name], bool) and usage[name] >= 0, f"{case_id} usage.{name} is invalid")
    require(usage["total_tokens"] == usage["input_tokens"] + usage["output_tokens"], f"{case_id} usage token total is inconsistent")
    require(
        isinstance(usage["cost_usd"], (int, float))
        and not isinstance(usage["cost_usd"], bool)
        and math.isfinite(usage["cost_usd"])
        and usage["cost_usd"] >= 0,
        f"{case_id} usage.cost_usd is invalid",
    )

    evidence = item["evidence"]
    require(isinstance(evidence, dict), f"{case_id} evidence must be an object")
    for name in CASE_EVIDENCE[case_id]:
        require(evidence.get(name) is True, f"{case_id} evidence.{name} must be true")
    if case_id == "RT08":
        require(item["profile"] == "claude_http", "RT08 must use claude_http")
        require(
            evidence.get("native_budget_capability") == "budget_claude_estimated_usd_v1",
            "RT08 native budget capability is missing",
        )
    if case_id == "RT09":
        require(item["profile"] == "claude_http", "RT09 must use claude_http")


def validate_report(report):
    scan_sensitive(report)
    require_keys(report, ("schema_version", "status", "candidate", "execution", "runtimes", "cases"), "report")
    require(report["schema_version"] == SCHEMA_VERSION, "schema_version is unsupported")
    require(report["status"] == "PASSED", "report status must be PASSED")

    candidate = report["candidate"]
    require_keys(candidate, ("source_sha", "built_from_sha"), "candidate")
    source_sha = candidate["source_sha"]
    require(SHA_RE.fullmatch(source_sha or ""), "candidate.source_sha is invalid")
    require(candidate["built_from_sha"] == source_sha, "candidate.built_from_sha does not match source_sha")

    execution = report["execution"]
    require_keys(
        execution,
        ("environment", "real_model_calls", "authorized_account", "service_endpoint_scheme", "started_at", "completed_at"),
        "execution",
    )
    require(execution["environment"] == "real", "execution.environment must be real")
    require(execution["real_model_calls"] is True, "execution.real_model_calls must be true")
    require(execution["authorized_account"] is True, "execution.authorized_account must be true")
    require(execution["service_endpoint_scheme"] == "unix", "service endpoint must use unix")
    require(isinstance(execution["started_at"], str) and execution["started_at"], "execution.started_at is required")
    require(isinstance(execution["completed_at"], str) and execution["completed_at"], "execution.completed_at is required")

    runtimes = report["runtimes"]
    require(isinstance(runtimes, list) and len(runtimes) == len(EXPECTED_VERSIONS), "exactly two runtimes are required")
    profiles = [runtime.get("profile") if isinstance(runtime, dict) else None for runtime in runtimes]
    require(len(set(profiles)) == len(profiles), "runtime profiles must be unique")
    require(set(profiles) == set(EXPECTED_VERSIONS), "codex_http and claude_http runtimes are required")
    for runtime in runtimes:
        validate_runtime(runtime)
    digests = {runtime["profile"]: runtime["image_digest"] for runtime in runtimes}

    cases = report["cases"]
    require(isinstance(cases, list), "cases must be an array")
    ids = [item.get("id") if isinstance(item, dict) else None for item in cases]
    duplicates = sorted(case_id for case_id in set(ids) if ids.count(case_id) > 1)
    require(not duplicates, f"duplicate case ids: {duplicates}")
    missing = sorted(set(REQUIRED_CASES) - set(ids))
    extra = sorted(set(ids) - set(REQUIRED_CASES), key=str)
    require(not missing, f"missing required cases: {missing}")
    require(not extra, f"unexpected cases: {extra}")
    task_ids = [item.get("task_id") for item in cases]
    attempt_ids = [item.get("attempt_id") for item in cases]
    require(len(set(task_ids)) == len(task_ids), "case task_id values must be unique")
    require(len(set(attempt_ids)) == len(attempt_ids), "case attempt_id values must be unique")
    require("codex_http" in {item.get("profile") for item in cases}, "at least one real codex_http case is required")
    for item in cases:
        validate_case(item, source_sha, digests)


def reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValidationError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=pathlib.Path)
    args = parser.parse_args(argv)
    try:
        with args.report.open(encoding="utf-8") as handle:
            report = json.load(handle, object_pairs_hook=reject_duplicate_keys)
        validate_report(report)
    except (OSError, json.JSONDecodeError, ValidationError) as exc:
        print(json.dumps({"status": "FAILED", "error": str(exc)}, sort_keys=True))
        return 1
    print(json.dumps({"status": "PASSED", "report": str(args.report)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
