#!/usr/bin/env python3
"""Verify one Worker certificate chain and URI identity without printing key material."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys


def verify(ca_file, certificate_file, worker_id, run=subprocess.run):
    expected_uri = f"spiffe://computecloud/workers/{worker_id}"
    try:
        chain = run(["openssl", "verify", "-purpose", "sslclient", "-CAfile", str(ca_file), str(certificate_file)],
                    capture_output=True, text=True, check=False)
        if chain.returncode != 0:
            return False
        san = run(["openssl", "x509", "-in", str(certificate_file), "-noout", "-ext", "subjectAltName"],
                  capture_output=True, text=True, check=False)
        if san.returncode != 0:
            return False
        worker_uris = re.findall(r"URI:([^,\s]+)", san.stdout)
        bound_uris = [uri for uri in worker_uris if uri.startswith("spiffe://computecloud/workers/")]
        return bound_uris == [expected_uri]
    except OSError:
        return False


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ca-file", required=True, type=Path)
    parser.add_argument("--certificate-file", required=True, type=Path)
    parser.add_argument("--worker-id", required=True)
    args = parser.parse_args(argv)
    result = verify(args.ca_file, args.certificate_file, args.worker_id)
    print(json.dumps({"result": "pass" if result else "fail", "worker_id": args.worker_id}, sort_keys=True))
    return 0 if result else 1


if __name__ == "__main__":
    raise SystemExit(main())
