#!/usr/bin/env python3
"""Validate representative cache samples and real NAT Relay evidence."""

import argparse
import json
import math
import pathlib
import re
import statistics
import sys


SCHEMA_VERSION = "computecloud.v0.4.7.performance-acceptance.v1"
PERF_CASES = ("PERF01", "PERF02", "PERF03")
RELAY_CASES = tuple(f"RLY{i:02d}" for i in range(1, 6))
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
            require(key.lower() not in {"ticket", "relay_ticket", "ticket_value"}, f"credential-shaped field is forbidden: {path}.{key}")
            scan_sensitive(child, f"{path}.{key}")
    elif isinstance(value, list):
        for index, child in enumerate(value):
            scan_sensitive(child, f"{path}[{index}]")


def percentile95(values):
    ordered = sorted(values)
    return ordered[math.ceil(len(ordered) * 0.95) - 1]


def summarize(cold_samples, warm_samples):
    cold = [sample["full_job_ms"] for sample in cold_samples]
    warm = [sample["full_job_ms"] for sample in warm_samples]
    cold_p50 = statistics.median(cold)
    warm_p50 = statistics.median(warm)
    return {
        "cold_sample_count": len(cold),
        "warm_sample_count": len(warm),
        "cold_p50_ms": cold_p50,
        "cold_p95_ms": percentile95(cold),
        "warm_p50_ms": warm_p50,
        "warm_p95_ms": percentile95(warm),
        "warm_p50_ratio": warm_p50 / cold_p50,
    }


def validate_samples(items, field, attempt_ids):
    require(isinstance(items, list) and 10 <= len(items) <= 1000, f"{field} requires 10 to 1000 samples")
    for index, item in enumerate(items):
        path = f"{field}[{index}]"
        require_keys(item, ("attempt_id", "full_job_ms", "preparation_ms"), path)
        attempt_id = item["attempt_id"]
        require(isinstance(attempt_id, str) and attempt_id, f"{path}.attempt_id is required")
        require(attempt_id not in attempt_ids, f"duplicate Attempt ID: {attempt_id}")
        attempt_ids.add(attempt_id)
        total = item["full_job_ms"]
        preparation = item["preparation_ms"]
        require(isinstance(total, (int, float)) and not isinstance(total, bool) and math.isfinite(total) and total > 0, f"{path}.full_job_ms is invalid")
        require(isinstance(preparation, (int, float)) and not isinstance(preparation, bool) and math.isfinite(preparation) and 0 <= preparation <= total, f"{path}.preparation_ms is invalid")


def validate_performance_case(item, source_sha, attempt_ids):
    require_keys(
        item,
        ("id", "status", "source_sha", "repository_ref", "base_commit", "size_class", "load", "cold_samples", "warm_samples", "reported"),
        "performance_case",
    )
    case_id = item["id"]
    require(case_id in PERF_CASES, f"unexpected performance case: {case_id}")
    require(item["status"] == "PASSED", f"{case_id} status must be PASSED")
    require(item["source_sha"] == source_sha, f"{case_id} source_sha does not match candidate")
    require(isinstance(item["repository_ref"], str) and item["repository_ref"], f"{case_id} repository_ref is required")
    require(SHA_RE.fullmatch(item["base_commit"] or ""), f"{case_id} base_commit must be pinned")
    require(item["size_class"] in {"small", "medium", "large"}, f"{case_id} size_class is invalid")
    load = item["load"]
    require_keys(load, ("worker_slots", "concurrent_jobs", "cpu_count", "memory_mib"), f"{case_id}.load")
    for field in ("worker_slots", "concurrent_jobs", "cpu_count", "memory_mib"):
        require(isinstance(load[field], int) and not isinstance(load[field], bool) and load[field] > 0, f"{case_id}.load.{field} must be positive")

    validate_samples(item["cold_samples"], f"{case_id}.cold_samples", attempt_ids)
    validate_samples(item["warm_samples"], f"{case_id}.warm_samples", attempt_ids)
    computed = summarize(item["cold_samples"], item["warm_samples"])
    reported = item["reported"]
    require_keys(reported, tuple(computed), f"{case_id}.reported")
    for field, expected in computed.items():
        actual = reported[field]
        require(isinstance(actual, (int, float)) and not isinstance(actual, bool) and math.isfinite(actual), f"{case_id}.reported.{field} is invalid")
        require(math.isclose(actual, expected, rel_tol=1e-9, abs_tol=1e-9), f"{case_id} {field} does not match raw samples")
    require(computed["warm_p50_ratio"] <= 0.40, f"{case_id} warm_p50_ratio exceeds 0.40")


RELAY_TRUE_FIELDS = {
    "RLY01": ("direct_connected",),
    "RLY02": ("direct_failure_forced", "fallback_attempted", "relay_connected"),
    "RLY03": ("real_nat_path", "control_during_bulk", "cancel_completed"),
    "RLY04": ("disconnect_injected", "reconnected", "event_watermark_preserved"),
    "RLY05": ("relay_restarted", "fresh_ticket_acquired", "reconnected", "long_task_completed"),
}


