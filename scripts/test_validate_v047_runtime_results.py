import copy
import unittest

from scripts.validate_v047_runtime_results import ValidationError, validate_report


SOURCE_SHA = "a" * 40
CODEX_DIGEST = "sha256:" + "b" * 64
CLAUDE_DIGEST = "sha256:" + "c" * 64


def case(case_id, profile="codex_http", digest=CODEX_DIGEST):
    evidence = {
        "RT01": {"start_created": True, "inspect_observed": True, "stop_confirmed": True},
        "RT02": {"cancellation_requested": True, "terminal_cancelled": True},
        "RT03": {"timeout_configured": True, "terminal_timed_out": True},
        "RT04": {"provider_error_observed": True, "terminal_provider_error": True},
        "RT05": {"event_continuity_verified": True},
        "RT06": {"artifact_provenance_verified": True},
        "RT07": {"service_restarted": True, "no_replay": True, "cleanup_confirmed": True},
        "RT08": {
            "native_budget_capability": "budget_claude_estimated_usd_v1",
            "budget_exhausted": True,
            "terminal_budget_exhausted": True,
        },
        "RT09": {
            "finite_goal_budget": True,
            "one_in_flight": True,
            "usage_settled_before_next": True,
            "exhausted_blocks_next": True,
        },
    }[case_id]
    return {
        "id": case_id,
        "status": "PASSED",
        "profile": profile,
        "task_id": "task-" + case_id.lower(),
        "attempt_id": "attempt-" + case_id.lower(),
        "source_sha": SOURCE_SHA,
        "image_digest": digest,
        "native_final": {
            "present": True,
            "sha256": "sha256:" + "d" * 64,
            "terminal_state": "SUCCEEDED" if case_id not in {"RT02", "RT03", "RT04", "RT08"} else "EXPECTED_FAILURE",
        },
        "events": {"first_sequence": 1, "last_sequence": 3, "contiguous": True},
        "usage": {
            "complete": True,
            "input_tokens": 10,
            "output_tokens": 5,
            "total_tokens": 15,
            "cost_usd": 0.001,
        },
        "evidence": evidence,
    }


def valid_report():
    cases = [case(f"RT{i:02d}") for i in range(1, 8)]
    cases.append(case("RT08", "claude_http", CLAUDE_DIGEST))
    cases.append(case("RT09", "claude_http", CLAUDE_DIGEST))
    return {
        "schema_version": "computecloud.v0.4.7.runtime-acceptance.v1",
        "status": "PASSED",
        "candidate": {"source_sha": SOURCE_SHA, "built_from_sha": SOURCE_SHA},
        "execution": {
            "environment": "real",
            "real_model_calls": True,
            "authorized_account": True,
            "service_endpoint_scheme": "unix",
            "started_at": "2026-10-09T00:00:00Z",
            "completed_at": "2026-10-09T00:10:00Z",
        },
        "runtimes": [
            {
                "profile": "codex_http",
                "version": "0.160.1",
                "image_digest": CODEX_DIGEST,
                "deployed_image_digest": CODEX_DIGEST,
                "real_provider": True,
            },
            {
                "profile": "claude_http",
                "version": "2.1.292",
                "image_digest": CLAUDE_DIGEST,
                "deployed_image_digest": CLAUDE_DIGEST,
                "real_provider": True,
            },
        ],
        "cases": cases,
    }


class RuntimeResultsValidationTest(unittest.TestCase):
    def assert_invalid(self, report, fragment):
        with self.assertRaisesRegex(ValidationError, fragment):
            validate_report(report)

    def test_accepts_complete_real_report(self):
        validate_report(valid_report())

    def test_rejects_fixture_model_calls(self):
        report = valid_report()
        report["execution"]["real_model_calls"] = False
        self.assert_invalid(report, "real_model_calls")

    def test_rejects_missing_native_final(self):
        report = valid_report()
        report["cases"][0]["native_final"]["present"] = False
        self.assert_invalid(report, "native_final")

    def test_rejects_source_sha_mismatch(self):
        report = valid_report()
        report["cases"][0]["source_sha"] = "e" * 40
        self.assert_invalid(report, "source_sha")

    def test_rejects_deployed_image_digest_mismatch(self):
        report = valid_report()
        report["runtimes"][0]["deployed_image_digest"] = "sha256:" + "e" * 64
        self.assert_invalid(report, "deployed_image_digest")

    def test_rejects_missing_cancel_timeout_or_restart_case(self):
        for missing in ("RT02", "RT03", "RT07"):
            with self.subTest(missing=missing):
                report = valid_report()
                report["cases"] = [item for item in report["cases"] if item["id"] != missing]
                self.assert_invalid(report, missing)

    def test_rejects_incomplete_cost_usage(self):
        report = valid_report()
        report["cases"][-1]["usage"]["complete"] = False
        self.assert_invalid(report, "usage")

    def test_rejects_credential_shaped_field_or_value(self):
        report = valid_report()
        report["execution"]["api_key"] = "redacted"
        self.assert_invalid(report, "credential-shaped")

        report = valid_report()
        report["execution"]["operator_note"] = "Bearer abcdefghijklmnopqrstuvwxyz"
        self.assert_invalid(report, "credential-shaped")

    def test_rejects_failed_case_and_wrong_case_semantics(self):
        report = valid_report()
        report["cases"][1]["status"] = "FAILED"
        self.assert_invalid(report, "RT02")

        report = valid_report()
        report["cases"][7]["evidence"]["budget_exhausted"] = False
        self.assert_invalid(report, "RT08")

    def test_rejects_duplicate_or_extra_case(self):
        report = valid_report()
        report["cases"].append(copy.deepcopy(report["cases"][0]))
        self.assert_invalid(report, "duplicate")

        report = valid_report()
        report["cases"].append(case("RT01") | {"id": "RT10"})
        self.assert_invalid(report, "RT10")

    def test_rejects_reused_task_or_attempt_identity(self):
        report = valid_report()
        report["cases"][1]["task_id"] = report["cases"][0]["task_id"]
        self.assert_invalid(report, "task_id")

        report = valid_report()
        report["cases"][1]["attempt_id"] = report["cases"][0]["attempt_id"]
        self.assert_invalid(report, "attempt_id")

    def test_rejects_missing_real_codex_case_or_wrong_goal_profile(self):
        report = valid_report()
        for item in report["cases"][:7]:
            item["profile"] = "claude_http"
            item["image_digest"] = CLAUDE_DIGEST
        self.assert_invalid(report, "codex_http")

        report = valid_report()
        report["cases"][-1]["profile"] = "codex_http"
        report["cases"][-1]["image_digest"] = CODEX_DIGEST
        self.assert_invalid(report, "RT09")


if __name__ == "__main__":
    unittest.main()
