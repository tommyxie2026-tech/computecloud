# Agent Job Executor 总体架构

- 项目：computecloud
- 日期：2026-09-24
- 定位：**轻量、可靠、可扩展的分布式 Agent Job Executor**
- 当前实现基线：v0.2.0
- 长期路线：[长期演进路线图](../implementation/long-term-roadmap.md)
- 决策依据：[ADR-003：Agent Job Executor 产品边界](../adr/0003-agent-job-executor-product-scope.md)

## 1. 产品定位

computecloud 的核心不是模型推理平台、GPU 云或通用 AI Execution OS，而是：

> **把 Codex、Claude 以及后续其他 Agent / Tool Runtime 作为远程执行器，统一完成 Job 提交、拆分、调度、执行、取消、恢复、产物交接和治理。**

核心问题是：一个 Agent Job 进入系统后，如何可靠地把它分配给合适的 Worker / Runtime，并在跨节点、失败、重启、取消和长时间执行场景下保持状态一致、结果可追溯、产物可交接。

典型场景包括远程 Codex / Claude 任务、代码分析与修改、多 Agent 分片协作、显式 Map/Reduce、CI 自动化、长时间 Agent Job，以及 Shell / Browser / MCP 等受控工具任务。

## 2. 明确非目标

当前路线不以以下能力为目标：

- 通用大模型 Serving / 推理平台；
- 模型 Deployment / Model Registry 平台；
- KV Cache / Prefix Cache / Memory Fabric；
- 训练调度平台；
- 通用 GPU 云；
- Kubernetes 替代品；
- Temporal / Airflow 类通用 Workflow 平台；
- 通用分布式存储系统；
- 所有 AI Workload 统一调度的 AI Execution OS。

如果未来出现独立业务需求，应作为独立产品或独立架构评估，而不是强行塞入 Agent Job Executor。

## 3. 核心领域模型

~~~text
Job
├── Task
│   └── Attempt
├── Task
│   └── Attempt
└── Artifact
~~~

其中：

- Job：调用方提交的一次完整业务任务；
- Task：Job 内一个独立可调度工作单元；
- Attempt：Task 的一次真实执行尝试；
- Artifact：Attempt / Job 产生的可交付结果；
- Workspace：执行任务使用的可写工作目录；
- Runtime：Codex、Claude、Shell、Browser 等真实执行器；
- Worker：承载 Runtime 的远程执行节点。

核心原则：**Task != Attempt**。业务任务与执行尝试必须分离，重试只创建新的 Attempt，不创建新的业务 Task。

## 4. 总体架构

~~~mermaid
flowchart TB
    C["调用方<br/>CLI / API / Codex / CI / MCP"] --> G

    subgraph S["computecloud Server"]
        G["Job Gateway<br/>HTTP / MCP / Token / Auth"]
        JC["Job Controller<br/>single / Map-Reduce / bounded stages"]
        SCH["Scheduler<br/>filter / fair queue / capability / quota"]
        ST["Store<br/>SQLite by default"]
        AM["Artifact & Workspace Manager"]
        RR["Runtime / Worker Capability Registry"]
        G --> JC
        JC --> SCH
        JC <--> ST
        SCH <--> ST
        SCH <--> RR
        JC <--> AM
    end

    SCH <-->|"主动 gRPC 控制流"| W1["Worker A<br/>Runtime Adapters<br/>Workspace"]
    SCH <-->|"主动 gRPC 控制流"| W2["Worker B<br/>Runtime Adapters<br/>Workspace"]
    SCH <-->|"主动 gRPC 控制流"| WN["Worker N<br/>Runtime Adapters<br/>Workspace"]

    AM --> AS["Artifact Storage<br/>Local / Shared / Object Provider"]
    OBS["Observability & Governance<br/>Events / Metrics / Audit / Quota"]
    OBS -.-> G
    OBS -.-> JC
    OBS -.-> SCH
~~~

## 5. 主执行流程

~~~text
Submit Job
   ↓
Auth / Project / Quota / Idempotency
   ↓
Job Controller
   ↓
Task Expansion
   ↓
Scheduler
   ↓
Worker Capability Match
   ↓
Attempt Lease
   ↓
Runtime Adapter
   ↓
Codex / Claude / Tool
   ↓
