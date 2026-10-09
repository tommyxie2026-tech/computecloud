import copy
import unittest

from scripts.validate_v047_production_results import ValidationError, validate_report


SOURCE_SHA = "a" * 40
HASH = "b" * 64


def host(role, node_id, machine):
    return {
        "role": role,
        "node_id": node_id,
        "machine_id_sha256": machine * 64,
        "binary_sha256": HASH,
        "source_sha": SOURCE_SHA,
        "os": "Linux",
        "runtime_versions": {
            "codex": {"version": "0.160.1", "sha256": "c" * 64},
            "claude": {"version": "2.1.292", "sha256": "d" * 64},
        } if role == "worker" else {},
        "mcp_version": "mcp-1" if role == "worker" else None,
    }


def case(case_id):
    evidence = {
        "VAL01": {"preflight_verified": True, "independent_workers": True},
        "VAL02": {"real_codex_call": True, "native_final_verified": True, "runtime_report_sha256": HASH, "native_final_sha256": HASH},
        "VAL03": {"real_claude_call": True, "native_final_verified": True, "runtime_report_sha256": HASH, "native_final_sha256": HASH},
        "VAL04": {"real_mcp_call": True, "mcp_result_verified": True, "mcp_report_sha256": HASH, "mcp_result_sha256": HASH},
        "VAL05": {"cross_worker_execution": True, "worker_ids": ["worker-a", "worker-b"]},
        "VAL06": {"worker_sigkill": True, "reconciled": True, "no_duplicate_attempt": True},
        "VAL07": {"server_sigkill": True, "reconciled": True, "no_state_loss": True},
        "VAL08": {"network_loss": True, "network_recovered": True, "event_watermark_preserved": True},
        "VAL09": {"duration_seconds": 86400, "lease_and_events_continuous": True},
        "VAL10": {"artifact_access_control_verified": True, "artifact_hashes_verified": True, "artifact_manifest_sha256": HASH},
        "VAL11": {"capacity_completed": 20, "capacity_failed": 0, "sqlite_integrity": "ok"},
        "VAL12": {
            "v13_snapshot_sha256": HASH,
            "v13_snapshot_user_version": 13,
            "v13_snapshot_integrity": "ok",
            "upgraded_user_version": 16,
            "migrated_integrity": "ok",
            "restored_snapshot_sha256": HASH,
            "restored_user_version": 13,
            "restored_integrity": "ok",
            "rollback_method": "restore_snapshot",
            "manual_user_version_change": False,
            "v13_binary_sha256": "e" * 64,
            "v16_binary_sha256": "f" * 64,
        },
    }[case_id]
    item = {
        "id": case_id,
        "status": "PASSED",
        "source_sha": SOURCE_SHA,
        "task_ids": ["task-" + case_id.lower()],
        "attempt_ids": ["attempt-" + case_id.lower()],
        "evidence": evidence,
    }
    if case_id in {"VAL06", "VAL07", "VAL08"}:
        item["fault_started_at"] = "2026-10-09T02:00:00Z"
        item["recovered_at"] = "2026-10-09T02:05:00Z"
    return item


def valid_report():
    return {
        "schema_version": "computecloud.v0.4.7.production-acceptance.v1",
        "status": "PASSED",
        "candidate": {"source_sha": SOURCE_SHA, "built_from_sha": SOURCE_SHA},
        "execution": {"environment": "real", "started_at": "2026-10-08T00:00:00Z", "completed_at": "2026-10-09T03:00:00Z"},
        "hosts": [host("server", "server", "1"), host("worker", "worker-a", "2"), host("worker", "worker-b", "3")],
        "correctness": {
            "duplicate_attempts": 0,
            "stale_generation_accepts": 0,
            "invalid_artifacts": 0,
            "lost_confirmed_events": 0,
            "terminal_state_regressions": 0,
            "unauthorized_accesses": 0,
        },
        "cases": [case(f"VAL{i:02d}") for i in range(1, 13)],
    }


class ProductionResultsValidationTest(unittest.TestCase):
    def assert_invalid(self, report, fragment):
        with self.assertRaisesRegex(ValidationError, fragment):
            validate_report(report)

    def test_accepts_complete_real_report(self):
        validate_report(valid_report())

    def test_rejects_equal_worker_machine_ids_or_fewer_workers(self):
        report = valid_report()
        report["hosts"][2]["machine_id_sha256"] = report["hosts"][1]["machine_id_sha256"]
        self.assert_invalid(report, "machine")
        report = valid_report()
        report["hosts"].pop()
        self.assert_invalid(report, "two Workers")

    def test_rejects_candidate_sha_mismatch(self):
        report = valid_report()
        report["hosts"][1]["source_sha"] = "9" * 40
        self.assert_invalid(report, "source_sha")

    def test_rejects_missing_runtime_or_mcp_version(self):
        report = valid_report()
        del report["hosts"][1]["runtime_versions"]["claude"]
        self.assert_invalid(report, "Codex/Claude")
        report = valid_report()
        report["hosts"][1]["mcp_version"] = None
        self.assert_invalid(report, "MCP")

    def test_rejects_absent_cross_worker_execution(self):
        report = valid_report()
        report["cases"][4]["evidence"]["worker_ids"] = ["worker-a"]
        self.assert_invalid(report, "VAL05")

    def test_rejects_missing_fault_timeline_or_short_long_run(self):
        report = valid_report()
        del report["cases"][5]["fault_started_at"]
        self.assert_invalid(report, "fault_started_at")
        report = valid_report()
        report["cases"][8]["evidence"]["duration_seconds"] = 86399
        self.assert_invalid(report, "duration_seconds")

    def test_rejects_nonzero_correctness_counter(self):
        report = valid_report()
        report["correctness"]["duplicate_attempts"] = 1
        self.assert_invalid(report, "duplicate_attempts")

    def test_rejects_manual_user_version_rollback(self):
        report = valid_report()
        report["cases"][11]["evidence"]["manual_user_version_change"] = True
        self.assert_invalid(report, "manual_user_version_change")

    def test_rejects_bad_upgrade_or_restored_snapshot(self):
        report = valid_report()
        report["cases"][11]["evidence"]["upgraded_user_version"] = 15
        self.assert_invalid(report, "upgraded_user_version")
        report = valid_report()
        report["cases"][11]["evidence"]["restored_snapshot_sha256"] = "0" * 64
        self.assert_invalid(report, "restored_snapshot")

    def test_rejects_missing_failed_duplicate_or_extra_case(self):
        report = valid_report()
        report["cases"] = [item for item in report["cases"] if item["id"] != "VAL08"]
        self.assert_invalid(report, "VAL08")
        report = valid_report()
        report["cases"].append(copy.deepcopy(report["cases"][0]))
        self.assert_invalid(report, "duplicate")
        report = valid_report()
        report["cases"][0]["status"] = "FAILED"
        self.assert_invalid(report, "VAL01")


if __name__ == "__main__":
    unittest.main()
