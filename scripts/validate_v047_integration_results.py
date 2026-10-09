#!/usr/bin/env python3
"""Fail-closed validator for v0.4.7 Container and CI integration evidence."""

import argparse
import json
import pathlib
import re
import sys


SCHEMA_VERSION = "computecloud.v0.4.7.integration-acceptance.v1"
REQUIRED_CASES = tuple([f"ENV{i:02d}" for i in range(1, 6)] + [f"INT{i:02d}" for i in range(1, 5)])
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
HASH_RE = re.compile(r"^[0-9a-f]{64}$")
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


CASE_TRUE_FIELDS = {
    "ENV01": ("container_created",),
    "ENV02": ("file_escape_attempted", "file_escape_blocked"),
    "ENV03": ("network_escape_attempted", "network_escape_blocked"),
    "ENV04": ("container_crashed", "controller_restarted", "no_replay"),
    "ENV05": (
        "release_requested",
        "runtime_cleanup_confirmed",
        "environment_cleanup_confirmed",
        "residual_container_absent",
    ),
    "INT01": ("authenticated_service_identity", "trigger_accepted"),
    "INT02": ("duplicate_delivery_attempted", "same_submission_key", "same_job_id"),
    "INT03": ("failed_job_observed", "failed_delivery_recorded", "bounded_error_code"),
    "INT04": ("retry_attempted", "retry_bounded", "source_provenance_verified", "artifact_provenance_verified"),
}


def validate_case(item, source_sha, image_digest):
    require(isinstance(item, dict), "case must be an object")
    require("id" in item, "case.id is required")
    case_id = item["id"]
    require(case_id in REQUIRED_CASES, f"unexpected case id: {case_id}")
    require_keys(item, ("status", "source_sha", "evidence"), case_id)
    require(item["status"] == "PASSED", f"{case_id} status must be PASSED")
    require(item["source_sha"] == source_sha, f"{case_id} source_sha does not match candidate")
    evidence = item["evidence"]
    require(isinstance(evidence, dict), f"{case_id}.evidence must be an object")
    for field in CASE_TRUE_FIELDS[case_id]:
        require(evidence.get(field) is True, f"{case_id} evidence.{field} must be true")

    if case_id.startswith("ENV"):
        require_keys(item, ("attempt_id", "image_digest"), case_id)
        require(isinstance(item["attempt_id"], str) and item["attempt_id"], f"{case_id} attempt_id is required")
        require(item["image_digest"] == image_digest, f"{case_id} image_digest does not match deployed container")
        if case_id == "ENV01":
            require(evidence.get("environment_provider") == "container", "ENV01 must use the container provider")
            require(evidence.get("host_cli_execution") is False, "ENV01 host CLI execution is forbidden")
    else:
        require_keys(
            item,
            (
                "delivery_id",
                "job_id",
                "submission_key_sha256",
                "spec_sha256",
            ),
            case_id,
        )
        require(isinstance(item["delivery_id"], str) and item["delivery_id"], f"{case_id} delivery_id is required")
        require(isinstance(item["job_id"], str) and item["job_id"], f"{case_id} job_id is required")
        for field in ("submission_key_sha256", "spec_sha256"):
            require(HASH_RE.fullmatch(item[field] or ""), f"{case_id} {field} is invalid")
        if case_id == "INT02":
            require(evidence.get("side_effect_count") == 1, "INT02 evidence.side_effect_count must equal 1")
        if case_id == "INT03":
            require_keys(item, ("result_sha256",), case_id)
            require(HASH_RE.fullmatch(item["result_sha256"] or ""), "INT03 result_sha256 is invalid")
        if case_id == "INT04":
            require_keys(item, ("result_sha256", "manifest_sha256"), case_id)
            require(HASH_RE.fullmatch(item["result_sha256"] or ""), "INT04 result_sha256 is invalid")
            require(HASH_RE.fullmatch(item["manifest_sha256"] or ""), "INT04 manifest_sha256 is invalid")


def validate_report(report):
    scan_sensitive(report)
    require_keys(report, ("schema_version", "status", "candidate", "execution", "container", "cases"), "report")
    require(report["schema_version"] == SCHEMA_VERSION, "schema_version is unsupported")
    require(report["status"] == "PASSED", "report status must be PASSED")

    candidate = report["candidate"]
    require_keys(candidate, ("source_sha", "built_from_sha"), "candidate")
    source_sha = candidate["source_sha"]
    require(SHA_RE.fullmatch(source_sha or ""), "candidate.source_sha is invalid")
    require(candidate["built_from_sha"] == source_sha, "candidate.built_from_sha does not match source_sha")

    execution = report["execution"]
    require_keys(execution, ("environment", "target_docker_host", "trusted_ci", "started_at", "completed_at"), "execution")
    require(execution["environment"] == "real", "execution.environment must be real")
    require(execution["target_docker_host"] is True, "execution.target_docker_host must be true")
    require(execution["trusted_ci"] is True, "execution.trusted_ci must be true")
    require(isinstance(execution["started_at"], str) and execution["started_at"], "execution.started_at is required")
    require(isinstance(execution["completed_at"], str) and execution["completed_at"], "execution.completed_at is required")

    container = report["container"]
    require_keys(container, ("provider", "image_digest", "deployed_image_digest"), "container")
    require(container["provider"] == "container", "container.provider must be container")
    image_digest = container["image_digest"]
    require(DIGEST_RE.fullmatch(image_digest or ""), "container.image_digest is invalid")
    require(container["deployed_image_digest"] == image_digest, "container.deployed_image_digest does not match image_digest")

    cases = report["cases"]
    require(isinstance(cases, list), "cases must be an array")
    ids = [item.get("id") if isinstance(item, dict) else None for item in cases]
    duplicates = sorted(case_id for case_id in set(ids) if ids.count(case_id) > 1)
    require(not duplicates, f"duplicate case ids: {duplicates}")
    missing = sorted(set(REQUIRED_CASES) - set(ids))
    extra = sorted(set(ids) - set(REQUIRED_CASES), key=str)
    require(not missing, f"missing required cases: {missing}")
    require(not extra, f"unexpected cases: {extra}")
    environment_attempts = [item.get("attempt_id") for item in cases if isinstance(item, dict) and str(item.get("id", "")).startswith("ENV")]
    require(len(set(environment_attempts)) == len(environment_attempts), "ENV attempt_id values must be unique")
    for item in cases:
        validate_case(item, source_sha, image_digest)


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
