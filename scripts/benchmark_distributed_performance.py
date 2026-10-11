#!/usr/bin/env python3
"""Measure comparable local or ComputeCloud executions and write a redacted report."""

from __future__ import annotations

import argparse
import json
import math
import platform
import re
import os
import shlex
import shutil
import subprocess
import tempfile
import time
import uuid
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any


REPORT_SCHEMA_VERSION = "1.0"
ENVIRONMENT_FIELDS = ("platform", "python_version", "runtime", "model")
METRIC_FIELDS = ("request_bytes", "input_tokens", "output_tokens", "cached_input_tokens", "peak_rss_bytes")
ERROR_CODE_RE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")


def percentile(samples: list[int], p: float) -> int | None:
    """Return a linearly interpolated percentile; empty input is unavailable."""
    if not samples:
        return None
    if not 0 <= p <= 1:
        raise ValueError("p must be between 0 and 1")
    ordered = sorted(samples)
    position = (len(ordered) - 1) * p
    lower = math.floor(position)
    upper = math.ceil(position)
    value = ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)
    return int(round(value))


def summarize_scenario(samples: list[dict[str, Any]]) -> dict[str, Any]:
    latencies = [int(s["latency_ms"]) for s in samples if isinstance(s.get("latency_ms"), (int, float)) and s["latency_ms"] >= 0]
    successes = [bool(s.get("success", False)) for s in samples]
    out: dict[str, Any] = {
        "sample_count": len(samples),
        "success_rate": sum(successes) / len(successes) if successes else None,
        "latency_ms": {"p50": percentile(latencies, 0.50), "p95": percentile(latencies, 0.95)},
        "request_bytes": percentile([int(s["request_bytes"]) for s in samples if isinstance(s.get("request_bytes"), (int, float)) and s["request_bytes"] >= 0], 0.50),
        "input_tokens": _sum_metric(samples, "input_tokens"),
        "output_tokens": _sum_metric(samples, "output_tokens"),
        "cached_input_tokens": _sum_metric(samples, "cached_input_tokens"),
        "peak_rss_bytes": max((int(s["peak_rss_bytes"]) for s in samples if isinstance(s.get("peak_rss_bytes"), (int, float)) and s["peak_rss_bytes"] >= 0), default=None),
        "failures": [{"error_code": _safe_error_code(s.get("error_code"))} for s in samples if not s.get("success", False)],
    }
    return out


def _sum_metric(samples: list[dict[str, Any]], name: str) -> int | None:
    values = [int(s[name]) for s in samples if isinstance(s.get(name), (int, float)) and s[name] >= 0]
    return sum(values) if values else None


def _safe_error_code(value: Any) -> str:
    code = str(value or "UNKNOWN")
    return code if ERROR_CODE_RE.fullmatch(code) else "UNKNOWN"


def redact_environment(environment: dict[str, Any]) -> dict[str, Any]:
    """Keep a small allowlist; paths, credentials and arbitrary values never pass through."""
    result: dict[str, Any] = {}
    for key in ENVIRONMENT_FIELDS:
        value = environment.get(key)
        if isinstance(value, str):
            if not value or len(value) > 128 or value.startswith(("/", "~", "\\")) or "\n" in value or "\r" in value:
                continue
            if re.search(r"(?i)(token|secret|api[_-]?key|password)=", value):
                continue
            if not re.fullmatch(r"[A-Za-z0-9_.:/+@-]+", value):
                continue
            result[key] = value
        elif isinstance(value, (int, float, bool)):
            result[key] = value
    return result


def build_report(environment: dict[str, Any], scenarios: dict[str, list[dict[str, Any]]], conditions: dict[str, dict[str, Any]] | None = None) -> dict[str, Any]:
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "environment": redact_environment(environment),
        "scenarios": {
            name: {**summarize_scenario(samples), **(conditions or {}).get(name, {})}
            for name, samples in sorted(scenarios.items())
        },
    }


