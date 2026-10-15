#!/usr/bin/env python3
"""Probe one operator-created Worker egress network without scanning networks."""

import argparse
import json
import re
import subprocess
import sys
from urllib.parse import urlsplit

PINNED_IMAGE = re.compile(r"^.+@sha256:[0-9a-f]{64}$")
SHA256 = re.compile(r"^sha256:[0-9a-f]{64}$")
PROBE = r'''const u=process.argv[1];
const c=new AbortController(); const t=setTimeout(()=>c.abort(),5000);
fetch(u,{method:"GET",signal:c.signal,redirect:"manual"}).then(r=>{clearTimeout(t);console.log(JSON.stringify({connected:true,status:r.status}));process.exit(0)}).catch(e=>{clearTimeout(t);console.log(JSON.stringify({connected:false}));process.exit(2)});'''


def validate_url(value: str, label: str) -> None:
    parsed = urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.fragment:
        raise ValueError(f"{label} must be an explicit HTTP(S) URL without userinfo or fragment")


def probe(docker: str, network: str, image: str, url: str) -> tuple[bool, int | None]:
    result = subprocess.run(
        [docker, "run", "--rm", "--pull=never", "--network", network, "--entrypoint", "node", image, "-e", PROBE, url],
        text=True, capture_output=True, timeout=20, check=False,
    )
    try:
        payload = json.loads(result.stdout.strip().splitlines()[-1])
    except (ValueError, IndexError):
        raise RuntimeError("probe container did not return a valid result") from None
    if result.returncode not in (0, 2):
        raise RuntimeError("probe container could not start; verify image, Docker and network")
    return bool(payload.get("connected")), payload.get("status")


def inspect_platform(docker: str, image: str) -> str:
    result = subprocess.run([docker, "image", "inspect", image, "--format", "{{.Os}}/{{.Architecture}}"], text=True, capture_output=True, timeout=20, check=True)
    return result.stdout.strip()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docker", default="docker")
    parser.add_argument("--network", required=True)
    parser.add_argument("--image", required=True, help="already present, digest-pinned Agent Runtime image")
    parser.add_argument("--platform", required=True, choices=("linux/amd64", "linux/arm64"))
    parser.add_argument("--worker-config-digest", required=True, help="sha256 digest of the reviewed Worker deployment config")
    parser.add_argument("--attempt-id", required=True, help="sanitized acceptance run identifier")
    parser.add_argument("--allowed-model-url", required=True, help="explicit Server model endpoint; only tests reachability")
    parser.add_argument("--metadata-url", required=True, help="explicit metadata test destination")
    parser.add_argument("--host-gateway-url", required=True, help="explicit Worker host-gateway test destination")
    parser.add_argument("--private-test-url", required=True, help="operator-owned isolated private test destination")
    parser.add_argument("--unapproved-public-url", required=True, help="explicit public endpoint that policy must deny")
    parser.add_argument("--output", required=True, help="JSON result file")
    args = parser.parse_args()
    if not PINNED_IMAGE.fullmatch(args.image):
        parser.error("--image must be pinned by @sha256 digest")
    if not SHA256.fullmatch(args.worker_config_digest):
        parser.error("--worker-config-digest must be sha256:<64 lowercase hex>")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,62}", args.network):
        parser.error("invalid Docker network name")
    urls = {
        "allowed_model_endpoint": args.allowed_model_url,
        "metadata_destination": args.metadata_url,
        "worker_host_gateway": args.host_gateway_url,
        "private_test_destination": args.private_test_url,
        "unapproved_public_destination": args.unapproved_public_url,
    }
    try:
        for label, url in urls.items():
            validate_url(url, label)
        if inspect_platform(args.docker, args.image) != args.platform:
            raise ValueError("declared platform does not match the local pinned image")
        results = {}
        for label, url in urls.items():
            connected, status = probe(args.docker, args.network, args.image, url)
            results[label] = {"connected": connected, "http_status": status}
        outcomes = {name: value["connected"] for name, value in results.items()}
        target_host_checks = {
            "target_host_probe_executed": True,
            "attempt_gateway_allowed": outcomes["allowed_model_endpoint"],
            "metadata_egress_denied": not outcomes["metadata_destination"],
            "host_gateway_egress_denied": not outcomes["worker_host_gateway"],
            "private_egress_denied": not outcomes["private_test_destination"],
            "unapproved_public_egress_denied": not outcomes["unapproved_public_destination"],
        }
        passed = outcomes["allowed_model_endpoint"] and not any(
            outcomes[name] for name in ("metadata_destination", "worker_host_gateway", "private_test_destination", "unapproved_public_destination")
        )
        report = {
            "attempt_id": args.attempt_id,
            "platform": args.platform,
            "image_digest": args.image.rsplit("@", 1)[1],
            "worker_config_digest": args.worker_config_digest,
            "network": args.network,
            "results": results,
            "target_host_checks": target_host_checks,
            "passed": passed,
            "secrets_included": False,
        }
        with open(args.output, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")
        print(json.dumps(report, sort_keys=True))
        return 0 if passed else 1
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        print(f"egress acceptance failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
