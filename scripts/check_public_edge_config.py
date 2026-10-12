#!/usr/bin/env python3
"""Validate the repository's public-edge policy manifest."""

import argparse
import json
from pathlib import Path
import sys


DEFAULT_MANIFEST = Path(__file__).resolve().parents[1] / "docs/deployment/public-edge.example.json"
FORBIDDEN_MOUNTS = {"/var/run/docker.sock", "docker.sock", "database", "workspace", "metrics"}
FORBIDDEN_SERVICES = {"database", "metrics", "admin", "docker_socket", "workspace"}


def validate(document):
    errors = []
    expected_ports = [7443, 7444]
    if document.get("public_ports") != expected_ports:
        errors.append("public_ports must be exactly [7443, 7444]")
    docker = document.get("docker", {})
    if docker.get("published_ports") != expected_ports:
        errors.append("docker.published_ports must be exactly [7443, 7444]")
    listeners = {item.get("port"): item for item in document.get("listeners", [])}
    grpc = listeners.get(7443, {})
    if grpc.get("protocol") != "tcp" or grpc.get("mode") != "tls_passthrough" or grpc.get("upstream_port") != 7443:
        errors.append("7443 must use TCP/TLS passthrough to upstream 7443")
    api = listeners.get(7444, {})
    if (api.get("protocol") != "https" or api.get("mode") != "https_gateway" or
            api.get("upstream_port") != 7444 or api.get("upstream_tls") is not True or
            api.get("cache") is not False or api.get("preserve_authorization") is not True):
        errors.append("7444 must use a non-caching HTTPS Gateway with TLS upstream and Authorization preserved")
    origin = document.get("origin", {})
    if origin.get("publicly_reachable") is not False or origin.get("allowed_sources") != "edge_proxy_only":
        errors.append("origin must deny public bypass and allow only edge proxy sources")
    tls = document.get("server_tls", {})
    if tls.get("enabled") is not True or tls.get("minimum_version") not in {"TLS1.2", "TLS1.3"}:
        errors.append("Server TLS 1.2 or newer must be enabled")
    mounts = {str(item).lower() for item in docker.get("mounts", [])}
    if any(item in mount for mount in mounts for item in FORBIDDEN_MOUNTS):
        errors.append("Docker mounts must not expose Docker socket, database, workspace, or metrics")
    exposed = {str(item).lower() for item in document.get("forbidden_public_services", [])}
    missing = FORBIDDEN_SERVICES - exposed
    if missing:
        errors.append("forbidden_public_services must explicitly deny: " + ", ".join(sorted(missing)))
    return errors


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", nargs="?", type=Path, default=DEFAULT_MANIFEST)
    args = parser.parse_args(argv)
    try:
        document = json.loads(args.manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"invalid edge manifest: {exc}", file=sys.stderr)
        return 2
    errors = validate(document)
    if errors:
        print("public edge policy rejected:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1
    print(f"public edge policy valid: {args.manifest}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