SCENARIOS = ("direct_local", "single_worker", "dual_worker_concurrent", "workspace_cold", "workspace_warm")
TERMINAL_STATES = {"SUCCEEDED", "FAILED", "CANCELED"}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="对比本机直跑与 ComputeCloud Worker 执行")
    parser.add_argument("--scenarios", nargs="+", choices=SCENARIOS, required=True)
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--prompt-file", required=True, help="统一输入提示文件；不会写入报告")
    parser.add_argument("--runtime", default="")
    parser.add_argument("--model", default="")
    parser.add_argument("--single-worker-count", type=int, help="single_worker 场景操作者声明的合格在线 Worker 数")
    parser.add_argument("--dual-worker-count", type=int, help="dual_worker_concurrent 场景操作者声明的合格在线 Worker 数")
    parser.add_argument("--confirm-single-worker", action="store_true", help="已通过 worker list 核对单 Worker 条件")
    parser.add_argument("--confirm-dual-workers", action="store_true", help="已通过 worker list 核对双 Worker 条件及槽位")
    parser.add_argument("--confirm-workspace-cold", action="store_true", help="已为每个冷样本重置隔离测试工作区")
    parser.add_argument("--confirm-workspace-warm", action="store_true", help="已在热样本前完成不计入样本的预热")
    parser.add_argument("--computecloud-bin", default="computecloud")
    parser.add_argument("--client-config", default="")
    parser.add_argument("--job-template", default="", help="有效的 single Job JSON 模板")
    parser.add_argument("--direct-command", default="", help="本机 Runtime 命令；提示通过 stdin 输入，不经 shell")
    parser.add_argument("--timeout-seconds", type=int, default=3600)
    parser.add_argument("--output", required=True)
    return parser


def _cli_json(args: list[str], timeout: int) -> dict[str, Any]:
    completed = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    if completed.returncode != 0:
        code_match = re.search(rb'"code"\s*:\s*"([A-Z][A-Z0-9_]{0,63})"', completed.stderr)
        http_match = re.search(rb"HTTP\s+(\d{3})", completed.stderr)
        code = code_match.group(1).decode("ascii") if code_match else ("HTTP_" + http_match.group(1).decode("ascii") if http_match else "CLI_FAILED")
        raise RuntimeError(code)
    try:
        value = json.loads(completed.stdout)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("INVALID_CLI_RESPONSE") from exc
    if not isinstance(value, dict):
        raise RuntimeError("INVALID_CLI_RESPONSE")
    return value


