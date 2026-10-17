#!/usr/bin/env python3
"""Read-only inventory preflight. Distinct declared IDs are not host acceptance."""
import argparse
import json
from pathlib import Path
import re


def preflight(manifest):
    reasons=[]
    if manifest.get("schema_version")!="iu-independent-host-inventory-v1":reasons.append("schema_version")
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})",manifest.get("computecloud_commit") or ""):reasons.append("computecloud_commit")
    hosts=manifest.get("hosts",[])
    if len(hosts)!=2:reasons.append("two_hosts_required")
    ids=[];workers=[]
    for host in hosts:
        identity=host.get("host_identity_sha256") or ""
        if not re.fullmatch(r"[0-9a-f]{64}",identity):reasons.append("host_identity_missing")
        ids.append(identity);workers.append(host.get("worker_id"))
        for field in ("worker_id","runtime_profile","runtime_version","model_configuration_ref","worker_binary_sha256"):
            if not isinstance(host.get(field),str) or not host[field]:reasons.append(field)
        if not re.fullmatch(r"[0-9a-f]{64}",host.get("worker_binary_sha256") or ""):reasons.append("worker_binary_sha256")
    if len(set(ids))!=len(ids):reasons.append("same_declared_host_identity")
    if len(set(workers))!=len(workers):reasons.append("duplicate_worker_identity")
    return {"schema_version":"iu-host-preflight-v1","identity_fields_complete":not reasons,
            "checks":"distinct_declared_host_identities_only","acceptance_status":"NOT_RUN",
            "real_provider_acceptance":"NOT_RUN","aicloud_acceptance":"NOT_RUN",
            "reasons":sorted(set(reasons))}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest",type=Path)
    args=parser.parse_args()
    result=preflight(json.loads(args.manifest.read_text()))
    print(json.dumps(result,ensure_ascii=False,indent=2))
    return 0 if result["identity_fields_complete"] else 2

if __name__=="__main__":raise SystemExit(main())