Artifact / Result / Events
   ↓
Verify
   ↓
Job Result
~~~

## 6. Server 职责

Server 负责控制面，不执行 Agent 本身。主要模块包括 API/MCP、Job Controller、Scheduler、Store、Worker Registry、Runtime Capability Registry、Artifact Metadata、Quota、Audit 和 Recovery。

Server 不应该演变成模型推理代理大杂烩、文件字节流代理、Shell 执行节点或通用 Workflow Engine。

## 7. Worker 职责

Worker 是执行面，负责主动连接 Server、注册版本与 Capability、接收 Start/Stop/Inspect、管理 Workspace、启动 Runtime、进程组监督、事件暂存与补传、lease/heartbeat、Artifact 打包上传和故障后的本地恢复。

Worker 不负责全局调度。

## 8. Runtime Adapter

Runtime 是最重要的扩展点之一。长期围绕 Agent Job 执行抽象：

~~~text
Prepare
Start
Inspect
Stop
Capabilities
Version
~~~

可选能力通过 capability 声明，例如 structured_output、stream_output、session_resume、interactive_input、approval、workspace_checkpoint、container、browser、network。

Scheduler 按 capability 匹配，不按 Runtime 名称写大量特殊逻辑。

## 9. Scheduler 演进边界

Scheduler 的目标是 Agent Job 调度，而不是 AI 全栈资源编排。

~~~text
Filter
  runtime capability
  worker labels
  project permission
  account quota
  resource availability

Queue / Fairness
  priority
  project fairness
  account concurrency
  job concurrency

Score
  worker load
  workspace affinity
  repository affinity
  runtime warm state
  historical latency

Bind
  task -> attempt -> worker
~~~

CPU、Memory、可选 GPU 可以成为 Worker capability 和资源约束，但仅服务 Agent Job，不因此扩展为模型 Serving 调度平台。

## 10. Job 编排边界

v0.2 已有 single 和 map_reduce。长期可以增加 fan-out、barrier、fan-in、bounded stage、conditional verifier 等**有限编排**，但不发展无限制 Workflow DSL。

判断原则：编排能力必须直接解决 Agent Job 执行与交付问题，而不是为了成为通用工作流系统。

## 11. Workspace 与 Artifact

重点不是构建 Storage Fabric，而是保证 Job 输入与结果可交接。

长期对象包括 Workspace、Input Package、Artifact、Report、Patch、Log、Test Result，以及 Runtime 支持时的 Checkpoint。

存储通过 Local、Shared File、Object 等 Provider 抽象。公共协议只引用 Artifact ID / URI / checksum，不暴露具体私有存储实现。

## 12. 可靠性模型

必须持续保持：

- 幂等 Submit；
- command_id 去重；
- Attempt generation；
- lease；
- heartbeat；
- fencing；
- cancel tombstone；
- completion CAS；
- Artifact checksum；
- Server restart recovery；
- Worker restart recovery；
- unknown execution reconciliation；
- 不确定副作用任务不自动重放。

核心原则：

> **宁可明确进入 RECONCILING，也不能制造双执行。**

## 13. 安全边界

安全能力围绕 Agent Job：User / Project Token、Worker identity、RBAC、CredentialRef、Runtime permission、repository allowlist、Tool permission、network policy、Workspace isolation、Audit 和 Artifact ownership。

执行不可信代码时应使用 Container / VM / Sandbox，而不是依赖普通进程隔离。

## 14. 可观测性

统一关联：

~~~text
request_id
job_id
task_id
attempt_id
worker_id
runtime
artifact_id
~~~

至少观测 submit latency、queue latency、scheduling latency、task start latency、runtime duration、cancellation latency、retry/reconcile、Worker availability、Artifact bytes、SQLite write/busy、Server/Worker CPU/RSS，以及 Job success/failure/cancel rate。

## 15. 产品边界总结

computecloud 的长期定义：

> **Agent Job Executor：一个面向 Codex、Claude 和其他 Agent / Tool Runtime 的轻量分布式作业执行系统。**

能力增长方向是可靠性、调度能力、Runtime 生态、Tool 生态、资源感知、多租户治理和规模化可用性，而不是不断扩大领域模型，把自己强行演变成另一套 AI 基础设施。
