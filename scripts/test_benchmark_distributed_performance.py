import unittest

from scripts.benchmark_distributed_performance import (
    percentile,
    summarize_scenario,
    redact_environment,
)


class BenchmarkReportTests(unittest.TestCase):
    def test_empty_samples_are_null(self):
        report = summarize_scenario([])
        self.assertEqual(report["sample_count"], 0)
        self.assertIsNone(report["latency_ms"]["p50"])
        self.assertIsNone(report["latency_ms"]["p95"])
        self.assertIsNone(report["success_rate"])

    def test_success_rate_uses_all_samples(self):
        report = summarize_scenario([
            {"latency_ms": 100, "success": True},
            {"latency_ms": 200, "success": False, "error_code": "TIMEOUT"},
        ])
        self.assertEqual(report["success_rate"], 0.5)
        self.assertEqual(report["failures"], [{"error_code": "TIMEOUT"}])
        self.assertEqual(report["latency_ms"], {"p50": 150, "p95": 195})

    def test_missing_metrics_remain_null(self):
        report = summarize_scenario([{"latency_ms": 10, "success": True}])
        for key in ("request_bytes", "input_tokens", "output_tokens", "cached_input_tokens", "peak_rss_bytes"):
            self.assertIsNone(report[key])

    def test_report_redacts_sensitive_values(self):
        safe = redact_environment({
            "platform": "Linux-x86_64",
            "python_version": "3.13.1",
            "runtime": "codex_exec",
            "model": "test-model",
            "token": "secret-token-value",
            "prompt": "private prompt text",
            "home": "/Users/private-user",
            "repository_path": "/private/repo",
        })
        rendered = str(safe)
        for secret in ("secret-token-value", "private prompt text", "/Users/private-user", "/private/repo"):
            self.assertNotIn(secret, rendered)
        self.assertEqual(set(safe), {"platform", "python_version", "runtime", "model"})

    def test_failed_jobs_are_retained(self):
        report = summarize_scenario([{"latency_ms": 50, "success": False, "error_code": "CANCELED"}])
        self.assertEqual(report["sample_count"], 1)
        self.assertEqual(report["failures"], [{"error_code": "CANCELED"}])


