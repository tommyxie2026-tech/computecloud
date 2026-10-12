#!/usr/bin/env python3
"""Validate a running Agent HTTP controller's Docker inspect security contract."""

import argparse
import json
import re
import subprocess
import sys

PINNED = re.compile(r"^.+@sha256:[0-9a-f]{64}$")
FORBIDDEN_PERSONAL_MOUNTS = ("/.codex", "/.claude", "/.config/claude")


def validate(inspect: dict) -> list[str]:
    errors = []
    config = inspect.get("Config") or {}
    host = inspect.get("HostConfig") or {}
    if not PINNED.fullmatch(config.get("Image", "")):
        errors.append("runtime image is not digest pinned")
    command = config.get("Cmd") or []
    values = {}
    index = 0
    while index < len(command):
        key = command[index]
        if key.startswith("--") and index + 1 < len(command) and not command[index + 1].startswith("--"):
            values[key] = command[index + 1]
            index += 2
        else:
            index += 1
    if "--public-strict" not in command:
        errors.append("controller public strict mode is disabled")
    network = values.get("--egress-network", "")
    if not network or network == "bridge":
        errors.append("controller does not use an operator-created egress network")
    if not values.get("--seccomp-profile", "").startswith("/"):
        errors.append("controller seccomp profile is not configured")
    for flag in ("--memory-bytes", "--nano-cpus", "--pids-limit"):
        try:
            if int(values.get(flag, "0")) < 1:
                raise ValueError
        except ValueError:
            errors.append(f"controller resource limit {flag} is missing")
    if host.get("Privileged"):
        errors.append("controller is running privileged")
    if host.get("CapAdd"):
        errors.append("controller has added Linux capabilities")
    mounts = inspect.get("Mounts") or []
    sources = [str(mount.get("Source", "")).lower() for mount in mounts]
    if not any(source.endswith("/var/run/docker.sock") for source in sources):
        errors.append("trusted controller Docker socket mount is missing")
    if any(any(marker in source for marker in FORBIDDEN_PERSONAL_MOUNTS) for source in sources):
        errors.append("personal Codex/Claude profile mount is present")
    if (config.get("ExposedPorts") or {}) or (host.get("PortBindings") or {}):
        errors.append("Agent Runtime controller exposes a TCP port")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docker", default="docker")
    parser.add_argument("--container", required=True)
    args = parser.parse_args()
    try:
        result = subprocess.run([args.docker, "inspect", args.container], check=True, text=True, capture_output=True)
        payload = json.loads(result.stdout)
        if not isinstance(payload, list) or len(payload) != 1:
            raise ValueError("docker inspect did not return exactly one container")
        errors = validate(payload[0])
    except (OSError, subprocess.CalledProcessError, ValueError, json.JSONDecodeError):
        print("runtime config validation failed: unable to inspect controller", file=sys.stderr)
        return 2
    if errors:
        print(json.dumps({"passed": False, "errors": errors}, sort_keys=True))
        return 1
    print(json.dumps({"passed": True, "errors": []}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
