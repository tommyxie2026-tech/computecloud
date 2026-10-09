import copy
import unittest

from scripts.test_validate_v047_integration_results import valid_report as integration_report
from scripts.test_validate_v047_performance_results import valid_report as performance_report
from scripts.test_validate_v047_production_results import valid_report as production_report
from scripts.test_validate_v047_runtime_results import valid_report as runtime_report
from scripts.v047_stable_gate import GateError, REQUIRED_CI_JOBS, evaluate_gate


SOURCE_SHA = "a" * 40


def reports():
    return {
        "runtime": runtime_report(),
        "integration": integration_report(),
        "production": production_report(),
        "performance": performance_report(),
    }


def ci_evidence():
    return {
        "schema_version": "computecloud.v0.4.7.ci-evidence.v1",
        "source_sha": SOURCE_SHA,
        "workflow_run_id": 123456,
        "workflow_url": "https://github.com/example/computecloud/actions/runs/123456",
        "event": "push",
        "status": "completed",
        "conclusion": "success",
        "jobs": {name: "success" for name in REQUIRED_CI_JOBS},
        "relay_contracts": {
            "control_bulk_isolation": True,
            "bounded_metrics": True,
            "operations_runbook": True,
        },
    }


class StableGateTest(unittest.TestCase):
    def assert_blocked(self, report_set, ci, fragment):
        with self.assertRaisesRegex(GateError, fragment):
            evaluate_gate(SOURCE_SHA, report_set, ci)

    def test_accepts_all_real_reports_and_exact_successful_ci(self):
        result = evaluate_gate(SOURCE_SHA, reports(), ci_evidence())
        self.assertEqual(result["status"], "PASSED")
        self.assertEqual(result["candidate_sha"], SOURCE_SHA)
        self.assertEqual(set(result["reports"]), {"runtime", "integration", "production", "performance"})

    def test_rejects_missing_report(self):
        values = reports()
        del values["runtime"]
        self.assert_blocked(values, ci_evidence(), "runtime")

    def test_rejects_nonpass_or_fixture_scope(self):
        values = reports()
        values["runtime"]["status"] = "BLOCKED_ENV"
        self.assert_blocked(values, ci_evidence(), "PASSED")

        values = reports()
        values["integration"]["execution"]["environment"] = "fixture"
        self.assert_blocked(values, ci_evidence(), "environment")

    def test_rejects_source_sha_mismatch(self):
        values = reports()
        values["performance"]["candidate"]["source_sha"] = "9" * 40
        self.assert_blocked(values, ci_evidence(), "source_sha")

    def test_rejects_missing_case_or_nonzero_correctness(self):
        values = reports()
        values["production"]["cases"].pop()
        self.assert_blocked(values, ci_evidence(), "VAL12")

        values = reports()
        values["performance"]["correctness"]["duplicate_attempts"] = 1
        self.assert_blocked(values, ci_evidence(), "duplicate_attempts")

    def test_rejects_failed_upgrade_or_manual_rollback(self):
        values = reports()
        values["production"]["cases"][11]["evidence"]["restored_integrity"] = "failed"
        self.assert_blocked(values, ci_evidence(), "restored_integrity")

        values = reports()
        values["production"]["cases"][11]["evidence"]["manual_user_version_change"] = True
        self.assert_blocked(values, ci_evidence(), "manual_user_version_change")

    def test_rejects_ci_sha_failure_or_missing_job(self):
        ci = ci_evidence()
        ci["source_sha"] = "9" * 40
        self.assert_blocked(reports(), ci, "CI source_sha")

        ci = ci_evidence()
        ci["conclusion"] = "failure"
        self.assert_blocked(reports(), ci, "conclusion")

        ci = ci_evidence()
        del ci["jobs"][REQUIRED_CI_JOBS[0]]
        self.assert_blocked(reports(), ci, REQUIRED_CI_JOBS[0])

    def test_rejects_incomplete_relay_contract(self):
        ci = ci_evidence()
        ci["relay_contracts"]["bounded_metrics"] = False
        self.assert_blocked(reports(), ci, "bounded_metrics")

    def test_does_not_mutate_input(self):
        values = reports()
        original = copy.deepcopy(values)
        evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertEqual(values, original)


if __name__ == "__main__":
    unittest.main()
