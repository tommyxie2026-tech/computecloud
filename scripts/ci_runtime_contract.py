#!/usr/bin/env python3
import argparse
import json
import pathlib
import subprocess
import sys
import time


CORE_FILES = [
    pathlib.Path("internal/worker/worker.go"),
    pathlib.Path("internal/worker/execute.go"),
    pathlib.Path("internal/server/server.go"),
]


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

    result = run([
        "go", "test",
        "./internal/adapter",
        "./internal/server",
        "./internal/worker",
        "-run", "RuntimeV2|NativeFinalAndProtocolBounds",
        "-count=1", "-v",
    ])

    violations = {}
    for path in CORE_FILES:
        text = path.read_text(encoding="utf-8")
        found = [name for name in ("codex_exec", "claude_print") if name in text]
        if found:
            violations[str(path)] = found

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-runtime-contract.v1",
        "real_model_calls": False,
        "status": status,
        "coverage": {
            "registry_driven_dispatch": True,
            "builtin_provider_contracts": True,
            "custom_provider_registration": True,
            "runtime_tool_environment_namespaces": True,
            "legacy_capability_compatibility": True,
            "gateway_capability_owned_by_provider": True,
            "scheduler_namespaced_capability_match": True,
            "core_profile_hardcoding_scan": not bool(violations),
        },
        "hardcoding_violations": violations,
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
        sys.stderr.write("runtime profile hardcoding remains in core files: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
