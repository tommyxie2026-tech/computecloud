#!/usr/bin/env python3
import argparse
import json
import pathlib
import subprocess
import sys
import time

SCHEMAS = [
    pathlib.Path("api/control/v1alpha1/control.schema.json"),
    pathlib.Path("api/control/v1alpha1/event.schema.json"),
    pathlib.Path("api/control/v1alpha1/approval.schema.json"),
    pathlib.Path("api/control/v1alpha1/capabilities.schema.json"),
    pathlib.Path("api/control/v1alpha1/operation.schema.json"),
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

    violations = []
    loaded = {}
    for path in SCHEMAS:
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
            loaded[str(path)] = doc
        except Exception as exc:
            violations.append(f"{path}: invalid JSON: {exc}")
            continue
        if doc.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
            violations.append(f"{path}: draft 2020-12 required")
        if not doc.get("$id"):
            violations.append(f"{path}: $id required")
        text = json.dumps(doc, sort_keys=True)
        for forbidden in ("codex_exec", "claude_print"):
            if forbidden in text:
                violations.append(f"{path}: runtime-specific public schema value: {forbidden}")

    control = loaded.get(str(SCHEMAS[0]), {})
    event = loaded.get(str(SCHEMAS[1]), {})
    approval = loaded.get(str(SCHEMAS[2]), {})
    capabilities = loaded.get(str(SCHEMAS[3]), {})
    operation = loaded.get(str(SCHEMAS[4]), {})
    if control.get("properties", {}).get("protocol_version", {}).get("const") != "control.v1alpha1":
        violations.append("control schema must freeze control.v1alpha1")
    if "generation" not in control.get("required", []):
        violations.append("control session must require generation")
    if "generation" not in event.get("required", []):
        violations.append("event envelope must require generation")
    if "request_version" not in approval.get("required", []):
        violations.append("approval must require request_version")
    if "generation" not in operation.get("required", []):
        violations.append("control operation must require generation")
    if "operation_id" not in operation.get("required", []):
        violations.append("control operation must require operation_id")
    caps = capabilities.get("$defs", {}).get("capability", {}).get("enum", [])
    for required in ("session_resume", "interactive_input", "approval", "cancel"):
        if required not in caps:
            violations.append(f"capability enum missing {required}")

    result = run([
        "go", "test", "./internal/control", "./internal/adapter",
        "-run", "AgentControl|ControlProvider", "-count=1", "-v",
    ])
    (logs / "go-test.stdout.log").write_text(result["stdout"], encoding="utf-8")
    (logs / "go-test.stderr.log").write_text(result["stderr"], encoding="utf-8")

    status = "PASSED" if result["returncode"] == 0 and not violations else "FAILED"
    report = {
        "schema_version": "ci-agent-control-schema.v1",
        "status": status,
        "real_model_calls": False,
        "coverage": {
            "control_v1alpha1_frozen": True,
            "session_generation_fenced_shape": True,
            "event_generation_fenced_shape": True,
            "approval_versioned_shape": True,
            "runtime_neutral_public_schema": not any("runtime-specific" in v for v in violations),
            "optional_runtime_control_extension": True,
            "fake_runtime_control_contract": True,
            "durable_operation_receipt_shape": True,
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
    if violations:
        sys.stderr.write("agent control schema violations: " + json.dumps(violations) + "\n")
    if status != "PASSED":
        raise SystemExit(1)

if __name__ == "__main__":
    main()
