import argparse
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from ci_trigger_delivery import run, submission_key


COMMIT = "a" * 40


class CITriggerDeliveryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = pathlib.Path(self.tmp.name)
        binary = root / "computecloud"
        binary.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        binary.chmod(0o700)
        config = root / "computecloud.yaml"
        config.write_text("client: {}\n", encoding="utf-8")
        spec = root / "job.json"
        spec.write_text(json.dumps({"workspace": {"base_commit": COMMIT}}), encoding="utf-8")
        self.args = argparse.Namespace(
            binary=binary, config=config, spec=spec,
            delivery_id="run-123-job-a", source_commit=COMMIT,
            out=root / "delivery.json", timeout_seconds=30, poll_seconds=1,
        )

    def test_success_records_pinned_result_and_artifact(self):
        result = {
            "job_id": "job-1", "state": "SUCCEEDED", "base_commit": COMMIT,
            "trace_id": "trace-1", "manifest_sha256": "c" * 64,
            "final_artifacts": [{"artifact_id": "artifact-1", "sha256": "b" * 64}],
            "summary": "not included in the delivery report",
        }
        with patch("ci_trigger_delivery.invoke", side_effect=[
            {"job_id": "job-1", "existing": False},
            {"job_id": "job-1", "state": "SUCCEEDED"}, result,
        ]) as invoke:
            self.assertEqual(run(self.args), 0)
        report = json.loads(self.args.out.read_text(encoding="utf-8"))
        self.assertEqual(report["status"], "COMPLETE")
        self.assertEqual(report["submission_key"], submission_key("run-123-job-a", COMMIT))
        self.assertEqual(report["final_artifacts"], [{"artifact_id": "artifact-1", "sha256": "b" * 64}])
        self.assertNotIn("summary", report)
        self.assertEqual(self.args.out.stat().st_mode & 0o777, 0o600)
        self.assertEqual(invoke.call_args_list[0].kwargs["key"], report["submission_key"])

    def test_failed_job_still_delivers_bounded_result(self):
        with patch("ci_trigger_delivery.invoke", side_effect=[
            {"job_id": "job-1", "existing": True},
            {"job_id": "job-1", "state": "FAILED"},
            {"job_id": "job-1", "state": "FAILED", "base_commit": COMMIT, "final_artifacts": []},
        ]):
            self.assertEqual(run(self.args), 1)
        report = json.loads(self.args.out.read_text(encoding="utf-8"))
        self.assertEqual(report["status"], "JOB_FAILED")
        self.assertTrue(report["existing"])

    def test_submission_error_has_no_raw_cli_output(self):
        with patch("ci_trigger_delivery.invoke", side_effect=RuntimeError("SUBMIT_FAILED")):
            self.assertEqual(run(self.args), 1)
        report = json.loads(self.args.out.read_text(encoding="utf-8"))
        self.assertEqual(report["error_code"], "SUBMIT_FAILED")
        self.assertNotIn("job_id", report)

    def test_commit_mismatch_never_submits(self):
        self.args.source_commit = "b" * 40
        with patch("ci_trigger_delivery.invoke") as invoke:
            with self.assertRaisesRegex(ValueError, "base_commit"):
                run(self.args)
            invoke.assert_not_called()

    def test_result_provenance_mismatch_fails_delivery(self):
        with patch("ci_trigger_delivery.invoke", side_effect=[
            {"job_id": "job-1"},
            {"job_id": "job-1", "state": "SUCCEEDED"},
            {"job_id": "job-1", "state": "SUCCEEDED", "base_commit": "b" * 40},
        ]):
            self.assertEqual(run(self.args), 1)
        report = json.loads(self.args.out.read_text(encoding="utf-8"))
        self.assertEqual(report["error_code"], "RESULT_PROVENANCE_MISMATCH")

    def test_timeout_preserves_job_reference_without_cancel(self):
        self.args.timeout_seconds = 1
        with patch("ci_trigger_delivery.invoke", side_effect=[
            {"job_id": "job-1"}, {"job_id": "job-1", "state": "RUNNING"},
        ]) as invoke, patch("ci_trigger_delivery.time.monotonic", side_effect=[0, 2]):
            self.assertEqual(run(self.args), 1)
        report = json.loads(self.args.out.read_text(encoding="utf-8"))
        self.assertEqual(report["job_id"], "job-1")
        self.assertEqual(report["error_code"], "JOB_TIMEOUT")
        self.assertEqual([call.args[2] for call in invoke.call_args_list], ["submit", "get"])

    def test_existing_report_stops_before_submission(self):
        self.args.out.write_text("{}", encoding="utf-8")
        with patch("ci_trigger_delivery.invoke") as invoke:
            with self.assertRaisesRegex(ValueError, "refusing to overwrite"):
                run(self.args)
            invoke.assert_not_called()


if __name__ == "__main__":
    unittest.main()