def validate_relay_case(item, source_sha, worker_ids, attempt_ids):
    require_keys(item, ("id", "status", "source_sha", "attempt_ids", "worker_ids", "evidence"), "relay_case")
    case_id = item["id"]
    require(case_id in RELAY_CASES, f"unexpected Relay case: {case_id}")
    require(item["status"] == "PASSED", f"{case_id} status must be PASSED")
    require(item["source_sha"] == source_sha, f"{case_id} source_sha does not match candidate")
    require(set(item["worker_ids"]) == worker_ids, f"{case_id} must include both Worker IDs")
    require(isinstance(item["attempt_ids"], list) and item["attempt_ids"], f"{case_id} attempt_ids are required")
    for attempt_id in item["attempt_ids"]:
        require(isinstance(attempt_id, str) and attempt_id, f"{case_id} attempt_id is invalid")
        require(attempt_id not in attempt_ids, f"duplicate Attempt ID: {attempt_id}")
        attempt_ids.add(attempt_id)
    evidence = item["evidence"]
    require(isinstance(evidence, dict), f"{case_id}.evidence must be an object")
    for field in RELAY_TRUE_FIELDS[case_id]:
        require(evidence.get(field) is True, f"{case_id} evidence.{field} must be true")
    if case_id == "RLY01":
        require(evidence.get("relay_used") is False, "RLY01 relay_used must be false")


def exact_case_ids(items, required, label):
    require(isinstance(items, list), f"{label} must be an array")
    ids = [item.get("id") if isinstance(item, dict) else None for item in items]
    duplicates = sorted(case_id for case_id in set(ids) if ids.count(case_id) > 1)
    require(not duplicates, f"duplicate {label} case ids: {duplicates}")
    missing = sorted(set(required) - set(ids))
    extra = sorted(set(ids) - set(required), key=str)
    require(not missing, f"missing required cases: {missing}")
    require(not extra, f"unexpected cases: {extra}")


def validate_report(report):
    scan_sensitive(report)
    require_keys(report, ("schema_version", "status", "candidate", "execution", "workers", "relay_topology", "performance_cases", "relay_cases", "correctness"), "report")
    require(report["schema_version"] == SCHEMA_VERSION, "schema_version is unsupported")
    require(report["status"] == "PASSED", "report status must be PASSED")
    candidate = report["candidate"]
    require_keys(candidate, ("source_sha", "built_from_sha"), "candidate")
    source_sha = candidate["source_sha"]
    require(SHA_RE.fullmatch(source_sha or ""), "candidate.source_sha is invalid")
    require(candidate["built_from_sha"] == source_sha, "candidate.built_from_sha does not match source_sha")
    execution = report["execution"]
    require_keys(execution, ("environment", "linux", "started_at", "completed_at"), "execution")
    require(execution["environment"] == "real" and execution["linux"] is True, "performance execution must use real Linux hosts")

    workers = report["workers"]
    require(isinstance(workers, list) and len(workers) == 2, "exactly two Workers are required")
    for worker in workers:
        require_keys(worker, ("worker_id", "machine_id_sha256"), "worker")
        require(isinstance(worker["worker_id"], str) and worker["worker_id"], "worker_id is required")
        require(HASH_RE.fullmatch(worker["machine_id_sha256"] or ""), "Worker machine_id_sha256 is invalid")
    require(len({worker["worker_id"] for worker in workers}) == 2, "Worker IDs must be unique")
    require(len({worker["machine_id_sha256"] for worker in workers}) == 2, "Worker machine IDs must be independent")
    worker_ids = {worker["worker_id"] for worker in workers}

    topology = report["relay_topology"]
    require_keys(topology, ("real_nat", "loopback_only", "relay_binary_sha256", "server_binary_sha256", "worker_binary_sha256"), "relay_topology")
    require(topology["real_nat"] is True, "relay_topology.real_nat must be true")
    require(topology["loopback_only"] is False, "loopback-only Relay evidence is forbidden")
    for field in ("relay_binary_sha256", "server_binary_sha256", "worker_binary_sha256"):
        require(HASH_RE.fullmatch(topology[field] or ""), f"relay_topology.{field} is invalid")

    exact_case_ids(report["performance_cases"], PERF_CASES, "performance_cases")
    exact_case_ids(report["relay_cases"], RELAY_CASES, "relay_cases")
    require({item.get("size_class") for item in report["performance_cases"]} == {"small", "medium", "large"}, "performance cases must cover small, medium and large repositories")
    attempt_ids = set()
    for item in report["performance_cases"]:
        validate_performance_case(item, source_sha, attempt_ids)
    for item in report["relay_cases"]:
        validate_relay_case(item, source_sha, worker_ids, attempt_ids)

    correctness = report["correctness"]
    for field in ("duplicate_attempts", "lost_event_watermarks", "invalid_artifacts"):
        require(isinstance(correctness, dict) and correctness.get(field) == 0, f"correctness.{field} must be zero")


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
