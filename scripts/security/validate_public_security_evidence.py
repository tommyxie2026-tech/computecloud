#!/usr/bin/env python3
"""Validate sanitized, target-host public security acceptance evidence."""

import argparse
import json
import re
import sys

SHA = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
SENSITIVE_KEY = re.compile(r"token|secret|password|private.?key|authorization|api.?key", re.I)
SENSITIVE_VALUE = re.compile(r"(?:bearer\s+\S+|sk-[A-Za-z0-9_-]{16,}|cc_live_[A-Za-z0-9_-]{16,})", re.I)


def contains_secret(value) -> bool:
    if isinstance(value, dict):
        for key, item in value.items():
            if str(key) == "secrets_included" and item is False:
                continue
            if SENSITIVE_KEY.search(str(key)) or contains_secret(item):
                return True
        return False
    if isinstance(value, list):
        return any(contains_secret(item) for item in value)
    return isinstance(value, str) and bool(SENSITIVE_VALUE.search(value))


def validate(report: dict) -> list[str]:
    errors = []
    if not SHA.fullmatch(str(report.get("candidate_sha", ""))):
        errors.append("candidate_sha is missing or invalid")
    for name in ("server_image_digest", "worker_image_digest", "agent_runtime_image_digest"):
        if not DIGEST.fullmatch(str(report.get(name, ""))):
            errors.append(f"{name} is missing or invalid")
    if report.get("platform") not in ("linux/amd64", "linux/arm64"):
        errors.append("platform is missing or invalid")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", str(report.get("attempt_id", ""))):
        errors.append("attempt_id is missing or invalid")
    if report.get("target_host") is not True or report.get("secrets_included") is not False:
        errors.append("evidence must be target-host and secret-scrubbed")
    required = (
        "container_isolation", "host_cli_fallback_denied", "workspace_escape_denied",
        "metadata_egress_denied", "host_gateway_egress_denied", "private_egress_denied",
        "unapproved_public_egress_denied", "attempt_gateway_allowed", "cleanup_confirmed",
        "revocation_confirmed", "origin_bypass_denied",
    )
    results = report.get("results") or {}
    for key in required:
        if results.get(key) is not True:
            errors.append(f"required result {key} is absent or failed")
    if contains_secret(report):
        errors.append("evidence contains credential-shaped data")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report")
    args = parser.parse_args()
    try:
        with open(args.report, encoding="utf-8") as stream:
            report = json.load(stream)
        if not isinstance(report, dict):
            raise ValueError
    except (OSError, json.JSONDecodeError, ValueError):
        print("security evidence validation failed: invalid JSON report", file=sys.stderr)
        return 2
    errors = validate(report)
    print(json.dumps({"passed": not errors, "errors": errors}, sort_keys=True))
    return 0 if not errors else 1


if __name__ == "__main__":
    raise SystemExit(main())
