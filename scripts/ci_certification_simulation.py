#!/usr/bin/env python3
"""Record repeatable release simulations without claiming real-host acceptance."""

import json
import os
import pathlib
import subprocess
import sys


EXPECTED = {
    ("github.com/tommyxie2026-tech/computecloud/internal/store", "TestReleaseV13ToV16AndSnapshotRestore"): "upgrade_restore_fixture",
    ("github.com/tommyxie2026-tech/computecloud/internal/maintenance", "TestOfflineBackupRestoresDatabaseAndArtifacts"): "offline_backup_fixture",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestJobHTTPMCPAndTwoWorkerStrategies"): "two_worker_same_runner",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestTwoWorkersLifecycle"): "two_worker_lifecycle_same_runner",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestRelayJobRestartRecovery"): "relay_loopback_recovery",
    ("github.com/tommyxie2026-tech/computecloud/internal/workspace", "TestCachePerformanceReport"): "cache_provider_fixture",
}


def main() -> int:
    out = pathlib.Path("dist/certification-simulation")
    out.mkdir(parents=True, exist_ok=True)
    command = [
        "go", "test", "-json", "-race", "-count=1",
        "./internal/store", "./internal/maintenance", "./internal/server", "./internal/workspace",
        "-run", "^(TestReleaseV13ToV16AndSnapshotRestore|TestOfflineBackupRestoresDatabaseAndArtifacts|TestJobHTTPMCPAndTwoWorkerStrategies|TestTwoWorkersLifecycle|TestRelayJobRestartRecovery|TestCachePerformanceReport)$",
    ]
    env = dict(os.environ)
    env["COMPUTECLOUD_CACHE_BENCHMARK"] = "1"
    run = subprocess.run(command, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (out / "go-test.jsonl").write_text(run.stdout)

    results = {}
    cache = None
    for line in run.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        key = (event.get("Package"), event.get("Test"))
        if key in EXPECTED and event.get("Action") in {"pass", "fail", "skip"}:
            results[EXPECTED[key]] = event["Action"]
        if key in EXPECTED and event.get("Action") == "output" and "CACHE_BENCHMARK " in event.get("Output", ""):
            cache = json.loads(event["Output"].split("CACHE_BENCHMARK ", 1)[1])

    ratio = cache.get("warm_over_cold_p50") if cache else None
    cache_gate = (
        cache is not None
        and cache.get("scope") == "provider_fixture_not_job_benchmark"
        and cache.get("cold_samples", 0) >= 10
        and cache.get("warm_samples", 0) >= 9
        and cache.get("concurrent_cache_hits") == 10
        and isinstance(ratio, (float, int))
        and ratio <= 0.4
    )
    passed = run.returncode == 0 and len(results) == len(EXPECTED) and all(v == "pass" for v in results.values()) and cache_gate
    report = {
        "status": "PASSED" if passed else "FAILED",
        "scope": "CI simulation on one hosted runner; not independent Linux hosts or real Runtime/NAT/24h acceptance",
        "source_sha": os.environ.get("GITHUB_SHA", "local"),
        "real_hosts": 0,
        "real_model_calls": 0,
        "real_upgrade": False,
        "cases": results,
        "cache": cache,
        "cache_p50_target_met_in_fixture": cache_gate,
        "command": command,
    }
    (out / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report, sort_keys=True))
    if not passed:
        failed = {name: results.get(name, "missing") for name in EXPECTED.values() if results.get(name) != "pass"}
        print(f"::error title=Certification simulation failed::cases={json.dumps(failed, sort_keys=True)} cache_p50_ratio={ratio} cache_gate={cache_gate} go_exit={run.returncode}")
        print(run.stdout[-12000:])
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
