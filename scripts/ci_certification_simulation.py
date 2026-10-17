#!/usr/bin/env python3
"""Record repeatable release simulations without claiming real-host acceptance."""

import argparse
import math
import tarfile
import tempfile
import json
import os
import pathlib
import subprocess
import sys


EXPECTED = {
    ("github.com/tommyxie2026-tech/computecloud/internal/workspace", "TestTreeDigestBoundedScratchMemory"): "cache_digest_scratch_memory",
    ("github.com/tommyxie2026-tech/computecloud/internal/store", "TestReleaseV13ToV17AndSnapshotRestore"): "upgrade_restore_fixture",
    ("github.com/tommyxie2026-tech/computecloud/internal/maintenance", "TestOfflineBackupRestoresDatabaseAndArtifacts"): "offline_backup_fixture",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestJobHTTPMCPAndTwoWorkerStrategies"): "two_worker_same_runner",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestTwoWorkersLifecycle"): "two_worker_lifecycle_same_runner",
    ("github.com/tommyxie2026-tech/computecloud/internal/server", "TestRelayJobRestartRecovery"): "relay_loopback_recovery",
    ("github.com/tommyxie2026-tech/computecloud/internal/workspace", "TestCachePerformanceReport"): "cache_provider_fixture",
}


def cache_gate(cache):
    """Recompute the existing 40% threshold from finite, positive raw samples."""
    if not isinstance(cache, dict) or cache.get("scope") != "provider_fixture_not_job_benchmark":
        return False
    arrays = [cache.get(key) for key in ("cold_ms", "warm_ms", "concurrent_ms")]
    for values, minimum in zip(arrays, (10, 9, 10)):
        if not isinstance(values, list) or len(values) < minimum or any(
                type(v) not in (int, float) or not math.isfinite(v) or v <= 0 for v in values):
            return False
    cold, warm, concurrent = arrays
    if (cache.get("cold_samples") != len(cold) or cache.get("warm_samples") != len(warm)
            or cache.get("concurrent_attempts") != 10 or cache.get("concurrent_cache_hits") != 10
            or len(concurrent) != 10):
        return False
    cold_p50, warm_p50 = sorted(cold)[len(cold)//2], sorted(warm)[len(warm)//2]
    ratio = warm_p50 / cold_p50
    return (cache.get("cold_p50_ms") == cold_p50 and cache.get("warm_p50_ms") == warm_p50
            and type(cache.get("warm_over_cold_p50")) in (int, float)
            and math.isclose(cache["warm_over_cold_p50"], ratio, rel_tol=1e-12)
            and ratio <= 0.4)


def comparison_report(rows, rounds):
    expected = {(i, arm) for i in range(rounds) for arm in ("baseline", "candidate")}
    complete = (len(rows) == len(expected) and {(r["round"], r["arm"]) for r in rows} == expected
                and all(r["exit_code"] == 0 and r["test_action"] == "pass" and r.get("cache") for r in rows))
    ratios = {arm: [r["cache"].get("warm_over_cold_p50") for r in rows
                    if r["arm"] == arm and r.get("cache")] for arm in ("baseline", "candidate")}
    return {"complete": bool(complete), "rounds": rows, "fixed_round_count": rounds,
            "candidate_passed": bool(complete and all(cache_gate(r["cache"]) for r in rows if r["arm"] == "candidate")),
            "baseline_ratios": ratios["baseline"], "candidate_ratios": ratios["candidate"],
            "policy": "all fixed candidate rounds must meet <=0.40; baseline never waives candidate failure"}


def parse_events(raw):
    results, cache = {}, None
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        key = (event.get("Package"), event.get("Test"))
        if key in EXPECTED and event.get("Action") in {"pass", "fail", "skip"}:
            results[EXPECTED[key]] = event["Action"]
        if key in EXPECTED and event.get("Action") == "output" and "CACHE_BENCHMARK " in event.get("Output", ""):
            cache = json.loads(event["Output"].split("CACHE_BENCHMARK ", 1)[1])
    return results, cache


def baseline_ref(value):
    # New tag creation sends an all-zero before SHA rather than a Git object.
    return "HEAD^" if not value or value == "0" * 40 else value


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-ref", default=os.environ.get("BASELINE_REF", "HEAD^"))
    args = parser.parse_args()
    out = pathlib.Path("dist/certification-simulation").resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = pathlib.Path.cwd()
    candidate_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    baseline_sha = subprocess.check_output(["git", "rev-parse", "--verify", baseline_ref(args.baseline_ref) + "^{commit}"], text=True).strip()
    correctness_command = [
        "go", "test", "-json", "-race", "-count=1",
        "./internal/store", "./internal/maintenance", "./internal/server", "./internal/workspace",
        "-run", "^(TestReleaseV13ToV17AndSnapshotRestore|TestOfflineBackupRestoresDatabaseAndArtifacts|TestJobHTTPMCPAndTwoWorkerStrategies|TestTwoWorkersLifecycle|TestRelayJobRestartRecovery|TestTreeDigestBoundedScratchMemory)$",
    ]
    performance_command = ["go", "test", "-json", "-count=1", "./internal/workspace", "-run", "^TestCachePerformanceReport$"]
    env = dict(os.environ, COMPUTECLOUD_CACHE_BENCHMARK="1")
    correctness = subprocess.run(correctness_command, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    (out / "correctness.jsonl").write_text(correctness.stdout)
    results, _ = parse_events(correctness.stdout)
    rows = []
    # Use identical measurement code in both source trees, retaining baseline production code.
    with tempfile.TemporaryDirectory(prefix="computecloud-certification-") as temporary:
        temporary = pathlib.Path(temporary)
        archive = temporary / "baseline.tar"
        baseline = temporary / "baseline"
        baseline.mkdir()
        subprocess.run(["git", "archive", "--format=tar", "--output", str(archive), baseline_sha], check=True)
        with tarfile.open(archive) as bundle:
            bundle.extractall(baseline, filter="data")
        benchmark = pathlib.Path("internal/workspace/cache_benchmark_test.go")
        (baseline / benchmark).write_bytes((root / benchmark).read_bytes())
        for i in range(3):
            # Fixed AB/BA/AB order; never retry until a desired performance value appears.
            for arm in (("baseline", "candidate") if i % 2 == 0 else ("candidate", "baseline")):
                run = subprocess.run(performance_command, cwd=baseline if arm == "baseline" else root,
                                     env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
                logfile = f"{arm}-{i}.jsonl"
                (out / logfile).write_text(run.stdout)
                observed, cache = parse_events(run.stdout)
                rows.append({"round": i, "arm": arm, "source_sha": baseline_sha if arm == "baseline" else candidate_sha,
                             "exit_code": run.returncode, "test_action": observed.get("cache_provider_fixture", "missing"),
                             "cache": cache, "log": logfile})
                print(json.dumps(rows[-1]), flush=True)
    comparison = comparison_report(rows, 3)
    results["cache_provider_fixture"] = "pass" if comparison["candidate_passed"] else "fail"
    passed = (correctness.returncode == 0 and len(results) == len(EXPECTED)
              and all(v == "pass" for v in results.values()) and comparison["candidate_passed"])
    report = {
        "status": "PASSED" if passed else "FAILED",
        "scope": "CI simulation on one hosted runner; not independent Linux hosts or real Runtime/NAT/24h acceptance",
        "source_sha": candidate_sha,
        "baseline_sha": baseline_sha,
        "benchmark_overlay": "candidate cache_benchmark_test.go used in both trees",
        "comparison": comparison,
        "real_hosts": 0,
        "real_model_calls": 0,
        "real_upgrade": False,
        "cases": results,
        "cache": next((r["cache"] for r in reversed(rows) if r["arm"] == "candidate"), None),
        "cache_p50_target_met_in_fixture": comparison["candidate_passed"],
        "correctness_command": correctness_command,
        "performance_command": performance_command,
    }
    (out / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report, sort_keys=True))
    if not passed:
        failed = {name: results.get(name, "missing") for name in EXPECTED.values() if results.get(name) != "pass"}
        print(f"::error title=Certification simulation failed::cases={json.dumps(failed, sort_keys=True)} candidate_ratios={comparison['candidate_ratios']} baseline_ratios={comparison['baseline_ratios']} comparison_complete={comparison['complete']} correctness_exit={correctness.returncode}")
        print(correctness.stdout[-12000:])
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
