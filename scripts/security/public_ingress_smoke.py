#!/usr/bin/env python3
"""Run bounded, credential-redacted HTTP checks against an explicitly supplied endpoint."""

import argparse
import json
from pathlib import Path
import secrets
import sys
import urllib.error
import urllib.request
from urllib.parse import urlparse


def request(url, token=None, method="GET", body=None, timeout=10):
    headers = {"Accept": "application/json"}
    if token is not None:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
    if method == "POST" and url.endswith("/v1/jobs"):
        headers["Idempotency-Key"] = "public-ingress-smoke-" + secrets.token_hex(16)
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, response.read(65536)
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(65536)


def run(base_url, token, oversized_bytes=17 * 1024 * 1024, max_rate_probes=64,
        queue_body=None, confirm_queue_test=False, requester=request):
    parsed = urlparse(base_url)
    if parsed.scheme != "https" and not (parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "::1", "localhost"}):
        raise ValueError("endpoint must use HTTPS; HTTP is allowed only for literal loopback")
    base = base_url.rstrip("/")
    result = {"unauthenticated": "not_checked", "oversized": "not_checked", "queue_saturation": "skipped", "rate_limit": "not_checked"}
    code, body = requester(base + "/v1/capabilities")
    result["unauthenticated"] = "pass" if code == 401 else "fail"
    if token.encode() in body:
        result["unauthenticated"] = "fail"
    too_large = b'{"padding":"' + b"x" * max(1, oversized_bytes) + b'"}'
    code, body = requester(base + "/v1/jobs", token, "POST", too_large)
    result["oversized"] = "pass" if code == 413 and token.encode() not in body else "fail"
    if queue_body is not None:
        if not confirm_queue_test:
            raise ValueError("queue test requires --confirm-queue-test because it submits a real Job")
        if len(queue_body) > 16 * 1024 * 1024:
            raise ValueError("queue Job file exceeds the 16 MiB safety cap")
        code, body = requester(base + "/v1/jobs", token, "POST", queue_body)
        try:
            error_code = json.loads(body).get("error", {}).get("code", "")
        except (json.JSONDecodeError, AttributeError):
            error_code = ""
        result["queue_saturation"] = "pass" if code == 413 and error_code.startswith("QUEUE_BACKPRESSURE") else ("not_saturated" if code in {200, 201, 202} else "fail")
        if token.encode() in body:
            result["queue_saturation"] = "fail"
    for _ in range(max(1, min(max_rate_probes, 1000))):
        code, body = requester(base + "/v1/capabilities", token)
        if token.encode() in body:
            result["rate_limit"] = "fail"
            break
        if code == 429:
            result["rate_limit"] = "pass"
            break
        if code != 200:
            result["rate_limit"] = "fail"
            break
    result["passed"] = all(result[key] == "pass" for key in ("unauthenticated", "oversized", "rate_limit")) and result["queue_saturation"] in {"pass", "skipped"}
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="Explicit HTTPS Server endpoint, e.g. https://api.example.com")
    parser.add_argument("--token-file", required=True, type=Path, help="Protected file containing a test Bearer token")
    parser.add_argument("--oversized-bytes", type=int, default=17 * 1024 * 1024)
    parser.add_argument("--max-rate-probes", type=int, default=64)
    parser.add_argument("--queue-job-file", type=Path, help="Optional valid small Job JSON; sends one real Job")
    parser.add_argument("--confirm-queue-test", action="store_true", help="Confirm the optional queue probe may submit one real Job")
    args = parser.parse_args(argv)
    try:
        token = args.token_file.read_text(encoding="utf-8").strip()
        if not token or "\n" in token or "\r" in token:
            raise ValueError("test token file must contain one non-empty line")
        queue_body = args.queue_job_file.read_bytes() if args.queue_job_file else None
        results = run(args.base_url, token, args.oversized_bytes, args.max_rate_probes,
                      queue_body, args.confirm_queue_test)
    except (OSError, ValueError) as exc:
        print(json.dumps({"passed": False, "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 2
    print(json.dumps(results, sort_keys=True))
    return 0 if results["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
