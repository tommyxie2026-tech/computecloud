#!/usr/bin/env python3
import argparse
import json
import os
import pathlib
import subprocess
import sys
import time


def run(command):
    started = time.time()
    process = subprocess.run(command, text=True, capture_output=True)
    return {
        "command": command,
        "returncode": process.returncode,
        "duration_ms": int((time.time() - started) * 1000),
        "stdout": process.stdout,
        "stderr": process.stderr,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    logs = output.parent / "logs"
    logs.mkdir(parents=True, exist_ok=True)

    commands = [
        ["make", "generate"],
        ["git", "diff", "--exit-code", "--", "api/agent/v1/runtime.pb.go", "api/agent/v1/runtime_grpc.pb.go"],
        ["go", "test", "./internal/adapter", "./internal/agenthttp", "-run", "USD|Budget|HTTPRuntime|RunLifecycle|Capabilities", "-count=1", "-v"],
        ["go", "test", "./internal/worker", "-run", "BudgetExhausted|ConfiguredRuntimeCapability|RemoteRuntime", "-count=1", "-v"],
        ["go", "test", "./internal/governance", "-run", "RemainingUsage|UsageOverflow", "-count=1", "-v"],
        ["go", "test", "./internal/server", "-run", "RuntimeBudget|FiniteBudgetGoal|BudgetedCompletion|BudgetedWorkerRestart|BudgetExhausted|UnknownUsage|GoalGovernanceProjectsOnlySupportedRuntimeBudget|GoalPublication", "-count=1", "-v"],
    ]
    results = [run(command) for command in commands]
    oci_requested = os.environ.get("CI_RUNTIME_BUDGET_OCI") == "1"
    oci_results = {}
    if oci_requested:
        fixture = output.parent / "oci-fixture"
        fixture.mkdir(parents=True, exist_ok=True)
        (fixture / "Dockerfile").write_text(
            "FROM alpine:3.22\nCOPY claude /usr/local/bin/claude\nRUN chmod 0755 /usr/local/bin/claude\nUSER 65532:65532\n",
            encoding="utf-8",
        )
        executable = fixture / "claude"
        executable.write_text(
            """#!/bin/sh
cat >/dev/null
case "${FAKE_CLAUDE_SCENARIO:-}" in
  normal)
    printf '%s\\n' '{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.000050,"usage":{"input_tokens":1,"output_tokens":2}}'
    ;;
  budget)
    printf '%s\\n' '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"stopped","total_cost_usd":1.2500001,"usage":{"input_tokens":3,"output_tokens":4}}'
    exit 1
    ;;
  container_failure)
    exit 42
    ;;
  stream_interruption)
    printf '%s' '{"type":"result"'
    ;;
  missing_final)
    printf '%s\\n' '{"type":"assistant","message":{"content":[]}}'
    ;;
  *) exit 64 ;;
esac
""",
            encoding="utf-8",
        )
        executable.chmod(0o755)
        image = "computecloud-runtime-budget-fixture:ci"
        build = run(["docker", "build", "--pull", "-t", image, str(fixture)])
        results.append(build)
        matrix = {
            "oci_normal_completion": "TestClaudeBudgetOCINormalCompletion",
            "oci_native_budget_termination": "TestClaudeBudgetOCINativeBudgetTermination",
            "oci_container_failure": "TestClaudeBudgetOCIContainerFailure",
            "oci_stream_interruption": "TestClaudeBudgetOCIStreamInterruption",
            "oci_missing_final": "TestClaudeBudgetOCIMissingFinal",
        }
        for name, test_name in matrix.items():
            if build["returncode"] == 0:
                command = ["env", f"COMPUTECLOUD_BUDGET_TEST_IMAGE={image}", "go", "test", "./internal/agenthttp", "-run", f"^{test_name}$", "-count=1", "-v"]
                result = run(command)
            else:
                result = {"command": ["go", "test", test_name], "returncode": 1, "duration_ms": 0, "stdout": "", "stderr": "OCI fixture image build failed\n"}
            results.append(result)
            oci_results[name] = result["returncode"] == 0

    for index, result in enumerate(results):
        (logs / f"{index:02d}.stdout.log").write_text(result["stdout"], encoding="utf-8")
        (logs / f"{index:02d}.stderr.log").write_text(result["stderr"], encoding="utf-8")

    passed = all(result["returncode"] == 0 for result in results)
    status = "PASSED" if passed else "FAILED"
    report = {
        "schema_version": "ci-runtime-budget.v1",
        "status": status,
        "real_model_calls": False,
        "real_docker_calls": oci_requested,
        "coverage": {
            "protocol_generation_current": results[0]["returncode"] == 0 and results[1]["returncode"] == 0,
            "assignment_propagation": results[5]["returncode"] == 0,
            "capability_fencing": results[2]["returncode"] == 0 and results[5]["returncode"] == 0,
            "decimal_safety": results[2]["returncode"] == 0,
            "single_inflight": results[5]["returncode"] == 0,
            "atomic_settlement": results[5]["returncode"] == 0,
            "crash_and_incomplete_usage_fail_closed": results[4]["returncode"] == 0 and results[5]["returncode"] == 0,
            "budget_exhaustion_terminal": results[3]["returncode"] == 0 and results[5]["returncode"] == 0,
            "docker_command_contract_fixture": results[2]["returncode"] == 0,
            **oci_results,
        },
        "commands": [{"command": result["command"], "returncode": result["returncode"], "duration_ms": result["duration_ms"]} for result in results],
    }
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": status, "report": str(output)}))
    if not passed:
        for result in results:
            if result["returncode"] != 0:
                sys.stderr.write("FAILED: " + " ".join(result["command"]) + "\n")
                sys.stderr.write(result["stdout"])
                sys.stderr.write(result["stderr"])
        raise SystemExit(1)


if __name__ == "__main__":
    main()
