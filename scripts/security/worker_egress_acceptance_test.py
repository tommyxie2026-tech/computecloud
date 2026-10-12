import json
import os
import tempfile
import unittest
from unittest import mock

from scripts.security import worker_egress_acceptance as acceptance


class EgressAcceptanceTests(unittest.TestCase):
    def test_rejects_urls_with_credentials_or_non_http_scheme(self):
        for url in ("file:///etc/passwd", "https://user:pass@example.com", "https://example.com/#fragment"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                acceptance.validate_url(url, "destination")

    @mock.patch.object(acceptance, "inspect_platform", return_value="linux/arm64")
    @mock.patch.object(acceptance, "probe")
    def test_accepts_only_model_endpoint_and_writes_sanitized_results(self, probe, _inspect):
        probe.side_effect = [(True, 401), (False, None), (False, None), (False, None), (False, None)]
        with tempfile.TemporaryDirectory() as directory:
            output = os.path.join(directory, "result.json")
            argv = [
                "worker_egress_acceptance.py", "--network", "cc-egress",
                "--image", "agent@sha256:" + "a" * 64, "--attempt-id", "acceptance-001",
                "--platform", "linux/arm64",
                "--allowed-model-url", "https://server.example/v1/", "--metadata-url", "http://169.254.169.254/",
                "--host-gateway-url", "http://172.17.0.1/", "--private-test-url", "http://10.10.10.20/",
                "--unapproved-public-url", "https://example.net/", "--output", output,
            ]
            with mock.patch("sys.argv", argv), mock.patch("builtins.print"):
                self.assertEqual(acceptance.main(), 0)
            with open(output, encoding="utf-8") as stream:
                report = json.load(stream)
            self.assertTrue(report["passed"])
            self.assertFalse(report["secrets_included"])
            self.assertEqual(report["platform"], "linux/arm64")
            self.assertNotIn("url", json.dumps(report))

    @mock.patch.object(acceptance, "inspect_platform", return_value="linux/amd64")
    @mock.patch.object(acceptance, "probe")
    def test_fails_when_unapproved_destination_is_reachable(self, probe, _inspect):
        probe.side_effect = [(True, 401), (False, None), (False, None), (False, None), (True, 200)]
        with tempfile.TemporaryDirectory() as directory:
            argv = [
                "worker_egress_acceptance.py", "--network", "cc-egress",
                "--image", "agent@sha256:" + "b" * 64, "--attempt-id", "acceptance-002",
                "--platform", "linux/amd64",
                "--allowed-model-url", "https://server.example/v1/", "--metadata-url", "http://169.254.169.254/",
                "--host-gateway-url", "http://172.17.0.1/", "--private-test-url", "http://10.10.10.20/",
                "--unapproved-public-url", "https://example.net/", "--output", os.path.join(directory, "result.json"),
            ]
            with mock.patch("sys.argv", argv), mock.patch("builtins.print"):
                self.assertEqual(acceptance.main(), 1)


if __name__ == "__main__":
    unittest.main()
