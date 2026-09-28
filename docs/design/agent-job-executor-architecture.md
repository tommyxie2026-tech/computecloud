# Agent-aware Distributed Job Execution Platform 总体架构

- 项目：computecloud
- 日期：2026-09-24
- 产品类别：**Agent Job Executor**
- 长期定位：**Agent-aware Distributed Job Execution Platform**
- 当前实现基线：v0.2.0
- 长期路线：[长期演进路线图](../implementation/long-term-roadmap.md)
- 产品边界：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)
- 执行语义：[ADR-004](../adr/0004-agent-aware-execution-semantics.md)\n- 计算模型：[ADR-017 Goal-oriented Computing](../adr/0017-goal-oriented-computing-model.md)

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

## 3. 核心计算模型

computecloud 的最高层计算模型升级为 **Goal-oriented Computing**：

~~~text
Goal
  ↓
Plan
  ↓
Execution Graph
  ↓
Scheduler
  ↓
Worker
  ↓
Artifact
  ↓
Evaluator
  ↓
Re-plan
  └────────→ Plan / Execution Graph next generation
~~~

其中原有模型不废弃，而是作为下层可靠执行内核：

~~~text
Goal-oriented Computing Layer
Goal -> Plan -> Execution Graph -> Evaluator -> Re-plan
                         |
                         v
Durable Execution Layer
Job -> Stage -> Task -> Attempt -> Artifact / Workspace
                         |
                         v
Execution Plane
Scheduler -> Worker -> Runtime / Tool / Environment
~~~

映射关系：

~~~text
Goal
 └── Plan revision
      └── Execution Graph generation
           └── Graph Node
                └── Job / Stage
                     └── Task
                          └── Attempt
~~~

语义：

- **Goal**：调用方希望真正达成的目标、约束和验收标准；
- **Plan**：针对 Goal 的可版本化求解策略；
- **Execution Graph**：Plan 编译后的受控、版本化、可恢复执行图；
- **Job / Stage / Task / Attempt**：Execution Graph Node 的可靠执行基元；
- **Artifact**：执行产物与 Evaluator 使用的证据对象；
- **Evaluator**：依据 Goal acceptance criteria 对结果做结构化判断；
- **Re-plan**：在 Goal 不变时产生新的 Plan revision 与 Graph generation；
- **Workspace**：Attempt 使用的可写执行环境；
- **Runtime**：Codex、Claude、自研 Agent 等执行载体；
- **Tool**：Shell、Git、Browser、MCP、HTTP、Verifier 等能力；
- **Worker**：承载 Runtime / Tool / Environment 的远程执行节点。

核心不变量：

> **Goal != Plan，Plan revision != Execution Graph generation，Stage != Task，Task != Attempt，Retry != Re-plan。**

Map/Reduce、fan-out / barrier / fan-in 继续作为 Execution Graph 的有限图模式，而不是最高层计算模型，也不扩张为通用 Workflow DSL。

## 4. 总体架构

~~~mermaid
flowchart TB
    C["调用方<br/>CLI / HTTP API / MCP / CI / Agent / Upstream AI OS"] --> G

    subgraph S["computecloud Server"]
        G["Goal / Job Gateway<br/>Auth / Project / Idempotency / Policy"]
        GC["Goal Controller<br/>Goal / Acceptance / Lifecycle"]
        P["Planner Provider<br/>Plan Revision"]
        EG["Execution Graph Controller<br/>Compile / Generation / Ready Nodes"]
        JC["Durable Job Controller<br/>Job / Stage / Task / Attempt"]
        SCH["Agent-aware Scheduler<br/>Capability / Credential / Environment / Affinity / Queue"]
        EV["Evaluator Provider<br/>Evidence / Verdict / Re-plan Trigger"]
        ST["State Store<br/>SQLite by default"]
        RR["Worker / Runtime / Tool / Environment Registry"]
        AM["Artifact / Workspace Metadata"]
        Q["Quota / Policy / Audit"]

        G --> GC
        GC --> P
        P --> EG
        EG --> JC
        JC --> SCH
        JC <--> ST
        GC <--> ST
        EG <--> ST
        EV <--> ST
        SCH <--> ST
        SCH <--> RR
        JC <--> AM
        AM --> EV
        EV --> GC
        EV -->|RE_PLAN| P
        G --> Q
        SCH --> Q
        EV --> Q
    end

    SCH <-->|"主动 gRPC 控制流"| W1["Worker A"]
    SCH <-->|"主动 gRPC 控制流"| W2["Worker B"]
    SCH <-->|"主动 gRPC 控制流"| WN["Worker N"]

    subgraph WX["Worker Execution Plane"]
        R["Agent Runtime<br/>Codex / Claude / Custom Agent"]
        T["Tools<br/>Shell / Git / Browser / MCP / HTTP / Verifier"]
        ENV["Environment<br/>Process / Container / VM / Provider"]
        WS["Workspace"]
        R --> T
        R --> ENV
        R --> WS
        T --> WS
    end

    W1 --> R
    W2 --> R
    WN --> R

    AM --> AS["Artifact Storage Provider<br/>Local / Shared File / Object"]
    OBS["Events / Metrics / Trace / Audit"]
    OBS -.-> G
    OBS -.-> GC
    OBS -.-> EG
    OBS -.-> JC
    OBS -.-> SCH
    OBS -.-> EV
    OBS -.-> WX
~~~

## 5. 主执行流程

~~~text
Submit Goal
   ↓
Auth / Project / Idempotency / Policy / Budget
   ↓
Goal Controller
   ↓
Create / Select Plan revision
   ↓
Compile Execution Graph generation
   ↓
Select Ready Graph Node
   ↓
Materialize Job / Stage / Task
   ↓
Agent-aware Scheduler
   ↓
Runtime + Tool + Credential + Environment + Affinity Match
   ↓
Create Attempt generation
   ↓
Worker lease / fencing
   ↓
Agent Runtime / Tool Calls / Workspace
   ↓
Artifact STAGED -> ACCEPTED
   ↓
Evaluator
   ├── ACCEPT ------> Verified Outcome / Goal SUCCEEDED
   ├── RETRY -------> same Plan/Graph, new Attempt generation
   ├── RE_ROUTE ----> Scheduler
   ├── RE_PLAN -----> new Plan revision + Graph generation
   ├── APPROVAL ----> Human / Policy Gate
   └── FAIL --------> Goal FAILED
~~~

这里必须严格区分：

~~~text
Retry
= same Goal
+ same Plan revision
+ same Execution Graph generation
+ same logical Task
+ new Attempt generation

Re-plan
= same Goal
+ new Plan revision
+ new Execution Graph generation
~~~

旧 Graph generation 与旧 Attempt generation 的迟到结果都必须经过 fencing，不能污染当前 Goal 的最终 Verified Outcome。

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


## 16. Goal-oriented Computing 扩展原则

自 ADR-017 起，任何新的执行能力优先判断它属于哪一层：

~~~text
Goal / Plan Layer
  objective / constraints / acceptance / plan revision

Execution Graph Layer
  dependency / node / graph generation / bounded dynamic expansion

Durable Execution Layer
  Job / Stage / Task / Attempt / retry / fencing

Execution Plane
  Scheduler / Worker / Runtime / Tool / Environment / Workspace

Evidence Layer
  Artifact / provenance / verifier evidence

Feedback Layer
  Evaluator / approval / retry / re-route / re-plan
~~~

Planner 和 Evaluator 的智能实现采用 Provider 边界；computecloud 负责持久化契约、状态机、可靠执行与审计，不要求所有智能能力都内置。