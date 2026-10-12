import unittest

from scripts.security import validate_public_security_evidence as validator


def passing_report():
    return {
        "candidate_sha": "a" * 40,
        "server_image_digest": "sha256:" + "1" * 64,
        "worker_image_digest": "sha256:" + "2" * 64,
        "agent_runtime_image_digest": "sha256:" + "3" * 64,
        "platform": "linux/amd64",
        "attempt_id": "public-acceptance-001",
        "target_host": True,
        "secrets_included": False,
        "results": {name: True for name in (
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
        report["target_host"] = False
        report["results"].pop("metadata_egress_denied")
        errors = validator.validate(report)
        self.assertTrue(any("target-host" in error for error in errors))
        self.assertTrue(any("metadata_egress_denied" in error for error in errors))

    def test_rejects_credential_shaped_data_without_echoing_it(self):
        report = passing_report()
        report["debug"] = "Bearer top-secret-token-value"
        errors = validator.validate(report)
        self.assertIn("evidence contains credential-shaped data", errors)


if __name__ == "__main__":
    unittest.main()
