#!/usr/bin/env python3
"""Run IU simulation and native protocol gates; never invoke a model provider."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid

PREFIX = "github.com/tommyxie2026-tech/computecloud/"
SERVER_TESTS = [
    "TestIUBackupRestoresAcceptedImportWithSubmissionsDisabled",
    "TestInvalidationArtifactDependenciesRemainUncertain", "TestImportRejectsSourceRoleAndOrderChange",
    "TestVerificationCannotPromoteUncoveredRecords",
    "TestVerificationRejectsStaleEvidence",
    "TestVerificationRechecksManagedKeyAndProfileChanges",
    "TestVerificationEvaluationReferencesCannotBeForged",
    "TestGraphSourceReadIsBoundedDurableAndIdempotent",
    "TestGraphSourceReadRejectsWrongPlanOrUTF8Boundary",
    "TestReuseSurvivesRestartWithoutNewAttempt",
    "TestReusePinsOriginArtifacts",
    "TestIncrementalEqualsFullRecompute",
    "TestReuseChangedInputFallsBackToNewTask",
    "TestGraphIncrementalOrdersMatchesFullRecompute",
    *["TestGraphIncrementalOrdersMatchesFullRecompute/"+name for name in ("rule_change","duplicate","different_customer","two_hop_counterevidence")],
    "TestInvalidationUsesFrozenContextForLegacyOmittedSources",
    "TestImportRechecksRevokedGrantAtCommit",
    "TestImportFreezesSnapshotAndPinsOrigin",
    "TestImportRejectsStaleRevisionAndChangedContext",
    "TestImportIdempotencyExpiryAndGCFencing",
    "TestImportRequiresExplicitAuthorityAndGuard",
    "TestGraphInvalidationSurvivesRestart",
    "TestInvalidationBlocksLateAdmissionAndPass",
    "TestInvalidationRejectsUnauthorizedOrStaleRevision",
    "TestInvalidationRejectsRevokedManagedKeyAndRollsBack",
    "TestInvalidationLinearizesWithPass",
    "TestInvalidationProjectionBatchesLeaveDurableBacklog",
    "TestInvalidationRejectsReplanUsingOldEvaluation",
    "TestGraphTwoWorkerMemoryJoinAndTrustedCommandFlow",
    *["TestGraphTwoWorkerMemoryJoinAndTrustedCommandFlow/" + name for name in (
        "discount_1000", "revised_discount_2000", "stale_rule_rejected", "missing_global_rule_rejected")],
    "TestGraphMemoryPendingSurvivesServerRestartWithSubmissionDisabled",
    "TestGraphMemoryPermanentRejectionTerminatesGraph",
    "TestGraphQueuePressureDefersActivationWithoutBlockingController",
    "TestGraphInputBindingUsesConsumerPortAndRetainsProducerIdentity",
    "TestGraphGoalRequiresAuthorizedBusinessEvaluation",
    "TestGraphCancelWaitingEvaluationPreventsLatePass",
    "TestGraphEvaluationRejectsExpiredGoalBeforeReconciler",
    "TestGraphBusinessFailureCanPublishGuardedNewGeneration",
    "TestGraphEvaluationRequiresSeparateAuthority",
    "TestGraphSubmitNamespacesAndIdempotency",
]
REQUIRED_TESTS = tuple(
    [PREFIX + "internal/server::" + name for name in SERVER_TESTS]
    + [PREFIX + "internal/graph::" + name for name in (
        "TestInvalidationRetainsConflictHistory", "TestOutputBindsAllFrozenSourcesWithoutTrustingModelClaims", "TestFrozenSourceLimitFailsInsteadOfDroppingProvenance", "TestSourceBindingRejectsMalformedCandidateBeforeUnion", "TestImportedOriginTravelsInsideModelContext", "TestModelCannotMintImportedOrigin", "TestVerifierTimeoutRemainsUnknown", "TestEmpiricalContractDoesNotInventCertainty",
        "TestVerificationProjectionRetainsConflictEvidence", "TestCountUnavailableAndWrapperCoverageStayUnknown", "TestContextPolicyV4RejectsUnsupportedTokensAndAttachments", "TestTokenizerChangeRequiresRecount", "TestRequiredContextCannotBeTruncated",
        "TestCrossChunkReferenceNeedsEvidence", "TestModelSwitchDoesNotReuseTokenIDs", "TestRuleChangeInvalidatesDependentNodes", "TestImportRejectsInvalidOrIncompleteClosure", "TestImportPreservesOriginAndNamespace",
        "TestImportGraphVersionAndCanonicalBindings", "TestCompileContextPreservesSourceAndRequiredRules", "TestContextNeverSilentlyTruncates",
        "TestReconstructSourceValidatesOverlapsAndCoverage", "TestMemoryDeltaRequiresFrozenViewAndCannotSelfVerify",
        "TestMemoryMergeConflictsAndDeduplicates", "TestMemoryInvalidationPropagatesWithoutChangingHistory",
        "TestApplyDeltaPreservesConflictsAndInheritedInvalidity", "TestOutputSchemaRebasesFrozenLocalDefinitions")]
    + [PREFIX + "internal/store::TestIUReleaseV20UpgradeAndUntouchedRollbackSnapshot",
       PREFIX + "internal/store::TestMigrationFailureAtomicAndFutureSchema",
       PREFIX + "internal/maintenance::TestOfflineBackupRestoresDatabaseAndArtifacts",
       PREFIX + "cmd/computecloud::TestJobGraphCompileRequiresNoClientConnection",
       PREFIX + "cmd/computecloud::TestJobGraphCLIProposalAcquiresAndReleasesControlLease",
       PREFIX + "internal/aicloudadapter::TestReadOnlyAICloudBundleFreezesExactVersions",
       PREFIX + "internal/aicloudadapter::TestAICloudUnavailabilityRateLimitBudgetAndRedirectNeverFallback",
       PREFIX + "internal/aicloudadapter::TestAICloudPaginationLoopAndMissingDecisionFailClosed",
       PREFIX + "internal/iuexperiment::TestPilotProtocolFreezesTwentyCasesAndBudgets",
       PREFIX + "internal/iuexperiment::TestPilotSimulationExecutesAllArmsWithUnknownProviderCosts",
       PREFIX + "internal/iuexperiment::TestAblationsKeepMemoryAndVerificationEffectsSeparate",
       PREFIX + "internal/iuexperiment::TestDetailedMetricsUseAllSamplesAndUnknownCosts",
       PREFIX + "internal/iuexperiment::TestIndependentVerificationContrastRetainsRejectedSamples", PREFIX + "internal/iuexperiment::TestIncrementalEvidenceKeepsUnknownCostAndRequiresProvenance", PREFIX + "internal/worker::TestGraphWorkerRequiresOriginCapabilityForV2Memory", PREFIX + "internal/worker::TestGraphWorkerRejectsModelOrSerializationDrift", PREFIX + "internal/worker::TestGraphReplayBindingRejectsWorkerDrift", PREFIX + "cmd/iu-experiment::TestCLIProcessTimeoutDrainsWholeGroup",
       PREFIX + "internal/iuexperiment::TestSimulationChecksHealthyAndInjectedFailures",
       PREFIX + "internal/iuexperiment::TestCorrectFinalTotalDoesNotHideCorruptedIntermediateIdentity"]
)
STAGES = {"verifier", "contracts", "native", "cli", "simulation", "build", "processes"}


def build_report(stages, events, simulation, process_report=None):
    critical = [{"test": key, "action": events.get(key, "missing")} for key in REQUIRED_TESTS]
    scenarios = simulation.get("scenarios", [])
    healthy = sum(s.get("injected_fault") == "none" for s in scenarios)
    faults = len(scenarios) - healthy
    pilot=simulation.get("pilot",{})
    pilot_samples=pilot.get("samples",[])
    valid_pilot=(pilot.get("checks_passed") is True and pilot.get("provider_calls")==0
                 and pilot.get("fixture_invocations")==215 and len(pilot_samples)==80
                 and len(pilot.get("protocol",{}).get("samples",[]))==20
                 and all(sum(s.get("arm")==arm for s in pilot_samples)==20 for arm in ("monolithic","iu","iu_no_memory","iu_no_verifier"))
                 and all(s.get("independent_business_pass") is True and not s.get("error_code")
                         and s.get("input_tokens") is None and s.get("reported_cost_usd") is None for s in pilot_samples))
    valid_sim = (simulation.get("real_model_calls") is False and simulation.get("checks_passed") is True
                 and healthy == 8 and faults == 10
                 and all(s.get("check_passed") is True for s in scenarios))
    stage_ok = len(stages) == len(STAGES) and {s["name"] for s in stages} == STAGES and all(
        s["exit_code"] == 0 for s in stages)
    process_report = process_report or {}
    native = next((step.get("evidence", {}) for step in process_report.get("steps", [])
                   if step.get("name") == "native_iu_graphs" and step.get("status") == "PASSED"), {})
    native_cases = native.get("cases", [])
    recovery=native.get("import_recovery",[])
    valid_recovery=(len(recovery)==2 and {c.get("mode") for c in recovery}=={"memory","node_result"}
                    and all(c.get("generation")==2 and c.get("origin_pins",0)>0
                            and c.get("attempts")==c.get("tasks")== (1 if c.get("mode")=="memory" else 0)
                            and c.get("source_output")==c.get("recovered_output")==6000
                            and c.get("attempts_before_restart")==0
                            and "IMPORT_GRANT_NOT_USABLE" in c.get("revoked_denial","")
                            and all(c.get(key) is True for key in ("grant_consumed","server_process_restart","submissions_disabled_recovery",
                                "publication_idempotent","revoked_denied","verification_authorized","submitter_verification_denied","source_read_receipt_preserved","origin_forwarded"))
                            for c in recovery))
    valid_processes = (process_report.get("summary", {}).get("status") == "PASSED"
                       and native.get("real_model_calls") is False
                       and native.get("worker_processes") == 2
                       and len(native_cases) == 4 and any(c.get("server_process_restart") for c in native_cases)
                       and all(native.get("invalidation", {}).get(key) is True for key in (
                           "applied", "server_process_restart", "attempts_unchanged", "unauthorized_denied", "stale_pass_denied"))
                       and native.get("invalidation", {}).get("revision") == 1
                       and native.get("invalidation", {}).get("goal_state") == "FAILED")
    passed = stage_ok and valid_sim and valid_pilot and valid_processes and valid_recovery and all(c["action"] == "pass" for c in critical)
    return {"schema_version": "iu-ci-validation-v1", "status": "PASSED" if passed else "FAILED",
            "real_model_calls": False, "external_acceptance":{"real_provider":"NOT_RUN","aicloud_service":"NOT_RUN","independent_hosts":"NOT_RUN"}, "stages": stages, "required_checks": critical,
            "native_process_flow": native,
            "pilot":pilot, "simulation": {"healthy_scenarios": healthy, "injected_faults": faults, "passed": valid_sim},
            "test_totals": {action: sum(v == action for v in events.values()) for action in ("pass", "fail", "skip")},
            "limitations": ["Deterministic fixtures validate contracts and failure handling, not model intelligence or cost savings.",
                            "Separate Server/two Worker OS processes on one Runner use mock agents; this is not independent-host or real-Provider acceptance."]}


def read_events(paths):
    events = {}
    for path in paths:
        if not path.exists():
            continue
        for line in path.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("Test") and event.get("Action") in ("pass", "fail", "skip"):
                events[event.get("Package", "") + "::" + event["Test"]] = event["Action"]
    return events


def markdown(report, simulation):
    lines = ["# IU CI 模拟与原生执行验收", "", "**状态：" + report["status"] + "**；真实模型调用：0。", "",
             "通过表示模拟与协议检查符合预期，不代表真实模型正确率或成本收益。", "",
             "| 场景 | 注入 | 预期检测 | 检查 | 最终金额 | 提取语义保留 |", "|---|---|---|---|---|---|"]
    for s in simulation.get("scenarios", []):
        sample = s["sample"]
        lines.append(f'| {s["id"]} | {s["injected_fault"]} | {s["expected_detection"]} | '
                     f'{s["check_passed"]} | {sample.get("actual_cents")} | {sample.get("extraction_preserves_original_fields")} |')
    lines += ["", "## 独立 Server/Worker 进程验收", "", "| 场景 | 金额 | 验证 | Goal | Worker | 回执 | Server 重启 |", "|---|---|---|---|---|---|---|"]
    for c in report.get("native_process_flow", {}).get("cases", []):
        lines.append(f'| {c["case"]} | {c["amount"]} | {c["verdict"]} | {c["goal_state"]} | {len(c["workers"])} | {c["admitted_receipts"]} | {c["server_process_restart"]} |')
    lines += ["", "## 原生执行与契约必测项", "", "| 测试 | 结果 |", "|---|---|"]
    lines += [f'| {c["test"].split("::", 1)[-1]} | {c["action"]} |' for c in report["required_checks"]]
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", default="dist/iu-graph")
    args = parser.parse_args()
    output = Path(args.output).resolve()
    run_dir = output / ("run-" + uuid.uuid4().hex[:12])
    run_dir.mkdir(parents=True)
    simulation_path = run_dir / "simulation.json"
    process_report_path = run_dir / "processes/report.json"
    binary = run_dir / "computecloud"
    commands = [
        ("verifier", [sys.executable, "-m", "unittest", "discover", "-s", "examples/iu-graph", "-p", "test_*.py"], 60),
        ("contracts", [args.go, "test", "-json", "-race", "./internal/graph", "./internal/store", "./internal/config",
                       "./internal/iuexperiment", "./internal/aicloudadapter", "./internal/maintenance", "./cmd/iu-experiment", "-count=1", "-timeout=5m"], 330),
        ("native", [args.go, "test", "-json", "-race", "./internal/server", "./internal/worker", "./internal/adapter",
                    "-run", "TestGraph|TestCommandProvider|TestInvalidation|TestImport|TestReuse|TestIncremental|TestVerification|TestIU", "-count=1", "-timeout=5m"], 330),
        ("cli", [args.go, "test", "-json", "./cmd/computecloud", "-run", "TestJobGraphCLI|TestJobGraphCompile", "-count=1", "-timeout=2m"], 150),
        ("simulation", [args.go, "run", "./cmd/iu-experiment", "--simulate", "--output", str(simulation_path)], 120),
        ("build", [args.go, "build", "-o", str(binary), "./cmd/computecloud"], 120),
        ("processes", [sys.executable, "scripts/ci_task_flow.py", "--binary", str(binary), "--iu-graph-check",
                       "--timeout", "90", "--output", str(process_report_path)], 300)]
    stages, logs = [], []
    for name, command, timeout in commands:
        log = run_dir / (name + ".jsonl")
        logs.append(log)
        started = time.monotonic()
        with log.open("wb") as stream:
            try:
                proc = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=stream, stderr=subprocess.STDOUT,
                                        start_new_session=True)
                try:
                    code = proc.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
                    code = 124
            except OSError:
                code = 127
        stages.append({"name": name, "exit_code": code, "wall_ms": round((time.monotonic() - started) * 1000),
                       "log": str(log.relative_to(output)),"command":command})
        print(f"{name}: exit_code={code}", flush=True)
        if code:
            print(log.read_text(errors="replace")[-10000:], flush=True)
    try:
        simulation = json.loads(simulation_path.read_text())
    except (OSError, ValueError):
        simulation = {}
    try:
        process_report = json.loads(process_report_path.read_text())
    except (OSError, ValueError):
        process_report = {}
    report = build_report(stages, read_events(logs), simulation, process_report)
    revision = subprocess.run(["git", "rev-parse", "HEAD"], text=True, capture_output=True, check=False)
    report.update({"commit": revision.stdout.strip(), "created_at": datetime.now(timezone.utc).isoformat(),
                   "simulation_file": str(simulation_path.relative_to(output)),"environment":process_report.get("environment",{})})
    (output / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    summary = markdown(report, simulation)
    (output / "summary.md").write_text(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as stream:
            stream.write(summary)
    print(f'IU validation: {report["status"]}; required={len(REQUIRED_TESTS)}; simulation=18; real_model_calls=0')
    return 0 if report["status"] == "PASSED" else 1


if __name__ == "__main__":
    sys.exit(main())
