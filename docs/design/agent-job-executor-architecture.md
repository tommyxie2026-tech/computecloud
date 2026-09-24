# Agent-aware Distributed Job Execution Platform 总体架构

- 项目：computecloud
- 日期：2026-09-24
- 产品类别：**Agent Job Executor**
- 长期定位：**Agent-aware Distributed Job Execution Platform**
- 当前实现基线：v0.2.0
- 长期路线：[长期演进路线图](../implementation/long-term-roadmap.md)
- 产品边界：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)
- 执行语义：[ADR-004](../adr/0004-agent-aware-execution-semantics.md)

## 1. 产品定位

computecloud 专注一个问题：

> **可靠地把 Agent Job 分配到远程 Worker 执行，并原生理解 Agent Runtime、Tool、Credential、Workspace、Repository、Approval、Artifact provenance 等执行语义。**

它不是通用模型推理平台、GPU 云、通用 Workflow Engine 或 AI Execution OS。

“Agent-aware”是长期差异化：普通 Job Scheduler 只知道进程和资源，computecloud 还要知道这个 Job 使用什么 Agent、什么 Tool、什么账号、什么 Workspace、什么仓库和什么安全边界。

典型场景包括：

- 远程 Codex / Claude 任务；
- 代码分析、修改、测试和 Review；
- 多 Agent 分片协作；
- Map/Reduce 与有限 fan-out/fan-in；
- CI / 自动化任务；
- 长时间 Agent Job；
- Shell / Git / Browser / MCP / HTTP 等受控 Tool；
- 企业内部批处理 Agent。

## 2. 明确非目标

当前长期路线不承担：

- Model Serving / Deployment 平台；
- KV Cache / Memory Fabric；
- Training Scheduler；
- General GPU Cloud；
- Kubernetes 替代品；
- Temporal / Airflow 类通用 Workflow 平台；
- 通用 LLM Gateway；
- 通用分布式存储；
- 所有 AI Workload 的统一控制面。

如果未来确实存在这些需求，应建立独立系统，通过 Agent Job API 复用 computecloud，而不是改变本系统的领域模型。

## 3. 核心领域模型

长期稳定模型：

~~~text
Job
├── Stage
│   ├── Task
│   │   └── Attempt
│   └── Task
│       └── Attempt
└── Stage
    └── Task
        └── Attempt

Artifact / Workspace
    ↕
Attempt generation
~~~

语义：

- **Job**：调用方提交的一次完整 Agent 业务任务；
- **Stage**：Job 内有限执行阶段和同步边界；
- **Task**：可独立调度的逻辑工作单元；
- **Attempt**：Task 的一次真实执行尝试；
- **Artifact**：Attempt 产生且经过接受流程的结果；
- **Workspace**：Attempt 使用的可写执行环境；
- **Runtime**：Codex、Claude、自研 Agent 等 Agent 执行载体；
- **Tool**：Shell、Git、Browser、MCP、HTTP、Verifier 等 Agent 可调用能力；
- **Worker**：承载 Runtime / Tool 的远程执行节点。

核心不变量：

> **Stage != Task，Task != Attempt。**

Map/Reduce 自然映射为 Map Stage + Reduce Stage；fan-out / barrier / fan-in 仍是有限 Stage 组合，而不是通用 DAG DSL。

## 4. 总体架构

~~~mermaid
flowchart TB
    C["调用方<br/>CLI / HTTP API / MCP / CI / Agent"] --> G

    subgraph S["computecloud Server"]
        G["Job Gateway<br/>Auth / Project / Token / Idempotency"]
        JC["Job Controller<br/>Job / Stage / Task"]
        SCH["Agent-aware Scheduler<br/>Capability / Credential / Affinity / Queue"]
        ST["State Store<br/>SQLite by default"]
        RR["Worker & Runtime Registry"]
        AM["Artifact / Workspace Metadata"]
        Q["Quota / Policy / Audit"]

        G --> JC
        JC --> SCH
        JC <--> ST
        SCH <--> ST
        SCH <--> RR
        JC <--> AM
        G --> Q
        SCH --> Q
    end

    SCH <-->|"主动 gRPC 控制流"| W1["Worker A"]
    SCH <-->|"主动 gRPC 控制流"| W2["Worker B"]
    SCH <-->|"主动 gRPC 控制流"| WN["Worker N"]

    subgraph WX["Worker Execution Plane"]
        R["Agent Runtime<br/>Codex / Claude / Custom Agent"]
        T["Tools<br/>Shell / Git / Browser / MCP / HTTP / Verifier"]
        WS["Workspace"]
        R --> T
        R --> WS
        T --> WS
    end

    W1 --> R
    W2 --> R
    WN --> R

    AM --> AS["Artifact Storage Provider<br/>Local / Shared File / Object"]
    OBS["Events / Metrics / Trace / Audit"]
    OBS -.-> G
    OBS -.-> JC
    OBS -.-> SCH
    OBS -.-> WX
~~~

## 5. 主执行流程

~~~text
Submit Job
   ↓
Auth / Project / Idempotency / Quota
   ↓
Job Controller
   ↓
Create Stage / Tasks
   ↓
Agent-aware Scheduler
   ↓
Runtime + Tool + Credential + Affinity Match
   ↓
Create Attempt generation
   ↓
Worker lease / fencing
   ↓
Agent Runtime
   ↓
Tool Calls / Workspace
   ↓
Artifact STAGED
   ↓
Verifier / Completion CAS
   ↓
Artifact ACCEPTED / PUBLISHED
   ↓
Next Stage or Job Result
~~~

## 6. Runtime 与 Tool 必须分层

