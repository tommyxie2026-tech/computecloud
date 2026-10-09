import copy
import unittest

from scripts.validate_v047_performance_results import ValidationError, summarize, validate_report


SOURCE_SHA = "a" * 40
HASH = "b" * 64


def samples(prefix, values):
    return [
        {"attempt_id": f"{prefix}-{index}", "full_job_ms": value, "preparation_ms": value // 4}
        for index, value in enumerate(values)
    ]


def performance_case(case_id, size_class, base):
    cold = samples(case_id.lower() + "-cold", [base + i * 10 for i in range(10)])
    warm = samples(case_id.lower() + "-warm", [int((base + i * 10) * 0.3) for i in range(10)])
    computed = summarize(cold, warm)
    return {
        "id": case_id,
        "status": "PASSED",
        "source_sha": SOURCE_SHA,
        "repository_ref": "repo-" + size_class,
        "base_commit": "c" * 40,
        "size_class": size_class,
        "load": {"worker_slots": 2, "concurrent_jobs": 1, "cpu_count": 4, "memory_mib": 4096},
        "cold_samples": cold,
        "warm_samples": warm,
        "reported": computed,
    }


def relay_case(case_id):
    evidence = {
        "RLY01": {"direct_connected": True, "relay_used": False},
        "RLY02": {"direct_failure_forced": True, "fallback_attempted": True, "relay_connected": True},
        "RLY03": {"real_nat_path": True, "control_during_bulk": True, "cancel_completed": True},
        "RLY04": {"disconnect_injected": True, "reconnected": True, "event_watermark_preserved": True},
        "RLY05": {"relay_restarted": True, "fresh_ticket_acquired": True, "reconnected": True, "long_task_completed": True},
    }[case_id]
    return {
        "id": case_id,
        "status": "PASSED",
        "source_sha": SOURCE_SHA,
        "attempt_ids": ["attempt-" + case_id.lower()],
        "worker_ids": ["worker-a", "worker-b"],
        "evidence": evidence,
    }


def valid_report():
    return {
        "schema_version": "computecloud.v0.4.7.performance-acceptance.v1",
        "status": "PASSED",
        "candidate": {"source_sha": SOURCE_SHA, "built_from_sha": SOURCE_SHA},
        "execution": {"environment": "real", "linux": True, "started_at": "2026-10-09T03:00:00Z", "completed_at": "2026-10-09T05:00:00Z"},
        "workers": [
            {"worker_id": "worker-a", "machine_id_sha256": "1" * 64},
            {"worker_id": "worker-b", "machine_id_sha256": "2" * 64},
        ],
        "relay_topology": {
            "real_nat": True,
            "loopback_only": False,
            "relay_binary_sha256": HASH,
            "server_binary_sha256": HASH,
            "worker_binary_sha256": HASH,
        },
        "performance_cases": [
            performance_case("PERF01", "small", 1000),
            performance_case("PERF02", "medium", 2000),
            performance_case("PERF03", "large", 3000),
        ],
        "relay_cases": [relay_case(f"RLY{i:02d}") for i in range(1, 6)],
        "correctness": {"duplicate_attempts": 0, "lost_event_watermarks": 0, "invalid_artifacts": 0},
    }


class PerformanceResultsValidationTest(unittest.TestCase):
    def assert_invalid(self, report, fragment):
        with self.assertRaisesRegex(ValidationError, fragment):
            validate_report(report)

    def test_accepts_complete_real_report(self):
        validate_report(valid_report())

    def test_rejects_too_few_cold_or_warm_samples(self):
        for field in ("cold_samples", "warm_samples"):
            report = valid_report()
            report["performance_cases"][0][field].pop()
            self.assert_invalid(report, field)

    def test_rejects_missing_p95_or_load(self):
        report = valid_report()
        del report["performance_cases"][0]["reported"]["warm_p95_ms"]
        self.assert_invalid(report, "warm_p95_ms")
        report = valid_report()
        del report["performance_cases"][0]["load"]["cpu_count"]
        self.assert_invalid(report, "cpu_count")

    def test_rejects_tampered_summary(self):
        report = valid_report()
        report["performance_cases"][0]["reported"]["cold_p50_ms"] += 1
        self.assert_invalid(report, "cold_p50_ms")

    def test_rejects_warm_p50_ratio_above_target(self):
        report = valid_report()
        item = report["performance_cases"][0]
        item["warm_samples"] = samples("slow-warm", [900 + i * 10 for i in range(10)])
        item["reported"] = summarize(item["cold_samples"], item["warm_samples"])
        self.assert_invalid(report, "warm_p50_ratio")

    def test_rejects_loopback_only_or_missing_real_nat(self):
        report = valid_report()
        report["relay_topology"]["loopback_only"] = True
        self.assert_invalid(report, "loopback")
        report = valid_report()
        report["relay_topology"]["real_nat"] = False
        self.assert_invalid(report, "real_nat")

    def test_rejects_missing_direct_fallback(self):
        report = valid_report()
        report["relay_cases"][1]["evidence"]["fallback_attempted"] = False
        self.assert_invalid(report, "fallback_attempted")

    def test_rejects_missing_restart_or_event_watermark(self):
        report = valid_report()
        report["relay_cases"][4]["evidence"]["relay_restarted"] = False
        self.assert_invalid(report, "relay_restarted")
        report = valid_report()
        report["relay_cases"][3]["evidence"]["event_watermark_preserved"] = False
        self.assert_invalid(report, "event_watermark")

    def test_rejects_duplicate_attempt_ids(self):
        report = valid_report()
        report["relay_cases"][0]["attempt_ids"] = [report["performance_cases"][0]["cold_samples"][0]["attempt_id"]]
        self.assert_invalid(report, "duplicate Attempt")

    def test_rejects_nonzero_correctness_or_missing_case(self):
        report = valid_report()
        report["correctness"]["lost_event_watermarks"] = 1
        self.assert_invalid(report, "lost_event_watermarks")
        report = valid_report()
        report["relay_cases"] = report["relay_cases"][:-1]
        self.assert_invalid(report, "RLY05")

    def test_rejects_duplicate_case(self):
        report = valid_report()
        report["relay_cases"].append(copy.deepcopy(report["relay_cases"][0]))
        self.assert_invalid(report, "duplicate")

    def test_rejects_credential_shaped_fields(self):
        report = valid_report()
        report["relay_topology"]["ticket"] = "redacted"
        self.assert_invalid(report, "credential-shaped")


if __name__ == "__main__":
    unittest.main()
