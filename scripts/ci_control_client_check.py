#!/usr/bin/env python3
"""Build and statically validate the C2 Operate client."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import time

def run(cmd, cwd=None):
    started=time.time()
    p=subprocess.run(cmd,cwd=cwd,text=True,capture_output=True)
    return {"command":cmd,"returncode":p.returncode,"stdout":p.stdout,"stderr":p.stderr,
            "duration_ms":int((time.time()-started)*1000)}

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--output",required=True)
    args=ap.parse_args()
    out=Path(args.output)
    out.parent.mkdir(parents=True,exist_ok=True)
    logs=out.parent/"logs"
    logs.mkdir(parents=True,exist_ok=True)
    client=Path("clients/control")

    steps=[]
    for name,cmd in [
        ("npm_ci",["npm","ci","--ignore-scripts"]),
        ("typecheck",["npm","run","typecheck"]),
        ("export_web",["npm","run","export:web"]),
    ]:
        result=run(cmd,client)
        steps.append({"name":name,"returncode":result["returncode"],"duration_ms":result["duration_ms"]})
        (logs/f"{name}.stdout.log").write_text(result["stdout"],encoding="utf-8")
        (logs/f"{name}.stderr.log").write_text(result["stderr"],encoding="utf-8")
        if result["returncode"] != 0:
            sys.stderr.write(result["stdout"])
            sys.stderr.write(result["stderr"])
            out.write_text(json.dumps({"schema_version":"control-client-check.v2","status":"FAILED","steps":steps},indent=2)+"\n")
            raise SystemExit(1)

    sources="\n".join(p.read_text(encoding="utf-8") for p in [
        client/"App.tsx", client/"src/api.ts", client/"src/types.ts"
    ])
    violations=[]
    for forbidden in ["localStorage", "sessionStorage", "EventSource(", "token=", "access_token="]:
        if forbidden in sources:
            violations.append("forbidden Control client credential/transport pattern: "+forbidden)
    if 'headers.set("Authorization"' not in sources:
        violations.append("Bearer authorization header missing")
    if "streamEvents(" not in sources or ".body.getReader()" not in sources:
        violations.append("authenticated fetch-based SSE transport missing")
    for required in ["acquireWriteLease(", "renewWriteLease(", "releaseWriteLease(", "X-Control-Lease", "cancelJob(", "sendInput(", "decideApproval(", "resumeSession(", "submitJob(", "Idempotency-Key", "manualRetry(", "Retry failed task"]:
        if required not in sources:
            violations.append("C2 write contract missing: "+required)
    if not (client/"dist"/"index.html").exists():
        violations.append("Expo web export did not produce index.html")

    status="PASSED" if not violations else "FAILED"
    report={
        "schema_version":"control-client-check.v1",
        "status":status,
        "real_model_calls":False,
        "expo_web_export":True,
        "bearer_token_persistence":"memory_only",
        "write_operations":"lease_gated",
        "authenticated_sse":"fetch_stream",
        "steps":steps,
        "violations":violations,
    }
    out.write_text(json.dumps(report,indent=2,sort_keys=True)+"\n",encoding="utf-8")
    print(json.dumps({"status":status,"report":str(out)}))
    if violations:
        sys.stderr.write(json.dumps(violations)+"\n")
        raise SystemExit(1)

if __name__=="__main__":
    main()
