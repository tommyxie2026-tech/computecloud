#!/usr/bin/env python3
"""Fail-closed validator for the v0.4.7 independent-host production report."""

import argparse
import json
import pathlib
import re
import sys


SCHEMA_VERSION = "computecloud.v0.4.7.production-acceptance.v1"
REQUIRED_CASES = tuple(f"VAL{i:02d}" for i in range(1, 13))
CORRECTNESS_COUNTERS = (
    "duplicate_attempts",
    "stale_generation_accepts",
    "invalid_artifacts",
    "lost_confirmed_events",
    "terminal_state_regressions",
    "unauthorized_accesses",
)
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
HASH_RE = re.compile(r"^[0-9a-f]{64}$")
SENSITIVE_KEY_RE = re.compile(
    r"(?:^|_)(?:api_?key|secret|password|authorization|bearer|credential(?:s|_ref)?|access_token|refresh_token)(?:$|_)",
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


CASE_TRUE_FIELDS = {
    "VAL01": ("preflight_verified", "independent_workers"),
    "VAL02": ("real_codex_call", "native_final_verified"),
    "VAL03": ("real_claude_call", "native_final_verified"),
    "VAL04": ("real_mcp_call", "mcp_result_verified"),
    "VAL05": ("cross_worker_execution",),
    "VAL06": ("worker_sigkill", "reconciled", "no_duplicate_attempt"),
    "VAL07": ("server_sigkill", "reconciled", "no_state_loss"),
    "VAL08": ("network_loss", "network_recovered", "event_watermark_preserved"),
    "VAL09": ("lease_and_events_continuous",),
    "VAL10": ("artifact_access_control_verified", "artifact_hashes_verified"),
}


def validate_host(item, source_sha):
    require_keys(
        item,
        ("role", "node_id", "machine_id_sha256", "binary_sha256", "source_sha", "os", "runtime_versions", "mcp_version"),
        "host",
    )
    require(item["role"] in {"server", "worker"}, "host role is invalid")
    require(isinstance(item["node_id"], str) and item["node_id"], "host node_id is required")
    require(HASH_RE.fullmatch(item["machine_id_sha256"] or ""), "host machine_id_sha256 is invalid")
    require(HASH_RE.fullmatch(item["binary_sha256"] or ""), "host binary_sha256 is invalid")
    require(item["source_sha"] == source_sha, "host source_sha does not match candidate")
    require(item["os"] == "Linux", "all production hosts must be Linux")
    if item["role"] == "worker":
        versions = item["runtime_versions"]
        require(isinstance(versions, dict) and set(versions) == {"codex", "claude"}, "Worker Codex/Claude versions are incomplete")
        for name, version in versions.items():
            require_keys(version, ("version", "sha256"), f"worker.{name}")
            require(isinstance(version["version"], str) and version["version"], f"worker {name} version is missing")
            require(HASH_RE.fullmatch(version["sha256"] or ""), f"worker {name} binary hash is invalid")
        require(isinstance(item["mcp_version"], str) and item["mcp_version"], "Worker MCP version is missing")


def validate_case(item, source_sha, worker_ids):
    require(isinstance(item, dict) and "id" in item, "case.id is required")
    case_id = item["id"]
    require(case_id in REQUIRED_CASES, f"unexpected case id: {case_id}")
    require_keys(item, ("status", "source_sha", "task_ids", "attempt_ids", "evidence"), case_id)
    require(item["status"] == "PASSED", f"{case_id} status must be PASSED")
    require(item["source_sha"] == source_sha, f"{case_id} source_sha does not match candidate")
    require(isinstance(item["task_ids"], list) and item["task_ids"] and all(isinstance(v, str) and v for v in item["task_ids"]), f"{case_id} task_ids are required")
    require(isinstance(item["attempt_ids"], list) and item["attempt_ids"] and all(isinstance(v, str) and v for v in item["attempt_ids"]), f"{case_id} attempt_ids are required")
    evidence = item["evidence"]
    require(isinstance(evidence, dict), f"{case_id}.evidence must be an object")
    for field in CASE_TRUE_FIELDS.get(case_id, ()):
        require(evidence.get(field) is True, f"{case_id} evidence.{field} must be true")

    if case_id in {"VAL02", "VAL03"}:
        for field in ("runtime_report_sha256", "native_final_sha256"):
            require(HASH_RE.fullmatch(evidence.get(field, "")), f"{case_id} {field} is invalid")
    if case_id == "VAL04":
        for field in ("mcp_report_sha256", "mcp_result_sha256"):
            require(HASH_RE.fullmatch(evidence.get(field, "")), f"VAL04 {field} is invalid")
    if case_id == "VAL05":
        require(set(evidence.get("worker_ids", [])) == worker_ids, "VAL05 must execute across both Worker IDs")
    if case_id in {"VAL06", "VAL07", "VAL08"}:
        require_keys(item, ("fault_started_at", "recovered_at"), case_id)
        require(isinstance(item["fault_started_at"], str) and item["fault_started_at"], f"{case_id} fault_started_at is required")
        require(isinstance(item["recovered_at"], str) and item["recovered_at"], f"{case_id} recovered_at is required")
    if case_id == "VAL09":
        require(isinstance(evidence.get("duration_seconds"), int) and evidence["duration_seconds"] >= 86400, "VAL09 duration_seconds must be at least 86400")
    if case_id == "VAL10":
        require(HASH_RE.fullmatch(evidence.get("artifact_manifest_sha256", "")), "VAL10 artifact_manifest_sha256 is invalid")
    if case_id == "VAL11":
        require(isinstance(evidence.get("capacity_completed"), int) and evidence["capacity_completed"] > 0, "VAL11 capacity_completed must be positive")
        require(evidence.get("capacity_failed") == 0, "VAL11 capacity_failed must be zero")
        require(evidence.get("sqlite_integrity") == "ok", "VAL11 sqlite_integrity must be ok")
    if case_id == "VAL12":
        required = (
            "v13_snapshot_sha256", "v13_snapshot_user_version", "v13_snapshot_integrity", "upgraded_user_version",
            "migrated_integrity", "restored_snapshot_sha256", "restored_user_version",
            "restored_integrity", "rollback_method", "manual_user_version_change",
            "v13_binary_sha256", "v16_binary_sha256",
        )
        require_keys(evidence, required, "VAL12.evidence")
        for field in ("v13_snapshot_sha256", "restored_snapshot_sha256", "v13_binary_sha256", "v16_binary_sha256"):
            require(HASH_RE.fullmatch(evidence[field] or ""), f"VAL12 {field} is invalid")
        require(evidence["v13_snapshot_user_version"] == 13, "VAL12 v13_snapshot_user_version must be 13")
        require(evidence["v13_snapshot_integrity"] == "ok", "VAL12 v13_snapshot_integrity must be ok")
        require(evidence["upgraded_user_version"] == 16, "VAL12 upgraded_user_version must be 16")
        require(evidence["migrated_integrity"] == "ok", "VAL12 migrated_integrity must be ok")
        require(evidence["restored_snapshot_sha256"] == evidence["v13_snapshot_sha256"], "VAL12 restored_snapshot must match the v13 snapshot")
        require(evidence["restored_user_version"] == 13, "VAL12 restored_user_version must be 13")
        require(evidence["restored_integrity"] == "ok", "VAL12 restored_integrity must be ok")
        require(evidence["rollback_method"] == "restore_snapshot", "VAL12 rollback_method must restore the snapshot")
        require(evidence["manual_user_version_change"] is False, "VAL12 manual_user_version_change must be false")


def validate_report(report):
    scan_sensitive(report)
    require_keys(report, ("schema_version", "status", "candidate", "execution", "hosts", "correctness", "cases"), "report")
    require(report["schema_version"] == SCHEMA_VERSION, "schema_version is unsupported")
    require(report["status"] == "PASSED", "report status must be PASSED")
    candidate = report["candidate"]
    require_keys(candidate, ("source_sha", "built_from_sha"), "candidate")
    source_sha = candidate["source_sha"]
    require(SHA_RE.fullmatch(source_sha or ""), "candidate.source_sha is invalid")
    require(candidate["built_from_sha"] == source_sha, "candidate.built_from_sha does not match source_sha")
    execution = report["execution"]
    require_keys(execution, ("environment", "started_at", "completed_at"), "execution")
    require(execution["environment"] == "real", "execution.environment must be real")

    hosts = report["hosts"]
    require(isinstance(hosts, list) and len(hosts) == 3, "production report requires one Server and exactly two Workers")
    for item in hosts:
        validate_host(item, source_sha)
    require(sum(item["role"] == "server" for item in hosts) == 1 and sum(item["role"] == "worker" for item in hosts) == 2, "production report requires one Server and exactly two Workers")
    require(len({item["node_id"] for item in hosts}) == 3, "host node IDs must be unique")
    workers = [item for item in hosts if item["role"] == "worker"]
    require(len({item["machine_id_sha256"] for item in workers}) == 2, "Worker machine IDs must be independent")
    worker_ids = {item["node_id"] for item in workers}

    correctness = report["correctness"]
    require_keys(correctness, CORRECTNESS_COUNTERS, "correctness")
    for field in CORRECTNESS_COUNTERS:
        require(correctness[field] == 0, f"correctness.{field} must be zero")

    cases = report["cases"]
    require(isinstance(cases, list), "cases must be an array")
    ids = [item.get("id") if isinstance(item, dict) else None for item in cases]
    duplicates = sorted(case_id for case_id in set(ids) if ids.count(case_id) > 1)
    require(not duplicates, f"duplicate case ids: {duplicates}")
    missing = sorted(set(REQUIRED_CASES) - set(ids))
    extra = sorted(set(ids) - set(REQUIRED_CASES), key=str)
    require(not missing, f"missing required cases: {missing}")
    require(not extra, f"unexpected cases: {extra}")
    for item in cases:
        validate_case(item, source_sha, worker_ids)


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
