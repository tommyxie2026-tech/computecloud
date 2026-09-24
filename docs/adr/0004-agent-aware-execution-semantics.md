# ADR-004：Agent-aware 执行语义与长期演进边界

- 日期：2026-09-24
- 状态：Accepted
- 依赖：[ADR-003：Agent Job Executor 产品边界](0003-agent-job-executor-product-scope.md)
- 影响范围：领域模型、Runtime/Tool 边界、Artifact/Workspace 生命周期、Scheduler 演进、G1 定位

## Context

ADR-003 已经将 computecloud 从“AI Execution OS”路线收敛为 Agent Job Executor。进一步专家评审确认，该方向与 v0.2 的实际实现连续，但仍需防止后续版本重新出现范围膨胀。

评审集中发现四个结构问题：

1. Job -> Task -> Attempt 对 single 足够，但对现有 Map/Reduce 以及未来 fan-out / barrier / fan-in 缺少稳定的阶段语义；
2. Artifact / Workspace 被放在 Runtime 扩展阶段偏晚，而它们实际上直接参与 Attempt fencing、重试和结果提交正确性；
3. Runtime 与 Tool 如果完全等价，会导致 Shell、Browser、Git、MCP 等都被建模成 Runtime，模糊 Agent 执行载体与 Agent 可调用能力的边界；
4. Scheduler 如果按 CPU/GPU 资源编排方向扩展，会逐渐退化成通用资源调度器，失去 Agent 语义优势。

## Decision

computecloud 长期定位进一步明确为：

> **Agent-aware Distributed Job Execution Platform**

“Agent Job Executor”继续作为产品类别和简称；“Agent-aware”表示系统原生理解 Agent Runtime、Tool、Credential、Workspace、Repository、Approval、Artifact provenance 等执行语义。

### 1. 核心模型增加 Stage

领域模型固定为：

~~~text
Job
└── Stage
    └── Task
        └── Attempt
~~~

语义：

- Job：一次完整的 Agent 业务任务；
- Stage：Job 内一个有限执行阶段和同步边界；
- Task：可独立调度的逻辑工作单元；
- Attempt：Task 的一次真实执行尝试。

Map/Reduce 映射为 Map Stage + Reduce Stage；fan-out / barrier / fan-in 映射为有限 Stage 组合。

Stage 不发展为通用 Workflow DSL，不支持为了“工作流完整性”而引入任意循环、嵌套流程语言或复杂动态编排。

### 2. Artifact / Workspace 进入可靠性内核

Artifact 必须绑定：

~~~text
job_id
stage_id
task_id
attempt_id
generation
artifact_id
checksum
state
~~~

只有被接受的 Attempt 产生的 Artifact 才能进入下一 Stage、Job Result 或正式下载出口。

建议生命周期：

~~~text
UPLOADING -> STAGED -> ACCEPTED -> PUBLISHED
                     \-> REJECTED / ORPHANED
~~~

Workspace 至少记录 Attempt ownership、generation、状态、保留策略和 cleanup 状态。重试不能让新旧 Attempt 无约束共享可写 Workspace。

### 3. Runtime 与 Tool 分层

Runtime 是 Agent 执行载体：

~~~text
Runtime
├── Codex
├── Claude
└── Custom Agent
~~~

Tool 是 Runtime / Agent 可调用能力：

~~~text
Tool
├── Shell
├── Git
├── Browser
├── MCP
├── HTTP/API
└── Verifier
~~~

分别定义 RuntimeCapability 与 ToolCapability。

允许某些 Job 直接执行 Tool Executor，但不能因此在领域模型上把所有 Tool 都等价为 Agent Runtime。

### 4. Scheduler 走 Agent-aware，而不是通用资源编排

调度信号优先级面向 Agent Job：

~~~text
1. Runtime / Agent Capability
2. Security / Permission
3. Credential / Account Availability
4. Workspace / Repository Affinity
5. Network / Tool Reachability
6. Queue / Concurrency
7. CPU / Memory / Disk
8. Optional Accelerator
~~~

CPU、Memory、GPU 等资源是约束和评分信号，但不是产品中心。

长期 Scheduler 应理解：

- Agent Runtime/version；
- Tool Capability；
- Credential / account concurrency；
- Workspace / repository affinity；
- network reachability；
- sandbox/isolation；
- warm Runtime；
- queue/load；
- optional compute resources。

### 5. G1 模型网关降级为 Runtime Support Adapter

v0.2 已实现的 Responses/SSE/compact 网关可以保留，但长期定位为：

> **Agent Runtime Support Adapter**

它只服务于 Agent Runtime 执行需要，不扩展为通用 LLM Gateway、Model Router、Provider Gateway 或推理平台。

## Consequences

正面影响：

- Job -> Stage -> Task -> Attempt 能自然承载现有 Map/Reduce 和有限编排；
- Retry、Artifact、Workspace 的正确性进入同一可靠性模型；
- Runtime 和 Tool 生态可独立扩展；
- Scheduler 差异化集中在 Agent-aware 语义，而不是与通用 GPU/容器调度平台竞争；
- G1 不再成为产品范围重新膨胀的入口。

代价：

- Stage 需要新的 schema / API 兼容设计；
- Artifact / Workspace 生命周期必须比原路线更早稳定；
- Scheduler 的 capability / credential / affinity 数据模型需要明确版本；
- Runtime / Tool contract tests 需要分别建设。

## Non-goals

本 ADR 不引入：

- 通用 DAG / Workflow DSL；
- Model Serving；
- KV Cache Fabric；
- GPU Cloud；
- Training Scheduler；
- 通用 LLM Gateway。

## Canonical Direction

当前长期方向以以下文档为准：

- [Agent Job Executor 总体架构](../design/agent-job-executor-architecture.md)
- [长期演进路线图](../implementation/long-term-roadmap.md)
- ADR-003
- ADR-004
