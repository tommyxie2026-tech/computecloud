import json
import unittest

from scripts.security.public_ingress_smoke import run


class PublicIngressSmokeTests(unittest.TestCase):
    def test_bounded_checks_pass_without_printing_or_returning_token(self):
        seen = {"rate": 0, "body_size": 0}

        def fake(url, token=None, method="GET", body=None, timeout=10):
            if url.endswith("/v1/capabilities") and token is None:
                return 401, b'{"error":{"code":"AUTH_REQUIRED"}}'
            if url.endswith("/v1/jobs"):
                seen["body_size"] = len(body)
                return 413, b'{"error":{"code":"REQUEST_TOO_LARGE"}}'
            seen["rate"] += 1
            return (429, b'{"error":{"code":"RATE_LIMITED"}}') if seen["rate"] == 3 else (200, b"{}")

        result = run("https://example.invalid", "top-secret", oversized_bytes=50, requester=fake)
        self.assertTrue(result["passed"])
        self.assertNotIn("top-secret", json.dumps(result))
        self.assertEqual(seen["body_size"], 64)

    def test_rejects_http_non_loopback_and_unconfirmed_job_submission(self):
        with self.assertRaises(ValueError):
            run("http://example.invalid", "token", requester=lambda *a, **k: (200, b""))
        with self.assertRaises(ValueError):
            run("https://example.invalid", "token", queue_body=b"{}", requester=lambda *a, **k: (200, b""))

    def test_queue_probe_requires_backpressure_and_never_exposes_token(self):
        calls = []

        def fake(url, token=None, method="GET", body=None, timeout=10):
            calls.append((url, method))
            if token is None:
                return 401, b"{}"
            if url.endswith("/v1/jobs"):
                return 413, b'{"error":{"code":"QUEUE_BACKPRESSURE_GLOBAL"}}'
            return 429, b"{}"

        result = run("https://example.invalid", "private-token", oversized_bytes=1,
                     queue_body=b"{}", confirm_queue_test=True, requester=fake)
        self.assertTrue(result["passed"])
        self.assertEqual(result["queue_saturation"], "pass")
        self.assertNotIn("private-token", json.dumps(result))
        self.assertEqual(sum(url.endswith("/v1/jobs") for url, _ in calls), 2)

    def test_fails_when_errors_echo_token_or_rate_limit_is_missing(self):
        def fake(url, token=None, method="GET", body=None, timeout=10):
            if token is None:
                return 401, b"{}"
            if url.endswith("/v1/jobs"):
                return 413, token.encode()
            return 200, b"{}"

        result = run("https://example.invalid", "secret", oversized_bytes=1,
                     max_rate_probes=2, requester=fake)
        self.assertFalse(result["passed"])
        self.assertEqual(result["oversized"], "fail")
        self.assertEqual(result["rate_limit"], "not_checked")


if __name__ == "__main__":
    unittest.main()