### 6.1 Runtime

Runtime 是 Agent 执行载体：

~~~text
Runtime
├── Codex
├── Claude
└── Custom Agent
~~~

Runtime 主要能力：

- prepare；
- start；
- inspect；
- stop；
- structured / streamed output；
- session resume（可选）；
- interactive input / approval（可选）；
- workspace checkpoint（可选）。

### 6.2 Tool

Tool 是 Runtime / Agent 调用的外部能力：

~~~text
Tool
├── Shell
├── Git
├── Browser
├── MCP
├── HTTP/API
└── Verifier
~~~

分别维护 RuntimeCapability 和 ToolCapability。

某些特殊 Task 可以直接调用 Tool Executor，但不能因此把所有 Tool 都建模成 Runtime。

## 7. Agent-aware Scheduler

Scheduler 不以 CPU/GPU Bin Packing 为中心，而以 Agent Job 可执行性为中心。

建议 Filter / Score 的优先顺序：

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

### 7.1 Filter

硬条件：

- Runtime/version；
- Tool Capability；
- project permission；
- credential/account；
- repository access；
- network reachability；
- isolation level；
- Worker labels；
- resource floor。

### 7.2 Queue / Fairness

- priority；
- project fair share；
- account concurrency；
- job concurrency；
- aging；
- backpressure。

### 7.3 Score

- Worker load；
- Workspace affinity；
- Repository affinity；
- warm Runtime；
- credential/account availability；
- historical latency；
- failure rate；
- optional resource headroom。

### 7.4 Bind

最终形成：

~~~text
Task
  ↓
Attempt generation
  ↓
Worker
  ↓
Runtime
  ↓
Credential / Tool context
~~~

调度结果应可解释：至少能够给出候选 Worker、拒绝原因和主要选中因素。

## 8. Stage：有限编排而不是 Workflow 平台

支持方向：

~~~text
single
map_reduce
fan_out
barrier
fan_in
bounded_stages
conditional_verifier
~~~

明确不做：

- 任意循环语言；
- 通用 Workflow DSL；
- 复杂动态子流程；
- 为替代 Temporal / Airflow 而设计的调度语义。

Stage 只解决 Agent Job 内“阶段、并行、屏障、汇总和验证”。

## 9. Artifact / Workspace 是可靠性内核

Artifact 不只是文件存储对象，而是 Attempt 提交正确性的一部分。

至少绑定：

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

建议生命周期：

~~~text
UPLOADING
   ↓
STAGED
   ↓
ACCEPTED
   ↓
PUBLISHED

STAGED -> REJECTED / ORPHANED
~~~

只有当前有效 generation 的 ACCEPTED Artifact 才能被下一 Stage 或 Job Result 使用。

Workspace 至少记录：

- owner Attempt / generation；
- repository baseline；
- writable state；
- cleanup state；
- retention / TTL；
- disk quota；
- checkpoint（Runtime 支持时）。

旧 Attempt 和新 Attempt 不得无约束共享同一个可写 Workspace。

## 10. 可靠性模型

长期最重要的系统能力仍然是：

- idempotent submit；
- command_id 去重；
- Attempt generation；
- lease / heartbeat；
- generation fencing；
- cancel tombstone；
- completion CAS；
- Artifact checksum / acceptance；
- Server restart recovery；
- Worker restart recovery；
- unknown execution reconciliation；
- side-effect-aware retry。

核心原则：

> **宁可进入 RECONCILING，也不能因为网络、重启或重试制造两个有效执行结果。**

## 11. G1 的长期定位

v0.2 已有 Responses / SSE / compact 网关能力，可以保留，但长期定位为：

> **Agent Runtime Support Adapter**

它用于满足某些 Agent Runtime 的模型访问需求，不扩展成独立的 LLM Gateway、Model Router 或 Provider Gateway 产品线。

## 12. Worker

Worker 负责：

- 主动连接；
- identity / version；
- RuntimeCapability；
- ToolCapability；
- labels / isolation；
- resource snapshot；
- Start / Stop / Inspect；
- Workspace；
- process supervision；
- heartbeat / lease；
- event buffering；
- Artifact upload；
- crash recovery。

Worker 不做全局调度。

## 13. 安全与治理

需要围绕 Agent Job 建立：

- Project / Tenant identity；
- User / Service Account；
- Worker identity；
- CredentialRef / SecretRef；
- Runtime permission；
- Tool permission；
- repository allowlist；
- network policy；
- isolation capability；
- Artifact ownership；
- Audit。

执行不可信代码时使用 Container / VM / Sandbox，不把普通进程当成安全边界。

## 14. 可观测性

统一关联：

~~~text
request_id
job_id
stage_id
task_id
attempt_id
generation
worker_id
runtime
credential_ref
artifact_id
~~~

至少观测：

- submit / queue / scheduling / start latency；
- stage duration；
- runtime duration；
- retry / reconcile；
- cancellation latency；
- Worker availability；
- account/credential blocker；
- Artifact lifecycle；
- Workspace cleanup；
- SQLite write / busy；
- Server / Worker CPU/RSS；
- Job success / failure / cancel。

## 15. 长期定位总结

computecloud 的长期产品定义：

> **Agent-aware Distributed Job Execution Platform：一个理解 Agent 执行语义、面向多 Worker 的轻量可靠作业执行平台。**

它持续增强：

~~~text
可靠性
→ Agent Runtime / Tool 生态
→ Agent-aware Scheduling
→ 企业治理
→ 大规模运行与按需 HA
~~~

而不是通过扩大领域模型变成通用 AI 基础设施。