class BenchmarkCliTests(unittest.TestCase):
    def test_cli_parses_scenarios_samples_and_output(self):
        from scripts.benchmark_distributed_performance import build_parser

        args = build_parser().parse_args([
            "--scenarios", "single_worker", "dual_worker_concurrent",
            "--samples", "4", "--prompt-file", "prompt.txt", "--output", "report.json",
        ])
        self.assertEqual(args.scenarios, ["single_worker", "dual_worker_concurrent"])
        self.assertEqual(args.samples, 4)
        self.assertEqual(args.output, "report.json")

    def test_job_sample_keeps_failure_and_collects_trace_metrics(self):
        import argparse
        import json
        import tempfile
        from pathlib import Path
        from types import SimpleNamespace
        from unittest.mock import patch
        from scripts.benchmark_distributed_performance import _job_sample

        with tempfile.TemporaryDirectory() as directory:
            prompt_path = Path(directory) / "prompt.txt"
            prompt_path.write_text("private prompt marker", encoding="utf-8")
            args = argparse.Namespace(
                computecloud_bin="cc", client_config="client.yaml", job_template="template.json",
                timeout_seconds=5, model="fixture-model", runtime="codex_exec",
            )
            responses = [
                {"job_id": "job-fixture"},
                {"state": "FAILED", "error_code": "RUNTIME_FAILED"},
                {"worker_usage": {"input_tokens": 20, "output_tokens": 5}, "attempts": [
                    {"metrics": {"process": {"peak_rss_bytes": 1000}, "usage": {"cached_input_tokens": 7}}}
                ]},
            ]
            def fake_run(command, **kwargs):
                return SimpleNamespace(returncode=0, stdout=json.dumps(responses.pop(0)).encode(), stderr=b"")
            with patch("scripts.benchmark_distributed_performance.subprocess.run", side_effect=fake_run):
                sample = _job_sample(args, "single_worker", b"private prompt marker", {
                    "mode": "single", "input": {"text": "old"}, "execution": {},
                })
            self.assertFalse(sample["success"])
            self.assertEqual(sample["error_code"], "RUNTIME_FAILED")
            self.assertEqual(sample["input_tokens"], 20)
            self.assertEqual(sample["cached_input_tokens"], 7)
            self.assertNotIn("private prompt marker", str(sample))

    def test_direct_cli_writes_report_without_prompt(self):
        import json
        import tempfile
        from pathlib import Path
        from scripts.benchmark_distributed_performance import main

        with tempfile.TemporaryDirectory() as directory:
            prompt = Path(directory) / "prompt.txt"
            output = Path(directory) / "report.json"
            prompt.write_text("private prompt sentinel", encoding="utf-8")
            result = main([
                "--scenarios", "direct_local", "--samples", "1", "--prompt-file", str(prompt),
                "--direct-command", "cat", "--output", str(output),
            ])
            self.assertEqual(result, 0)
            report = output.read_text(encoding="utf-8")
            self.assertNotIn("private prompt sentinel", report)
            data = json.loads(report)
            self.assertEqual(data["scenarios"]["direct_local"]["sample_count"], 1)
            self.assertEqual(data["scenarios"]["direct_local"]["success_rate"], 1.0)

    def test_zero_samples_rejected(self):
        import argparse
        from scripts.benchmark_distributed_performance import run_benchmark, build_parser

        args = build_parser().parse_args([
            "--scenarios", "direct_local", "--samples", "0", "--prompt-file", "missing",
            "--direct-command", "codex exec -", "--output", "out.json",
        ])
        with self.assertRaises(ValueError):
            run_benchmark(args)


    def test_report_schema_declares_generated_metrics(self):
        import json
        from pathlib import Path

        schema = json.loads(Path("docs/validation/distributed-performance-report.schema.json").read_text(encoding="utf-8"))
        self.assertEqual(schema["properties"]["schema_version"]["const"], "1.0")
        required = set(schema["$defs"]["scenario"]["required"])
        self.assertTrue({"sample_count", "success_rate", "latency_ms", "request_bytes", "failures"}.issubset(required))

    def test_report_records_operator_declared_conditions(self):
        from scripts.benchmark_distributed_performance import build_report

        report = build_report({}, {"dual_worker_concurrent": []}, {
            "dual_worker_concurrent": {"condition_status": "operator_confirmed", "worker_count_declared": 2},
        })
        scenario = report["scenarios"]["dual_worker_concurrent"]
        self.assertEqual(scenario["condition_status"], "operator_confirmed")
        self.assertEqual(scenario["worker_count_declared"], 2)

    def test_duplicate_scenarios_rejected(self):
        from scripts.benchmark_distributed_performance import run_benchmark, build_parser

        args = build_parser().parse_args([
            "--scenarios", "direct_local", "direct_local", "--prompt-file", "missing",
            "--direct-command", "cat", "--output", "out.json",
        ])
        with self.assertRaisesRegex(ValueError, "不能重复"):
            run_benchmark(args)

    def test_dual_worker_requires_explicit_declaration_and_confirmation(self):
        from scripts.benchmark_distributed_performance import run_benchmark, build_parser

        args = build_parser().parse_args([
            "--scenarios", "dual_worker_concurrent", "--dual-worker-count", "2", "--prompt-file", "missing",
            "--output", "out.json",
        ])
        with self.assertRaisesRegex(ValueError, "操作者确认"):
            run_benchmark(args)

    def test_cold_workspace_requires_one_sample_per_reset(self):
        from scripts.benchmark_distributed_performance import run_benchmark, build_parser

        args = build_parser().parse_args([
            "--scenarios", "workspace_cold", "--samples", "2", "--confirm-workspace-cold",
            "--prompt-file", "missing", "--output", "out.json",
        ])
        with self.assertRaisesRegex(ValueError, "仅允许一个样本"):
            run_benchmark(args)


if __name__ == "__main__":
    unittest.main()
