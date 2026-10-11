import copy
import pathlib
import re
import tempfile
import unittest

from scripts.test_validate_v047_integration_results import valid_report as integration_report
from scripts.test_validate_v047_performance_results import valid_report as performance_report
from scripts.test_validate_v047_production_results import valid_report as production_report
from scripts.test_validate_v047_runtime_results import valid_report as runtime_report
from scripts.v047_stable_gate import GateError, REQUIRED_CI_JOBS, evaluate_gate, load_supplemental_report


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

    def test_ci_only_allows_release_without_claiming_real_environment_pass(self):
        result = evaluate_gate(SOURCE_SHA, {}, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["release_basis"], "EXACT_SHA_CI")
        self.assertEqual(result["real_environment_status"], "NOT_RUN_OR_PENDING")
        self.assertEqual(result["reports"], {})

    def test_malformed_supplemental_report_is_recorded_as_rejected_without_blocking_ci(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = pathlib.Path(temp_dir) / "runtime.json"
            path.write_text("{not-json", encoding="utf-8")
            report = load_supplemental_report(path)
        result = evaluate_gate(SOURCE_SHA, {"runtime": report}, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["runtime"]["status"], "SUPPLEMENTAL_REJECTED")

    def test_missing_real_report_is_supplemental_and_does_not_block_release(self):
        values = reports()
        del values["runtime"]
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["real_environment_status"], "SUPPLEMENTAL_PRESENT_NOT_GATING")
        self.assertNotIn("runtime", result["reports"])

    def test_failed_or_fixture_supplemental_report_does_not_block_ci_release(self):
        values = reports()
        values["runtime"]["status"] = "BLOCKED_ENV"
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["runtime"]["status"], "SUPPLEMENTAL_REJECTED")

        values = reports()
        values["integration"]["execution"]["environment"] = "fixture"
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["integration"]["status"], "SUPPLEMENTAL_REJECTED")

    def test_rejects_source_sha_mismatch(self):
        values = reports()
        values["performance"]["candidate"]["source_sha"] = "9" * 40
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["performance"]["status"], "SUPPLEMENTAL_REJECTED")

    def test_rejects_missing_case_or_nonzero_correctness(self):
        values = reports()
        values["production"]["cases"].pop()
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["production"]["status"], "SUPPLEMENTAL_REJECTED")

        values = reports()
        values["performance"]["correctness"]["duplicate_attempts"] = 1
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["performance"]["status"], "SUPPLEMENTAL_REJECTED")

    def test_rejects_failed_upgrade_or_manual_rollback(self):
        values = reports()
        values["production"]["cases"][11]["evidence"]["restored_integrity"] = "failed"
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["production"]["status"], "SUPPLEMENTAL_REJECTED")

        values = reports()
        values["production"]["cases"][11]["evidence"]["manual_user_version_change"] = True
        result = evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertTrue(result["release_request_allowed"])
        self.assertEqual(result["reports"]["production"]["status"], "SUPPLEMENTAL_REJECTED")

    def test_rejects_ci_sha_failure_or_missing_job(self):
        ci = ci_evidence()
        ci["source_sha"] = "9" * 40
        self.assert_blocked(reports(), ci, "CI source_sha")

        ci = ci_evidence()
        ci["conclusion"] = "failure"
        self.assert_blocked(reports(), ci, "conclusion")

        ci = ci_evidence()
        ci["event"] = "pull_request"
        self.assert_blocked(reports(), ci, "event")

        ci = ci_evidence()
        del ci["jobs"][REQUIRED_CI_JOBS[0]]
        self.assert_blocked(reports(), ci, REQUIRED_CI_JOBS[0])

    def test_rejects_incomplete_relay_contract(self):
        ci = ci_evidence()
        ci["relay_contracts"]["bounded_metrics"] = False
        self.assert_blocked(reports(), ci, "bounded_metrics")

    def test_release_package_waits_for_every_required_ci_job(self):
        workflow = pathlib.Path(__file__).parents[1] / ".github/workflows/ci.yml"
        content = workflow.read_text(encoding="utf-8")
        match = re.search(r"(?ms)^  package:\n.*?^    needs: \[([^]]+)\]", content)
        self.assertIsNotNone(match, "package job needs list missing")
        package_needs = {name.strip() for name in match.group(1).split(",")}
        missing = sorted(set(REQUIRED_CI_JOBS) - package_needs)
        self.assertEqual(missing, [], f"package job does not wait for required CI jobs: {missing}")
        release_match = re.search(r"(?ms)^  release:\n.*?^    needs: \[([^]]+)\]", content)
        self.assertIsNotNone(release_match, "release job needs list missing")
        self.assertIn("package", {name.strip() for name in release_match.group(1).split(",")})

    def test_does_not_mutate_input(self):
        values = reports()
        original = copy.deepcopy(values)
        evaluate_gate(SOURCE_SHA, values, ci_evidence())
        self.assertEqual(values, original)


if __name__ == "__main__":
    unittest.main()