def _direct_sample(command: list[str], prompt: bytes, timeout: int) -> dict[str, Any]:
    started = time.monotonic_ns()
    try:
        completed = subprocess.run(command, input=prompt, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
        latency = (time.monotonic_ns() - started) // 1_000_000
        sample: dict[str, Any] = {"success": completed.returncode == 0, "latency_ms": latency, "request_bytes": len(prompt)}
        if not sample["success"]:
            sample["error_code"] = "LOCAL_RUNTIME_FAILED"
        # Metrics may be emitted as a JSON object, but arbitrary output is discarded.
        try:
            metrics = json.loads(completed.stdout)
        except (UnicodeDecodeError, json.JSONDecodeError):
            metrics = {}
        if isinstance(metrics, dict):
            for key in METRIC_FIELDS:
                if key in metrics and isinstance(metrics[key], int) and metrics[key] >= 0:
                    sample[key] = metrics[key]
        return sample
    except subprocess.TimeoutExpired:
        return {"success": False, "latency_ms": (time.monotonic_ns() - started) // 1_000_000, "request_bytes": len(prompt), "error_code": "TIMEOUT"}


def _job_sample(args: argparse.Namespace, scenario: str, prompt: bytes, template: dict[str, Any]) -> dict[str, Any]:
    spec = json.loads(json.dumps(template))
    if spec.get("mode") != "single" or not isinstance(spec.get("input"), dict):
        raise ValueError("Job 模板必须是包含 input.text 的 single Job JSON")
    spec["input"]["text"] = prompt.decode("utf-8")
    execution = spec.get("execution", {})
    if args.model:
        execution["model"] = args.model
    if args.runtime:
        execution["runtime_profile"] = args.runtime
    spec["execution"] = execution
    payload = json.dumps(spec, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    started = time.monotonic_ns()
    request_bytes = len(payload)
    with tempfile.NamedTemporaryFile(prefix="cc-benchmark-", suffix=".json", delete=False) as stream:
        stream.write(payload)
        request_path = stream.name
    try:
        submit = _cli_json([args.computecloud_bin, "job", "submit", "--config", args.client_config,
                           "--key", "bench-" + uuid.uuid4().hex, "--file", request_path], args.timeout_seconds)
        job_id = submit.get("job_id") or submit.get("id")
        if not isinstance(job_id, str) or not job_id:
            raise RuntimeError("INVALID_SUBMIT_RESPONSE")
        deadline = time.monotonic() + args.timeout_seconds
        job: dict[str, Any] = {}
        while time.monotonic() < deadline:
            job = _cli_json([args.computecloud_bin, "job", "get", "--config", args.client_config, "--id", job_id], min(args.timeout_seconds, 30))
            if job.get("state") in TERMINAL_STATES:
                break
            time.sleep(0.5)
        if job.get("state") not in TERMINAL_STATES:
            return {"success": False, "latency_ms": (time.monotonic_ns() - started) // 1_000_000, "request_bytes": request_bytes, "error_code": "TIMEOUT"}
        sample = {"success": job.get("state") == "SUCCEEDED", "latency_ms": (time.monotonic_ns() - started) // 1_000_000, "request_bytes": request_bytes}
        if not sample["success"]:
            sample["error_code"] = _safe_error_code(job.get("error_code"))
        try:
            trace = _cli_json([args.computecloud_bin, "job", "trace", "--config", args.client_config, "--id", job_id, "--limit", "100"], min(args.timeout_seconds, 30))
        except (RuntimeError, subprocess.TimeoutExpired):
            trace = {}
        usage = trace.get("worker_usage", {}) if isinstance(trace.get("worker_usage"), dict) else {}
        for target, source in (("input_tokens", "input_tokens"), ("output_tokens", "output_tokens")):
            if isinstance(usage.get(source), int) and usage[source] >= 0:
                sample[target] = usage[source]
        peak = None
        cached = 0
        cached_seen = False
        for attempt in trace.get("attempts", []) if isinstance(trace.get("attempts"), list) else []:
            metrics = attempt.get("metrics") or {}
            process = metrics.get("process") or {}
            if isinstance(process.get("peak_rss_bytes"), int):
                peak = max(peak or 0, process["peak_rss_bytes"])
            tokens = metrics.get("usage") or {}
            if isinstance(tokens.get("cached_input_tokens"), int):
                cached += tokens["cached_input_tokens"]
                cached_seen = True
        if peak is not None:
            sample["peak_rss_bytes"] = peak
        if cached_seen:
            sample["cached_input_tokens"] = cached
        return sample
    except subprocess.TimeoutExpired:
        return {"success": False, "latency_ms": (time.monotonic_ns() - started) // 1_000_000, "request_bytes": request_bytes, "error_code": "TIMEOUT"}
    except (RuntimeError, OSError):
        return {"success": False, "latency_ms": (time.monotonic_ns() - started) // 1_000_000, "request_bytes": request_bytes, "error_code": "CLI_FAILED"}
    finally:
        try:
            os.unlink(request_path)
        except OSError:
            pass


def _run_job_scenario(args: argparse.Namespace, scenario: str, prompt: bytes, template: dict[str, Any]) -> list[dict[str, Any]]:
    if scenario == "dual_worker_concurrent":
        samples: list[dict[str, Any]] = []
        with ThreadPoolExecutor(max_workers=args.samples) as pool:
            futures = [pool.submit(_job_sample, args, scenario, prompt, template) for _ in range(args.samples)]
            for future in as_completed(futures):
                samples.append(future.result())
        return samples
    return [_job_sample(args, scenario, prompt, template) for _ in range(args.samples)]


def run_benchmark(args: argparse.Namespace) -> dict[str, Any]:
    if args.samples < 1 or args.timeout_seconds < 1:
        raise ValueError("samples 和 timeout-seconds 必须大于 0")
    if len(args.scenarios) != len(set(args.scenarios)):
        raise ValueError("scenarios 不能重复")
    required_conditions = {
        "single_worker": (1, args.single_worker_count, args.confirm_single_worker),
        "dual_worker_concurrent": (2, args.dual_worker_count, args.confirm_dual_workers),
    }
    for name, (required_count, declared_count, confirmed) in required_conditions.items():
        if name in args.scenarios:
            if declared_count != required_count:
                raise ValueError(f"{name} 场景必须通过对应参数声明 {required_count} 个 Worker")
            if not confirmed:
                raise ValueError(f"{name} 场景必须由操作者确认 Worker 在线状态和槽位")
    if "workspace_cold" in args.scenarios and not args.confirm_workspace_cold:
        raise ValueError("workspace_cold 场景必须确认每个样本前均已重置隔离测试工作区")
    if "workspace_cold" in args.scenarios and args.samples != 1:
        raise ValueError("workspace_cold 每次运行仅允许一个样本；重置工作区后重复运行以采集多个冷样本")
    if "workspace_warm" in args.scenarios and not args.confirm_workspace_warm:
        raise ValueError("workspace_warm 场景必须确认样本前已完成预热")
    prompt = Path(args.prompt_file).read_bytes()
    if not prompt:
        raise ValueError("提示文件不能为空")
    env = {"platform": platform.system() + "-" + platform.machine(), "python_version": platform.python_version(),
           "runtime": args.runtime or None, "model": args.model or None}
    template: dict[str, Any] = {}
    if any(name != "direct_local" for name in args.scenarios):
        if not args.client_config or not args.job_template:
            raise ValueError("Job 场景需要 --client-config 和 --job-template")
        if not Path(args.client_config).is_file() or not Path(args.job_template).is_file():
            raise ValueError("ComputeCloud 客户端配置或 Job 模板文件不存在")
        if not (shutil.which(args.computecloud_bin) or Path(args.computecloud_bin).is_file()):
            raise ValueError("找不到 ComputeCloud CLI")
        template = json.loads(Path(args.job_template).read_text(encoding="utf-8"))
    scenarios: dict[str, list[dict[str, Any]]] = {}
    for name in args.scenarios:
        if name == "direct_local":
            if not args.direct_command:
                raise ValueError("direct_local 场景需要 --direct-command")
            command = shlex.split(args.direct_command)
            if not command or not (shutil.which(command[0]) or Path(command[0]).is_file()):
                raise ValueError("找不到 direct-command 可执行文件")
            scenarios[name] = [_direct_sample(command, prompt, args.timeout_seconds) for _ in range(args.samples)]
        else:
            scenarios[name] = _run_job_scenario(args, name, prompt, template)
    condition_flags = {
        "single_worker": args.confirm_single_worker,
        "dual_worker_concurrent": args.confirm_dual_workers,
        "workspace_cold": args.confirm_workspace_cold,
        "workspace_warm": args.confirm_workspace_warm,
    }
    worker_counts = {
        "single_worker": args.single_worker_count,
        "dual_worker_concurrent": args.dual_worker_count,
    }
    conditions = {
        name: {
            "condition_status": "operator_confirmed" if condition_flags.get(name, False) else ("not_applicable" if name == "direct_local" else "not_confirmed"),
            "worker_count_declared": worker_counts.get(name),
        }
        for name in scenarios
    }
    return build_report(env, scenarios, conditions)


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        report = run_benchmark(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        parser.error(str(exc))
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
