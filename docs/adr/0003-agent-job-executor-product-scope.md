# ADR-003：computecloud 定位为 Agent Job Executor

- 日期：2026-09-24
- 状态：Accepted
- 影响范围：长期路线图、总体架构、版本规划
- 替代观点：将 v0.2 线性演进为 AI Execution OS

## Context

v0.2 已经形成清晰、可运行的系统边界：

- Job / Task / Attempt；
- Codex / Claude Runtime；
- 多 Worker；
- HTTP / MCP；
- Map/Reduce；
- SQLite；
- lease / heartbeat；
- Artifact；
- 故障恢复。

此前曾提出将其继续演变为统一承载模型 Serving、Deployment、Session、KV Cache、Storage Fabric、GPU 调度和多集群控制面的 AI Execution OS。

进一步评审后确认，这两者并非同一领域模型的自然连续演进。若强行线性演变，会让 v0.2 的 Job 语义、数据库结构和 Worker 模型承受大量与 Agent Job 无关的兼容负担。

## Decision

computecloud 当前产品主线正式定义为：

> **Agent Job Executor**

长期围绕 Agent Job 的可靠执行持续演进，而不是演进为通用 AI Execution Infrastructure。

核心模型保持：

~~~text
Job -> Task -> Attempt
~~~

主要扩展方向：

- Agent Runtime；
- Tool Runtime；
- 有限 Job 编排；
- 重试与恢复；
- Worker capability；
- resource-aware scheduling；
- Artifact / Workspace；
- 多租户、安全、审计；
- 大规模 Worker 集群；
- 必要时的控制面 HA。

## Non-goals

当前主线明确不承担：

- 模型 Serving 平台；
- Model Deployment；
- KV Cache / Memory Fabric；
- 训练调度；
- 通用 GPU Cloud；
- 通用 Workflow 平台；
- AI Execution OS。

## Consequences

正面影响：

- v0.2 的领域模型可以直接连续演进；
- 避免为了未来假设大规模重写；
- 研发重点集中在可靠 Agent 执行；
- 单 Go 二进制 + SQLite 的轻量优势可以保留更久；
- Runtime / Tool 生态成为主要扩展边界；
- 测试和故障模型更容易保持一致。

代价：

- 模型 Serving / KV Cache / GPU 推理等能力不进入当前主线；
- 如果未来确实需要 AI Execution Platform，应建立独立架构和产品边界，再通过 API / Adapter 复用已有 Agent Job Executor，而不是强制兼容内部数据模型。

## Superseded Direction

[AI Execution OS 总体架构](../design/ai-execution-os-architecture.md) 保留为历史探索材料，不再作为 computecloud 当前产品路线的实施依据。

当前规范以：

- [Agent Job Executor 总体架构](../design/agent-job-executor-architecture.md)
- [长期演进路线图](../implementation/long-term-roadmap.md)

为准。
