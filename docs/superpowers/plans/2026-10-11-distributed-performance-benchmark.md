# 分布式执行性能基准实施计划

> **执行说明：** 使用 `superpowers:executing-plans` 按任务逐项实施；步骤使用复选框跟踪。

**目标：** 用相同任务测出本机直跑、单 Worker、双 Worker 和冷/热工作区的延迟、成功率与资源差异。

**架构：** 新增可选的本地基准脚本，调用本机 Runtime 与 ComputeCloud Job API，采集 Job/Trace 数据并输出脱敏 JSON。CI 只运行无外部凭据的格式与统计契约测试；真实模型性能由用户本机或专用 Runner 重复执行，不把不稳定的耗时阈值作为 CI Gate。

**技术栈：** Python 3 标准库、ComputeCloud CLI/HTTP API、现有 Job Trace、JSON。

**设计文档：** [2026-10-11-worker-performance-and-balancing-design.md](../specs/2026-10-11-worker-performance-and-balancing-design.md)

## 全局约束

- 所有场景使用相同提示、Runtime、模型、仓库基线和验收条件。
- 脚本只读环境变量或本机配置中的凭据，不把 Token、请求正文或私有路径写入报告。
- 同时报告原始样本数和环境信息；不把缓存 Token 当成减少请求字节数。
- CI 不调用真实模型、不依赖在线凭据，也不设绝对耗时门槛。
- 性能结论必须基于重复测量的 P50/P95、成功率和资源数据。

## 重点复核场景

1. 某场景失败或无样本时不伪造 P50/P95；由 `test_empty_samples_are_null` 覆盖。
2. 成功与失败混合时成功率按全部样本计算；由 `test_success_rate_uses_all_samples` 覆盖。
3. 缺失 Token 或 RSS 指标显示 `null` 而非 0；由 `test_missing_metrics_remain_null` 覆盖。
4. 超时/取消任务计入样本并保留错误码；由 `test_failed_jobs_are_retained` 覆盖。
5. 报告中不出现密钥、提示正文或原始本机绝对路径；由 `test_report_redacts_sensitive_values` 覆盖。

---

### Task 1：定义报告结构与统计函数

**文件：**

- 新建：`scripts/benchmark_distributed_performance.py`
- 新建：`scripts/test_benchmark_distributed_performance.py`
- 新建：`docs/validation/distributed-performance-report.schema.json`

**接口：**

- 报告顶层字段：`schema_version`、`environment`、`scenarios`。
- 每种场景包括 `sample_count`、`success_rate`、`latency_ms.p50/p95`、`request_bytes`、Token 指标、`peak_rss_bytes` 和 `failures`。
- 不可用指标使用 JSON `null`；失败记录保留稳定错误码，不写提示正文。

- [x] 新增统计函数测试：`test_empty_samples_are_null`、`test_success_rate_uses_all_samples`、`test_missing_metrics_remain_null`、`test_report_redacts_sensitive_values`。
- [x] 先运行测试确认实现缺失，再实现统计及报告脱敏。
- [x] 实现纯函数 `percentile(samples: list[int], p: float) -> int | None`、`summarize_scenario(samples: list[dict]) -> dict` 和 `redact_environment(environment: dict) -> dict`。
- [x] 运行测试确认统计与脱敏通过。

### Task 2：接入四类执行场景

**文件：**

- 修改：`scripts/benchmark_distributed_performance.py`
- 新建：`docs/validation/distributed-performance-baseline.md`

**接口：**

- 命令行参数提供场景选择、样本数、提示文件、Runtime/模型标识、ComputeCloud 配置路径和输出路径。
- 场景名称固定为 `direct_local`、`single_worker`、`dual_worker_concurrent`、`workspace_cold`、`workspace_warm`。
- 脚本返回非零状态表示配置错误；单次 Job 失败写入报告并继续剩余样本。

- [x] 新增 CLI 参数解析、样本数、重复场景与 Worker 条件声明测试。
- [x] 实现直接 Runtime 和 Job API 调用适配器；凭据从受保护配置读取，报告只输出脱敏环境摘要。
- [x] 用 fixture 验证 Job 提交、轮询、Trace 采集、失败保留及 JSON schema。
- [x] 记录同一提示/模型/仓库及双 Worker 条件配置，并注明真实模型运行需要调用方已有凭据。
- [x] 根据独立审查加入每场景条件确认状态、逐场景 Worker 数声明，并拒绝重复场景，防止样本被覆盖或误读。

### Task 3：CI 契约验证与本机基线

**文件：**

- 修改：`Makefile`
- 修改：对应 CI workflow（按仓库现有 Python/Go 验证 Gate 接入）。
- 修改：`docs/validation/distributed-performance-baseline.md`

- [x] 增加 `benchmark-contract` 目标，只执行无网络单元/格式测试。
- [x] 运行 `make benchmark-contract`，确认 CI 环境不需要模型凭据。
- [ ] 在具备配置的本机/专用 Runner 重复运行四类场景，至少记录样本数、环境和 P50/P95。
- [ ] 将实测结果作为本机证据记录；没有实测的字段保持 `null`，不声称性能提升。
- [x] 运行 `git diff --check` 并提交代码、测试和中文文档，提交信息使用 `test(perf): add distributed execution benchmark`。

