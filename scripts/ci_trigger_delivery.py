#!/usr/bin/env python3
"""Submit one pinned CI Job and write an immutable, bounded delivery record."""

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import time


DELIVERY_RE = re.compile(r"[A-Za-z0-9_.-]{1,128}")
COMMIT_RE = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})")
TERMINAL = {"SUCCEEDED", "FAILED", "CANCELED"}


def submission_key(delivery_id, commit):
    digest = hashlib.sha256((delivery_id + "\0" + commit).encode("ascii")).hexdigest()
    return "ci-" + digest[:48]


def load_spec(path, commit):
    raw = path.read_bytes()
    if not raw or len(raw) > 256 << 10:
        raise ValueError("Job spec must be nonempty and at most 256 KiB")
    spec = json.loads(raw)
    if not isinstance(spec, dict) or not isinstance(spec.get("workspace"), dict) or spec["workspace"].get("base_commit") != commit:
        raise ValueError("Job workspace.base_commit must match --source-commit")
    return raw


def invoke(binary, config, operation, **options):
    command = [str(binary), "job", operation, "--config", str(config)]
    for key, value in options.items():
        command.extend(["--" + key.replace("_", "-"), str(value)])
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=30, check=False)
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(operation.upper() + "_TIMEOUT") from exc
    if result.returncode != 0:
        # The CLI may include upstream details in stderr. Do not copy them into
        # CI output or the delivery record.
        raise RuntimeError(operation.upper() + "_FAILED")
    if len(result.stdout) > 1 << 20:
        raise RuntimeError(operation.upper() + "_RESPONSE_TOO_LARGE")
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(operation.upper() + "_INVALID_RESPONSE") from exc
    if not isinstance(payload, dict):
        raise RuntimeError(operation.upper() + "_INVALID_RESPONSE")
    return payload


def artifact_provenance(result):
    items = result.get("final_artifacts", [])
    if not isinstance(items, list) or len(items) > 100:
        raise RuntimeError("INVALID_ARTIFACT_PROVENANCE")
    out = []
    for item in items:
        if not isinstance(item, dict):
            raise RuntimeError("INVALID_ARTIFACT_PROVENANCE")
        aid = item.get("artifact_id")
        digest = item.get("sha256")
        if not isinstance(aid, str) or not aid or not re.fullmatch(r"[0-9a-f]{64}", str(digest)):
            raise RuntimeError("INVALID_ARTIFACT_PROVENANCE")
        out.append({"artifact_id": aid, "sha256": digest})
    return out


def write_report(path, report):
    if path.exists():
        raise ValueError("refusing to overwrite an existing delivery record")
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(report, stream, indent=2, sort_keys=True)
        stream.write("\n")


def run(args):
    if not DELIVERY_RE.fullmatch(args.delivery_id) or not COMMIT_RE.fullmatch(args.source_commit):
        raise ValueError("bounded delivery ID and full lowercase source commit are required")
    if args.timeout_seconds < 1 or args.timeout_seconds > 7 * 24 * 3600 or args.poll_seconds < 1 or args.poll_seconds > 60:
        raise ValueError("invalid timeout or poll interval")
    binary = args.binary.resolve(strict=True)
    config = args.config.resolve(strict=True)
    spec = args.spec.resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("--binary must be executable")
    if args.out.exists():
        raise ValueError("refusing to overwrite an existing delivery record")
    raw = load_spec(spec, args.source_commit)
    report = {
        "schema_version": 1,
        "delivery_id": args.delivery_id,
        "source_commit": args.source_commit,
        "spec_sha256": hashlib.sha256(raw).hexdigest(),
        "submission_key": submission_key(args.delivery_id, args.source_commit),
        "status": "STARTED",
    }
    try:
        submitted = invoke(binary, config, "submit", file=spec, key=report["submission_key"])
        job_id = submitted.get("job_id")
        if not isinstance(job_id, str) or not job_id:
            raise RuntimeError("SUBMIT_INVALID_RESPONSE")
        report["job_id"] = job_id
        report["existing"] = submitted.get("existing") is True
        deadline = time.monotonic() + args.timeout_seconds
        while True:
            current = invoke(binary, config, "get", id=job_id)
            if current.get("job_id") != job_id:
                raise RuntimeError("POLL_ID_MISMATCH")
            state = current.get("state")
            if state in TERMINAL:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("JOB_TIMEOUT")
            time.sleep(min(args.poll_seconds, max(0, deadline - time.monotonic())))
        result = invoke(binary, config, "result", id=job_id)
        if result.get("job_id") != job_id or result.get("state") != state or result.get("base_commit") != args.source_commit:
            raise RuntimeError("RESULT_PROVENANCE_MISMATCH")
        report.update({
            "status": "COMPLETE" if state == "SUCCEEDED" else "JOB_FAILED",
            "job_state": state,
            "trace_id": result.get("trace_id", ""),
            "manifest_sha256": result.get("manifest_sha256", ""),
            "result_sha256": hashlib.sha256(json.dumps(result, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
            "final_artifacts": artifact_provenance(result),
        })
    except RuntimeError as exc:
        report["status"] = "INCOMPLETE"
        report["error_code"] = str(exc)
    write_report(args.out, report)
    print(args.out)
    return 0 if report["status"] == "COMPLETE" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--config", required=True, type=pathlib.Path)
    parser.add_argument("--spec", required=True, type=pathlib.Path)
    parser.add_argument("--delivery-id", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--out", required=True, type=pathlib.Path)
    parser.add_argument("--timeout-seconds", type=int, default=3600)
    parser.add_argument("--poll-seconds", type=int, default=2)
    args = parser.parse_args()
    try:
        return run(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        parser.exit(2, f"ci-trigger-delivery: {exc}\n")


if __name__ == "__main__":
    sys.exit(main())
