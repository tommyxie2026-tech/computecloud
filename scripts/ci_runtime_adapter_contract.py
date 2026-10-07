#!/usr/bin/env python3
import argparse
import json
import pathlib
import subprocess
import sys
import time

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
        "go", "test", "./internal/adapter", "./internal/agenthttp", "./internal/worker",
        "-run", "BuiltinProvidersAdvertiseOnlyCertifiedControlCapabilities|BuiltinControlDescriptorMatchesRuntimeCapabilities|RuntimeV2BuiltinsAndCapabilityNamespaces|RuntimeExecutionRefRoundTrip|RemoteRuntimeExecutesWithoutLocalAgentProcess|HTTPRuntime|RunLifecycle|RunRejects|EnvironmentFile",
        "-count=1", "-v",
    ])

    contract = pathlib.Path("internal/adapter/control_contract.go").read_text(encoding="utf-8")
    adapter = pathlib.Path("internal/adapter/adapter.go").read_text(encoding="utf-8")
    violations = []
    for required in [
        "func (p codexProvider) ControlDescriptor()",
        "func (p claudeProvider) ControlDescriptor()",
        "CapabilityStreamOutput",
        "CapabilityStructuredOutput",
        "CapabilityCancel",
    ]:
        if required not in contract:
            violations.append("missing certified builtin control contract: " + required)
    for forbidden in [
        "CapabilitySessionResume",
        "CapabilityInteractiveInput",
        "CapabilityApproval",
        "CapabilityInterrupt",
    ]:
        # These symbols may exist in request interfaces, but must never appear in
        # builtinControlDescriptor, which is the certified advertisement source.
        start = contract.find("func builtinControlDescriptor")
        end = contract.find("func (p codexProvider) ControlDescriptor")
        body = contract[start:end] if start >= 0 and end > start else ""
        if forbidden in body:
            violations.append("builtin descriptor exposes uncertified capability: " + forbidden)
    if 'if p.Profile() == "codex_exec"' in adapter or 'if p.Profile() == "claude_print"' in adapter:
        violations.append("runtime Provider core contains profile-name branching")

    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")
    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-runtime-adapter-contract.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "codex_provider_certified": True,
            "claude_provider_certified": True,
            "read_stream_capability": True,
            "cancel_capability": True,
            "interactive_capabilities_fail_closed": True,
            "runtime_execution_ref": True,
            "remote_provider_transport_neutral": True,
            "self_hosted_http_contract": True,
            "docker_attempt_command_contract": True,
        },
        "violations": violations,
        "command": result["command"],
        "duration_ms": result["duration_ms"],
        "returncode": result["returncode"],
    }
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": status, "report": str(out)}))
    if result["returncode"] != 0:
        sys.stderr.write(result["stdout"])
        sys.stderr.write(result["stderr"])
        failures = [line for line in (result["stdout"] + "\n" + result["stderr"]).splitlines()
                    if "--- FAIL:" in line or "panic:" in line or "server_test.go:" in line
                    or "http_runtime_test.go:" in line or "runtime_execution_test.go:" in line]
        for line in failures[:8]:
            print("::error::" + line.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A"))
    if violations:
        sys.stderr.write("runtime adapter contract violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
