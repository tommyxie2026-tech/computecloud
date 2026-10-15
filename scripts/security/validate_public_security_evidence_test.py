import unittest

from scripts.security import validate_public_security_evidence as validator


def passing_report():
    return {
        "candidate_sha": "a" * 40,
        "server_image_digest": "sha256:" + "1" * 64,
        "worker_image_digest": "sha256:" + "2" * 64,
        "agent_runtime_image_digest": "sha256:" + "3" * 64,
        "worker_config_digest": "sha256:" + "4" * 64,
        "platform": "linux/amd64",
        "worker_id": "worker-template",
        "attempt_id": "public-acceptance-001",
        "secrets_included": False,
        "static_config_checks": {
            "worker_container_config_passed": True,
            "runtime_controller_config_passed": True,
        },
        "target_host_checks": {name: True for name in (
            "target_host_verified",
            "container_isolation", "host_cli_fallback_denied", "workspace_escape_denied",
            "metadata_egress_denied", "host_gateway_egress_denied", "private_egress_denied",
            "unapproved_public_egress_denied", "attempt_gateway_allowed", "cleanup_confirmed",
            "revocation_confirmed", "origin_bypass_denied",
        )},
    }


class PublicSecurityEvidenceTests(unittest.TestCase):
    def test_accepts_complete_target_host_evidence(self):
        self.assertEqual(validator.validate(passing_report()), [])

    def test_rejects_incomplete_or_fixture_only_evidence(self):
        report = passing_report()
        report["target_host_checks"]["target_host_verified"] = False
        report["target_host_checks"].pop("metadata_egress_denied")
        errors = validator.validate(report)
        self.assertTrue(any("target-host" in error for error in errors))
        self.assertTrue(any("metadata_egress_denied" in error for error in errors))

    def test_requires_worker_config_digest(self):
        report = passing_report()
        del report["worker_config_digest"]
        self.assertIn("worker_config_digest is missing or invalid", validator.validate(report))

    def test_static_config_cannot_substitute_for_target_host_checks(self):
        report = passing_report()
        del report["target_host_checks"]
        errors = validator.validate(report)
        self.assertTrue(any("target_host_checks" in error for error in errors))

    def test_requires_static_config_results_separately(self):
        report = passing_report()
        report["static_config_checks"]["worker_container_config_passed"] = False
        self.assertIn("static config check worker_container_config_passed is absent or failed", validator.validate(report))

    def test_rejects_missing_worker_identity(self):
        report = passing_report()
        del report["worker_id"]
        self.assertIn("worker_id is missing or invalid", validator.validate(report))

    def test_rejects_unsafe_worker_identity(self):
        report = passing_report()
        report["worker_id"] = "../worker-a"
        self.assertIn("worker_id is missing or invalid", validator.validate(report))

    def test_rejects_credential_shaped_data_without_echoing_it(self):
        report = passing_report()
        report["debug"] = "Bearer top-secret-token-value"
        errors = validator.validate(report)
        self.assertIn("evidence contains credential-shaped data", errors)


if __name__ == "__main__":
    unittest.main()
