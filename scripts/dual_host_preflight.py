#!/usr/bin/env python3
"""Collect and compare Linux host preflight records; never certifies test cases."""

import argparse
import hashlib
import json
import os
import pathlib
import platform
import re
import subprocess
import sys
from datetime import datetime, timezone


SHA_RE = re.compile(r"[0-9a-f]{40}")


def fail(message):
    raise ValueError(message)


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def machine_id_hash():
    for name in ("/etc/machine-id", "/var/lib/dbus/machine-id"):
        path = pathlib.Path(name)
        if path.is_file():
            raw = path.read_text(encoding="ascii").strip()
            if raw:
                return hashlib.sha256(raw.encode("ascii")).hexdigest()
    fail("Linux machine-id missing; host independence cannot be checked")


def runtime_versions(values):
    versions = {}
    for value in values:
        name, sep, executable = value.partition("=")
        if not sep or name not in ("codex", "claude") or not executable:
            fail("--runtime must be codex=PATH or claude=PATH")
        if name in versions:
            fail(f"duplicate runtime: {name}")
        path = pathlib.Path(executable).resolve(strict=True)
        if not path.is_file() or not os.access(path, os.X_OK):
            fail(f"runtime is not executable: {path}")
        result = subprocess.run(
            [str(path), "--version"], capture_output=True, text=True,
            timeout=10, check=False,
        )
        if result.returncode != 0:
            fail(f"{name} --version failed with exit code {result.returncode}")
        version = result.stdout.strip().splitlines()
        if not version or not version[0].strip():
            fail(f"{name} --version returned no version")
        versions[name] = {"version": version[0][:200], "sha256": sha256_file(path)}
    return versions


def collect(args):
    if platform.system() != "Linux":
        fail("preflight collection must run on each Linux host")
    if not SHA_RE.fullmatch(args.source_sha):
        fail("--source-sha must be a full lowercase Git SHA")
    if not args.run_id.strip() or not args.node_id.strip():
        fail("run and node IDs are required")
    data_dir = pathlib.Path(args.data_dir).resolve(strict=True)
    if not data_dir.is_dir():
        fail("--data-dir must be an existing directory")
    binary = pathlib.Path(args.binary).resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        fail("--binary must be an executable file")
    versions = runtime_versions(args.runtime)
    if args.role == "worker" and (set(versions) != {"codex", "claude"} or not args.mcp_version):
        fail("each Worker requires Codex, Claude and MCP version records")
    if args.role == "server" and (versions or args.mcp_version):
        fail("record runtime/MCP versions on Worker hosts only")
    uname = platform.uname()
    record = {
        "schema_version": 1,
        "run_id": args.run_id,
        "source_sha": args.source_sha,
        "captured_at_utc": datetime.now(timezone.utc).isoformat(),
        "role": args.role,
        "node_id": args.node_id,
        "machine_id_sha256": machine_id_hash(),
        "os": uname.system,
        "kernel": uname.release,
        "arch": uname.machine,
        "data_dir": str(data_dir),
        "binary_sha256": sha256_file(binary),
        "runtime_versions": versions,
        "mcp_version": args.mcp_version or None,
    }
    out = pathlib.Path(args.out)
    if out.exists():
        fail(f"refusing to overwrite existing record: {out}")
    out.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(record, stream, indent=2, sort_keys=True)
        stream.write("\n")
    print(out)


def verify_records(records):
    if len(records) != 3:
        fail("supply one Server and exactly two Worker records")
    if not all(isinstance(record, dict) for record in records):
        fail("each preflight record must be a JSON object")
    roles = [record.get("role") for record in records]
    if roles.count("server") != 1 or roles.count("worker") != 2:
        fail("records must contain one Server and two Workers")
    for key in ("run_id", "source_sha"):
        if len({record.get(key) for record in records}) != 1:
            fail(f"{key} differs between hosts")
    if not isinstance(records[0].get("run_id"), str) or not records[0]["run_id"].strip():
        fail("run_id is missing")
    if not SHA_RE.fullmatch(str(records[0].get("source_sha", ""))):
        fail("source_sha is not a full Git SHA")
    if len({record.get("node_id") for record in records}) != 3:
        fail("node IDs must be unique")
    for record in records:
        if record.get("schema_version") != 1 or record.get("os") != "Linux":
            fail("all records must be Linux preflight schema v1")
        if not isinstance(record.get("node_id"), str) or not record["node_id"].strip():
            fail("node_id is missing")
        if not pathlib.PurePosixPath(str(record.get("data_dir", ""))).is_absolute():
            fail("data_dir must be absolute")
        if not re.fullmatch(r"[0-9a-f]{64}", str(record.get("machine_id_sha256", ""))):
            fail("missing machine-id hash")
        if not re.fullmatch(r"[0-9a-f]{64}", str(record.get("binary_sha256", ""))):
            fail("missing executable checksum")
    workers = [record for record in records if record["role"] == "worker"]
    if workers[0]["machine_id_sha256"] == workers[1]["machine_id_sha256"]:
        fail("Workers have the same machine-id; independent hosts not demonstrated")
    for worker in workers:
        if set(worker.get("runtime_versions", {})) != {"codex", "claude"} or not worker.get("mcp_version"):
            fail("Worker runtime/MCP versions are incomplete")
        for version in worker["runtime_versions"].values():
            if not isinstance(version, dict) or not str(version.get("version", "")).strip():
                fail("Worker runtime version string is missing")
            if not re.fullmatch(r"[0-9a-f]{64}", str(version.get("sha256", ""))):
                fail("Worker runtime executable checksum is missing")
    for left in records:
        for right in records:
            if left is not right and left["machine_id_sha256"] == right["machine_id_sha256"] and left.get("data_dir") == right.get("data_dir"):
                fail("Server and Worker on one host cannot share a data directory")
    server = next(record for record in records if record["role"] == "server")
    production_hosts = []
    for record in sorted(records, key=lambda item: (item["role"] != "server", item["node_id"])):
        production_hosts.append({
            "role": record["role"],
            "node_id": record["node_id"],
            "machine_id_sha256": record["machine_id_sha256"],
            "binary_sha256": record["binary_sha256"],
            "source_sha": record["source_sha"],
            "os": record["os"],
            "runtime_versions": record.get("runtime_versions", {}),
            "mcp_version": record.get("mcp_version"),
        })
    return {
        "run_id": server["run_id"],
        "source_sha": server["source_sha"],
        "worker_hosts_independent": True,
        "server_host_independent": all(server["machine_id_sha256"] != worker["machine_id_sha256"] for worker in workers),
        "v047_production_hosts": production_hosts,
        "scope": "preflight metadata only; no Job, fault, upgrade or performance acceptance",
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    collector = commands.add_parser("collect")
    collector.add_argument("--run-id", required=True)
    collector.add_argument("--source-sha", required=True)
    collector.add_argument("--role", choices=("server", "worker"), required=True)
    collector.add_argument("--node-id", required=True)
    collector.add_argument("--data-dir", required=True)
    collector.add_argument("--binary", required=True)
    collector.add_argument("--runtime", action="append", default=[])
    collector.add_argument("--mcp-version")
    collector.add_argument("--out", required=True)
    verifier = commands.add_parser("verify")
    verifier.add_argument("records", nargs=3, type=pathlib.Path)
    args = parser.parse_args()
    try:
        if args.command == "collect":
            collect(args)
        else:
            records = [json.loads(path.read_text(encoding="utf-8")) for path in args.records]
            print(json.dumps(verify_records(records), indent=2, sort_keys=True))
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        parser.exit(1, f"preflight: {exc}\n")


if __name__ == "__main__":
    main()
