#!/usr/bin/env python3
"""Validate UI-04a native mobile security and Expo contract."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import time

def run(cmd, cwd=None):
    started=time.time()
    p=subprocess.run(cmd,cwd=cwd,text=True,capture_output=True)
    return {
        "command":cmd,
        "returncode":p.returncode,
        "stdout":p.stdout,
        "stderr":p.stderr,
        "duration_ms":int((time.time()-started)*1000),
    }

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
        ("expo_config",["npx","expo","config","--type","public","--json"]),
        ("export_ios",["npx","expo","export","--platform","ios","--output-dir","dist-ios"]),
        ("export_android",["npx","expo","export","--platform","android","--output-dir","dist-android"]),
    ]:
        result=run(cmd,client)
        steps.append({"name":name,"returncode":result["returncode"],"duration_ms":result["duration_ms"]})
        (logs/f"{name}.stdout.log").write_text(result["stdout"],encoding="utf-8")
        (logs/f"{name}.stderr.log").write_text(result["stderr"],encoding="utf-8")
        if result["returncode"] != 0:
            sys.stderr.write(result["stdout"])
            sys.stderr.write(result["stderr"])
            out.write_text(json.dumps({
                "schema_version":"control-mobile-check.v1",
                "status":"FAILED",
                "steps":steps,
            },indent=2)+"\n",encoding="utf-8")
            raise SystemExit(1)

    app=json.loads((client/"app.json").read_text(encoding="utf-8"))["expo"]
    package=json.loads((client/"package.json").read_text(encoding="utf-8"))
    mobile=(client/"src/mobile.ts").read_text(encoding="utf-8")
    ui=(client/"App.tsx").read_text(encoding="utf-8")
    sources=mobile+"\n"+ui

    violations=[]
    if app.get("scheme") != "computecloud":
        violations.append("Expo scheme must be computecloud")
    if not app.get("ios",{}).get("bundleIdentifier"):
        violations.append("iOS bundleIdentifier missing")
    if not app.get("android",{}).get("package"):
        violations.append("Android package id missing")
    if "expo-secure-store" not in package.get("dependencies",{}):
        violations.append("expo-secure-store dependency missing")

    for required in [
        "SecureStore.setItemAsync",
        "SecureStore.getItemAsync",
        "AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY",
        "loadOrCreateDeviceID",
        "serverFromConnectURL",
        "subscribeConnectURLs",
        "saveMobileProfile",
        "clearMobileProfile",
    ]:
        if required not in sources:
            violations.append("mobile security contract missing: "+required)

    for forbidden in [
        "AsyncStorage",
        "localStorage",
        "sessionStorage",
        'searchParams.get("token")',
        'searchParams.get("authorization")',
    ]:
        if forbidden in sources:
            violations.append("forbidden mobile credential persistence/deep-link pattern: "+forbidden)

    if 'searchParams.has("token")' not in mobile or 'searchParams.has("authorization")' not in mobile:
        violations.append("deep link must reject token and authorization parameters")
    if 'parsed.protocol !== "computecloud:"' not in mobile:
        violations.append("deep link scheme validation missing")
    if 'parsed.hostname !== "connect"' not in mobile:
        violations.append("deep link connect host validation missing")

    status="PASSED" if not violations else "FAILED"
    report={
        "schema_version":"control-mobile-check.v1",
        "status":status,
        "real_model_calls":False,
        "platforms":["ios","android","web"],
        "native_secret_storage":"expo-secure-store",
        "web_secret_storage":"memory_only",
        "deep_link_scheme":"computecloud",
        "native_bundle_exports":["ios","android"],
        "push_enabled":False,
        "pairing_enabled":False,
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
