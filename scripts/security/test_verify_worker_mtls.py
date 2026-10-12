import json
from contextlib import redirect_stdout
from io import StringIO
import unittest
from unittest.mock import Mock, patch

from scripts.security import verify_worker_mtls
from scripts.security.verify_worker_mtls import verify


class VerifyWorkerMTLSTests(unittest.TestCase):
    def test_accepts_verified_single_matching_worker_uri(self):
        runner = Mock(side_effect=[Mock(returncode=0, stdout="cert: OK"), Mock(returncode=0, stdout="X509v3 Subject Alternative Name: URI:spiffe://computecloud/workers/worker-a")])
        self.assertTrue(verify("ca.pem", "worker.pem", "worker-a", runner))
        self.assertEqual(runner.call_count, 2)

    def test_rejects_untrusted_certificate_and_does_not_inspect_identity(self):
        runner = Mock(return_value=Mock(returncode=1, stdout="private diagnostic"))
        self.assertFalse(verify("ca.pem", "worker.pem", "worker-a", runner))
        self.assertEqual(runner.call_count, 1)

    def test_rejects_wrong_or_multiple_worker_uri_sans(self):
        for san in [
            "URI:spiffe://computecloud/workers/worker-b",
            "URI:spiffe://computecloud/workers/worker-a, URI:spiffe://computecloud/workers/worker-b",
        ]:
            runner = Mock(side_effect=[Mock(returncode=0, stdout="OK"), Mock(returncode=0, stdout=san)])
            self.assertFalse(verify("ca.pem", "worker.pem", "worker-a", runner))

    def test_result_output_contains_only_worker_id_and_status(self):
        output = StringIO()
        with patch.object(verify_worker_mtls, "verify", return_value=True), redirect_stdout(output):
            code = verify_worker_mtls.main(["--ca-file", "ca.pem", "--certificate-file", "worker.pem", "--worker-id", "worker-a"])
        self.assertEqual(code, 0)
        encoded = output.getvalue()
        self.assertEqual(json.loads(encoded), {"result": "pass", "worker_id": "worker-a"})
        self.assertNotIn("private-key", encoded)
        self.assertNotIn("token", encoded)


if __name__ == "__main__":
    unittest.main()
