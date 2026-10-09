import copy
import unittest

from scripts.validate_v047_integration_results import ValidationError, validate_report


SOURCE_SHA = "a" * 40
IMAGE_DIGEST = "sha256:" + "b" * 64
HASH = "c" * 64


def environment_case(case_id):
    evidence = {
        "ENV01": {
            "environment_provider": "container",
            "host_cli_execution": False,
            "container_created": True,
        },
        "ENV02": {"file_escape_attempted": True, "file_escape_blocked": True},
        "ENV03": {"network_escape_attempted": True, "network_escape_blocked": True},
        "ENV04": {
            "container_crashed": True,
            "controller_restarted": True,
            "no_replay": True,
        },
        "ENV05": {
            "release_requested": True,
            "runtime_cleanup_confirmed": True,
            "environment_cleanup_confirmed": True,
            "residual_container_absent": True,
        },
    }[case_id]
    return {
        "id": case_id,
        "status": "PASSED",
        "source_sha": SOURCE_SHA,
        "attempt_id": "attempt-" + case_id.lower(),
        "image_digest": IMAGE_DIGEST,
        "evidence": evidence,
    }


def integration_case(case_id):
    evidence = {
        "INT01": {
            "authenticated_service_identity": True,
            "trigger_accepted": True,
        },
        "INT02": {
            "duplicate_delivery_attempted": True,
            "same_submission_key": True,
            "same_job_id": True,
            "side_effect_count": 1,
        },
        "INT03": {
            "failed_job_observed": True,
            "failed_delivery_recorded": True,
            "bounded_error_code": True,
        },
        "INT04": {
            "retry_attempted": True,
            "retry_bounded": True,
            "source_provenance_verified": True,
            "artifact_provenance_verified": True,
        },
    }[case_id]
    return {
        "id": case_id,
        "status": "PASSED",
        "source_sha": SOURCE_SHA,
        "delivery_id": "delivery-" + case_id.lower(),
        "job_id": "job-" + case_id.lower(),
        "submission_key_sha256": HASH,
        "spec_sha256": HASH,
        "result_sha256": HASH,
        "manifest_sha256": HASH,
        "evidence": evidence,
    }


def valid_report():
    return {
        "schema_version": "computecloud.v0.4.7.integration-acceptance.v1",
        "status": "PASSED",
        "candidate": {"source_sha": SOURCE_SHA, "built_from_sha": SOURCE_SHA},
        "execution": {
            "environment": "real",
            "target_docker_host": True,
            "trusted_ci": True,
            "started_at": "2026-10-09T01:00:00Z",
            "completed_at": "2026-10-09T01:30:00Z",
        },
        "container": {
            "provider": "container",
            "image_digest": IMAGE_DIGEST,
            "deployed_image_digest": IMAGE_DIGEST,
        },
        "cases": [
            *[environment_case(f"ENV{i:02d}") for i in range(1, 6)],
            *[integration_case(f"INT{i:02d}") for i in range(1, 5)],
        ],
    }


class IntegrationResultsValidationTest(unittest.TestCase):
    def assert_invalid(self, report, fragment):
        with self.assertRaisesRegex(ValidationError, fragment):
            validate_report(report)

    def test_accepts_complete_real_report(self):
        validate_report(valid_report())

    def test_rejects_host_cli_masquerading_as_container(self):
        report = valid_report()
        report["cases"][0]["evidence"]["host_cli_execution"] = True
        self.assert_invalid(report, "ENV01")

    def test_rejects_missing_file_or_network_escape_evidence(self):
        for index, field in ((1, "file_escape_blocked"), (2, "network_escape_blocked")):
            with self.subTest(field=field):
                report = valid_report()
                report["cases"][index]["evidence"][field] = False
                self.assert_invalid(report, field)

    def test_rejects_unconfirmed_cleanup(self):
        report = valid_report()
        report["cases"][4]["evidence"]["environment_cleanup_confirmed"] = False
        self.assert_invalid(report, "cleanup")

    def test_rejects_missing_crash_restart_case(self):
        report = valid_report()
        report["cases"] = [item for item in report["cases"] if item["id"] != "ENV04"]
        self.assert_invalid(report, "ENV04")

    def test_rejects_unauthenticated_trigger(self):
        report = valid_report()
        report["cases"][5]["evidence"]["authenticated_service_identity"] = False
        self.assert_invalid(report, "authenticated_service_identity")

    def test_rejects_duplicate_delivery_side_effects(self):
        report = valid_report()
        report["cases"][6]["evidence"]["side_effect_count"] = 2
        self.assert_invalid(report, "side_effect_count")

    def test_rejects_missing_failed_delivery(self):
        report = valid_report()
        report["cases"][7]["evidence"]["failed_delivery_recorded"] = False
        self.assert_invalid(report, "failed_delivery_recorded")

    def test_rejects_absent_or_mismatched_provenance(self):
        report = valid_report()
        del report["cases"][8]["manifest_sha256"]
        self.assert_invalid(report, "manifest_sha256")

        report = valid_report()
        report["cases"][8]["source_sha"] = "d" * 40
        self.assert_invalid(report, "source_sha")

    def test_rejects_fixture_environment_and_digest_drift(self):
        report = valid_report()
        report["execution"]["environment"] = "fixture"
        self.assert_invalid(report, "environment")

        report = valid_report()
        report["container"]["deployed_image_digest"] = "sha256:" + "d" * 64
        self.assert_invalid(report, "deployed_image_digest")

    def test_rejects_failed_duplicate_or_extra_case(self):
        report = valid_report()
        report["cases"][0]["status"] = "FAILED"
        self.assert_invalid(report, "ENV01")

        report = valid_report()
        report["cases"].append(copy.deepcopy(report["cases"][0]))
        self.assert_invalid(report, "duplicate")

        report = valid_report()
        report["cases"].append(environment_case("ENV01") | {"id": "ENV06"})
        self.assert_invalid(report, "ENV06")

    def test_rejects_credential_shaped_data(self):
        report = valid_report()
        report["execution"]["access_token"] = "redacted"
        self.assert_invalid(report, "credential-shaped")

    def test_rejects_reused_environment_attempt(self):
        report = valid_report()
        report["cases"][1]["attempt_id"] = report["cases"][0]["attempt_id"]
        self.assert_invalid(report, "attempt_id")


if __name__ == "__main__":
    unittest.main()
