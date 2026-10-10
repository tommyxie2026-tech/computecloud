#!/usr/bin/env python3
"""Run native conversation protocol fixtures and emit sanitized evidence."""

import argparse
import json
import pathlib
import os
import subprocess
import sys


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default="dist/ci-conversation-jobs/report.json")
    args = parser.parse_args()
    command = [os.environ.get("GO", "go"), "test", "-json", "-v", "./internal/server", "-run", "^TestConversationMessagesExecutesDurableWorkerJob$", "-count=1", "-timeout=55s"]
    try:
        completed = subprocess.run(command, text=True, capture_output=True, check=False, timeout=70)
    except subprocess.TimeoutExpired as error:
        output = pathlib.Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps({"schema": "computecloud.native-conversation-ci.v1", "passed": False, "failure": "fixture timed out"}, indent=2) + "\n", encoding="utf-8")
        sys.stderr.write((error.stderr or "")[-6000:])
        return 1
    tests = []
    evidence = []
    output_chunks = []
    for line in completed.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Action") == "pass" and event.get("Test"):
            tests.append(event["Test"])
        output = event.get("Output", "")
        if output:
            output_chunks.append(output)
    marker = "CI_EVIDENCE "
    combined = "".join(output_chunks)
    if marker in combined:
        candidate = combined.split(marker, 1)[1].strip()
        try:
            evidence, _ = json.JSONDecoder().raw_decode(candidate)
        except json.JSONDecodeError:
            evidence = []
    report = {
        "schema": "computecloud.native-conversation-ci.v1",
        "passed": completed.returncode == 0,
        "tests": sorted(set(tests)),
        "evidence": evidence,
        "server_process": "in-process Go Server fixture",
        "worker_execution": "real fixture CLI child process",
        "real_provider_credentials_used": False,
    }
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    if completed.returncode:
        sys.stderr.write(completed.stderr[-6000:])
        return completed.returncode
    if not evidence:
        sys.stderr.write("native conversation test passed without emitting acceptance evidence\n")
        return 1
    print(f"conversation fixture tests passed; sanitized evidence: {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
