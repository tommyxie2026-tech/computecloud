# Execution Token Observability & Efficiency — 分阶段实施计划

状态：PLANNED（2026-10-12）。属于 computecloud **Agent Job Executor** 的执行可观测和治理能力，不是通用 LLM/Token Gateway 或独立 AI Execution OS。

## 目标与边界
为 Goal → Plan → Job → Stage → Task → Attempt 建立可追溯的模型用量、估算费用、质量证据与效率信号。保持单 Go Server、SQLite、Worker 主动连接、durable execution truth。不引入强制 LiteLLM 代理，不复制 Usage/Budget/Evaluator 事实源。

**范围内**：Runtime usage ingestion；attempt/job/goal 聚合；重试与 re-plan 成本归因；执行产物/CI 的结果质量关联；异步效率建议；标准事件只读导出。
**范围外**：通用 LLM Gateway、跨所有应用 Prompt 仓库、全局模型价格计费平台、自动改写生产 Prompt、将 LiteLLM/Langfuse 作为核心依赖。

## 当前事实与缺口
README 基线：主线 server schema v18（Conversation 请求度量）、Worker v6；`claude_http` 已有客户端估算费用强制边界，真实 Provider 和独立主机验收未完成；Codex token/cost 和 Claude token 上限尚未支持。不得把预计费用声明为提供商最终账单，也不能把不完整 Usage 作为 0。

## 统一执行 Usage Contract（新增前先复用现有结构）
- event_id、source、source_event_id、observed_at、usage_schema_version
- goal_id、plan_revision、graph_generation、job_id、stage_id、task_id、attempt_id、runtime、model_id（未知可空）
- provider_request_id（可空）、logical_request_id（可空）、sequence/offset、attempt_generation
- input_tokens、output_tokens、cached_input_tokens、reasoning_tokens：optional；后两者为前两者子集，不能重复相加
- estimated_cost_microusd：optional；currency、pricing_version、usage_origin(provider_reported/agent_reported/estimated/unknown)
- usage_complete、result_status、error_class、latency_ms、correlation_ref；不得假设本地日志一定提供逐请求完整数据
- request/response payload 默认不存；可选本地脱敏后保存摘要/指纹，并通过策略控制授权和生命周期

不变量：按 event_id + source_event_id 去重；累计计数与 delta 必须显式区分；stale generation 不可写入新的 Attempt；接入 LiteLLM/旁路两个来源时按 provider_request_id/attempt/时间窗口和来源优先级仲裁，不能重复结算。原始事件只追加、聚合可重建。预算消费与观测统计区分，不能用异步遥测覆盖 durable accounting。

## 路线图与验收 Gate
### v0.4.x — Contract / Correctness，不扩大当前发布范围
- 盘点当前 Runtime / Conversation usage / Goal budget / SQLite schema，形成字段映射与精度矩阵。
- 为 `claude_http` 现有估算费用路径补齐增量结算、缺失值、重放、失败重试、恢复后的不重复计费测试。
- 规划只读导出协议和 Feature Flags；不承诺 Codex token hard limit。
- Gate `ci-execution-usage-contract`：缺失/累计/重放/故障、版本隔离、重复事件；保持现有 CI 时延预算。

### v0.5 — Execution Usage MVP（Agent-aware Scheduler 主线的子能力）
- Runtime Provider 可选上报 Token/Cache/Reasoning 用量，保留来源和置信级别。
- 聚合 per Attempt / Task / Job / Goal，按模型及 Runtime 可查看；增加 Retry Amplification 与 Cost-per-successful-Job 的实验性指标。
- 提供只读 API/CLI 和基础报表；输出结构化事件以供外部 Collector / LiteLLM 适配。
- Gate `ci-execution-usage-e2e`：两 Worker、多 Attempt、取消、恢复、后到事件及去重；未支持的 Provider 显示 UNKNOWN。

### v0.6 — Evidence-linked Quality & Governance（Enterprise Governance 的子能力）
- 将 Artifact、Evaluator、CI pass/fail 关联到 Job/Goal，用可追溯规则确定 outcome，而非 LLM 自评断言。
- 报告预算偏差、失败/重试成本、成功任务等价成本，按任务类型分层比较。
- 异步、默认关闭的 Quality Evaluator 插件，记录评价依据和 evaluator_version；不能阻塞 Runtime 或修改 durable terminal state。
- Gate `ci-token-quality-governance`：无证据为 UNKNOWN；异步失败无执行影响；权限、敏感数据不泄漏。

### v0.7 — Efficiency Signals（Scale & Resilience 的子能力）
- 可解释的重复工具调用、上下文增长、缓存效率、re-plan 放大规则；规则仅提供建议和审计证据。
- 在真实基准对照实验中评估单位成功任务成本变化、质量不降、采集开销与事件积压恢复。
- 可选把历史统计作为 Scheduler hint，但不能越过 capability/security hard constraints。
- Gate `ci-token-efficiency-regression`：负向案例、采集故障 fail-open、低开销预算、指标对照回归。

### v1.0 — Stable Export Contract
- 稳定 OpenTelemetry/JSONL（或扩展协议）只读导出与版本管理；对接外部 Token Intelligence / LiteLLM observability。
- 实际 Provider/双机恢复、敏感字段清理和审计的生产验收完成后才宣布 GA。

## 验收指标（建议初始目标，不冒充已完成）
- 受支持 Provider：按事件源校验的 Request/Attempt 对账准确率 ≥99%；未知项单独统计。
- 重放与多来源重复结算 = 0；断线恢复不会更改已终态 Attempt 的用量事实。
- 默认采集不保存完整 Prompt/Response、Credential；额外内容必须显式授权、脱敏和设定保留期限。
- Collector/Evaluator 不得在模型请求关键路径同步等待；监控失效不阻塞执行，但预算强制规则仍遵守既有 fail-closed 策略。
- 性能门槛在真实负载测量后冻结；初期只防显著回退，不虚构 P95 SLA。

## 开发顺序
1. 使用现有 schema/Runtime usage 盘点，提交 Contract PR；
2. 独立 Usage 正确性与恢复 CI；
3. v0.5 只读 API + Attempt/Goal 聚合；
4. v0.6 Artifact/CI/Evaluator 质量证据关联；
5. v0.7 建议规则及实验；
6. v1.0 外部遥测契约稳定化。

建议分配给当前 Runtime/Goal Governance workstream，严格按 contract → migration → implementation → integration Gate 合并，**不新设产品主线**。
