#!/usr/bin/env python3
"""Validate the Docker inspect security contract for a Worker service container."""

import argparse
import json
import os
import re
import subprocess
import sys

PINNED = re.compile(r"^.+@sha256:[0-9a-f]{64}$")
PERSONAL_PROFILE = re.compile(r"(?:^|/)(?:\.codex|\.claude|\.config/claude)(?:/|$)", re.IGNORECASE)
DOCKER_SOCKET = re.compile(r"(?:^|/)docker\.sock$")
NETWORKS_BUILT_IN = {"bridge", "host", "none", "default"}


def _number(value) -> int:
    if isinstance(value, bool):
        return 0
    try:
        return int(value)
    except (TypeError, ValueError, OverflowError):
        return 0


def _safe_mounts(policy: dict) -> dict:
    configured = policy.get("allowed_mounts")
    if not isinstance(configured, dict):
        return {}
    result = {}
    for target, rules in configured.items():
        if not isinstance(target, str) or not target.startswith("/") or not isinstance(rules, dict):
            continue
        mode = rules.get("mode")
        source_prefix = rules.get("source_prefix")
        if mode in ("ro", "rw") and isinstance(source_prefix, str) and source_prefix.startswith("/"):
            result[target.rstrip("/") or "/"] = (mode, source_prefix.rstrip("/") or "/")
    return result


def validate(inspect: dict, expected: dict) -> list[str]:
    """Return sanitized policy labels; never include inspected paths or values."""
    errors = []
    if not isinstance(inspect, dict) or not isinstance(expected, dict):
        return ["invalid inspect or policy document"]

    config = inspect.get("Config") if isinstance(inspect.get("Config"), dict) else {}
    host = inspect.get("HostConfig") if isinstance(inspect.get("HostConfig"), dict) else {}

    image = config.get("Image")
    if not isinstance(image, str) or not PINNED.fullmatch(image):
        errors.append("worker image is not digest pinned")

    user = config.get("User")
    uid_part = user.split(":", 1)[0] if isinstance(user, str) else ""
    if not uid_part.isdigit() or int(uid_part) == 0:
        errors.append("worker user must be non-root")

    if host.get("ReadonlyRootfs") is not True:
        errors.append("worker root filesystem is writable")
    cap_drop = host.get("CapDrop") or []
    if not isinstance(cap_drop, list) or "ALL" not in [str(item).upper() for item in cap_drop]:
        errors.append("worker capabilities are not fully dropped")
    if host.get("CapAdd"):
        errors.append("worker has added capabilities")
    security_opts = host.get("SecurityOpt") or []
    if not isinstance(security_opts, list) or not any(
        str(item).lower() in ("no-new-privileges", "no-new-privileges:true") for item in security_opts
    ):
        errors.append("no-new-privileges is disabled")
    if host.get("Privileged") is True:
        errors.append("worker container is privileged")

    for field, label in (("Memory", "memory"), ("NanoCpus", "cpu"), ("PidsLimit", "pid")):
        if _number(host.get(field)) < 1:
            errors.append(f"worker {label} limit is missing")

    exposed = config.get("ExposedPorts") or {}
    bindings = host.get("PortBindings") or {}
    if exposed or bindings:
        errors.append("worker container exposes a host port")
    network = host.get("NetworkMode")
    if not isinstance(network, str) or not network or network.lower() in NETWORKS_BUILT_IN:
        errors.append("worker does not use an operator-created network")
    expected_network = expected.get("network_mode")
    if expected_network and network != expected_network:
        errors.append("worker network does not match policy")

    log = host.get("LogConfig") if isinstance(host.get("LogConfig"), dict) else {}
    log_config = log.get("Config") if isinstance(log.get("Config"), dict) else {}
    if log.get("Type") != "json-file" or not log_config.get("max-size") or _number(log_config.get("max-file")) < 1:
        errors.append("worker log rotation is unbounded")

    mounts = inspect.get("Mounts") or []
    if not isinstance(mounts, list):
        mounts = []
    allowed = _safe_mounts(expected)
    observed_targets = set()
    for mount in mounts:
        if not isinstance(mount, dict):
            errors.append("invalid worker mount")
            continue
        source = str(mount.get("Source", "")).replace("\\", "/").rstrip("/") or "/"
        destination = str(mount.get("Destination", "")).replace("\\", "/").rstrip("/") or "/"
        if PERSONAL_PROFILE.search(source):
            errors.append("personal profile mount is present")
            continue
        if DOCKER_SOCKET.search(source) or DOCKER_SOCKET.search(destination):
            errors.append("unapproved mount target is present")
            continue
        observed_targets.add(destination)
        rule = allowed.get(destination)
        if rule is None:
            errors.append("unapproved mount target is present")
            continue
        mode, source_prefix = rule
        if source != source_prefix and not source.startswith(source_prefix + "/"):
            errors.append("mount source violates policy")
        if bool(mount.get("RW")) != (mode == "rw"):
            errors.append("mount access mode violates policy")
    if observed_targets != set(allowed):
        errors.append("required worker mounts do not match policy")

    return list(dict.fromkeys(errors))


def _read_policy(path: str) -> dict:
    info = os.stat(path, follow_symlinks=False)
    if not os.path.isfile(path) or info.st_size > 64 * 1024:
        raise ValueError("policy file is not a bounded regular file")
    if info.st_mode & 0o077:
        raise ValueError("policy file permissions must restrict group and other access")
    with open(path, "r", encoding="utf-8") as stream:
        payload = json.load(stream)
    if not isinstance(payload, dict):
        raise ValueError("policy file must contain a JSON object")
    return payload


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docker", default="docker")
    parser.add_argument("--container", required=True)
    parser.add_argument("--policy", required=True, help="protected JSON policy file; mode 0600 or stricter")
    args = parser.parse_args()
    try:
        policy = _read_policy(args.policy)
        result = subprocess.run([args.docker, "inspect", args.container], check=True, text=True, capture_output=True)
        payload = json.loads(result.stdout)
        if not isinstance(payload, list) or len(payload) != 1:
            raise ValueError("docker inspect did not return exactly one container")
        errors = validate(payload[0], policy)
    except (OSError, subprocess.CalledProcessError, ValueError, json.JSONDecodeError):
        print("worker container validation failed: unable to validate policy or inspect container", file=sys.stderr)
        return 2
    report = {"passed": not errors, "errors": errors}
    print(json.dumps(report, sort_keys=True))
    return 0 if not errors else 1


if __name__ == "__main__":
    raise SystemExit(main())
