# Agent-aware Distributed Job Execution 产品调研报告（2026）

- 项目：computecloud
- 日期：2026-09-24
- 研究对象：与 Agent-aware Distributed Job Execution Platform 相邻或重叠的 Agent 平台、Coding Agent、Sandbox、Durable Workflow、Remote Job 产品
- 目标：识别竞争格局、可借鉴能力、产品空位，以及对 computecloud 长期路线的影响
- 关联架构：[Agent-aware 总体架构](../design/agent-job-executor-architecture.md)
- 关联路线：[长期演进路线图](../implementation/long-term-roadmap.md)
- 关联决策：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)、[ADR-004](../adr/0004-agent-aware-execution-semantics.md)

> 本文记录的是公开产品与公开文档层面的产品调研，不等同于对所有产品完成部署、源码或性能实测。产品能力会持续变化，重大版本决策前需要重新核对官方资料。

## 1. 调研结论

行业已经出现四类明显产品：

| 类别 | 代表产品 | 与 computecloud 的关系 |
| --- | --- | --- |
| Agent 执行 / Agent Runtime 平台 | OpenHands、OpenAI Agents API、Microsoft Agent Framework、LangGraph Agent Server | 架构与能力部分重叠 |
| Coding Agent Cloud | Cursor Cloud Agents、GitHub Copilot Cloud Agent、Codex、Jules | 产品体验和远程 Job 模式接近 |
| Agent Sandbox / Execution Infra | Daytona、E2B、Modal | 更适合作为 Worker / Sandbox Provider |
| Durable Job / Workflow Infra | Temporal、Ray Jobs | 可靠性与远程 Job 机制值得借鉴 |

目前没有一个公开产品与 computecloud 的目标完全重合：

> **Self-hosted + Multi-Agent Runtime + Durable Job Semantics + Agent-aware Scheduling + Enterprise Governance**

因此 computecloud 不需要与现有 Agent 产品正面竞争，而应该把长期差异化固定在：

~~~text
Agent Runtime diversity
+
Job / Stage / Task / Attempt
+
Attempt fencing / Retry Safety
+
Artifact provenance / Workspace lifecycle
+
Credential-aware / Tool-aware / Repo-aware scheduling
+
Self-hosted / Private infrastructure
~~~

## 2. 市场分层

建议把行业理解为四层：

~~~text
Agent / Harness Layer
────────────────────────────────
Codex
Claude Code
OpenHands
OpenAI Agents API
Microsoft Agent Framework
Custom Agent

Agent Job Control Layer
────────────────────────────────
computecloud
Agent-aware Distributed Job Execution

Sandbox / Runtime Infrastructure
────────────────────────────────
Daytona
E2B
Modal
Docker
VM
Kubernetes

Compute / Infrastructure
────────────────────────────────
Bare Metal
Cloud VM
Private Cloud
Kubernetes
~~~

computecloud 应保持在 Agent Job Control Layer，不主动向上重做 Agent Harness，也不主动向下重做完整 Sandbox Cloud / GPU Cloud。

## 3. OpenHands

### 产品方向

OpenHands 已经从单一 Coding Agent 演进出 Agent Server / Agent Canvas / Sandbox 等能力，并支持多个 Agent backend。公开文档显示其架构可以连接本地、Docker、VM、Cloud 等执行环境，也可接入第三方 Agent。

官方资料：

- https://github.com/OpenHands/docs
- https://docs.openhands.dev/

### 与 computecloud 的重叠

~~~text
OpenHands
Agent / Conversation / Sandbox first

computecloud
Job / Attempt / Scheduler / Reliability first
~~~

重叠点包括多 Agent Runtime、Remote backend、Docker / VM、Workspace、Tool、Self-hosted 和自动化任务。

差异在于 OpenHands 更偏 Agent / Conversation / Sandbox；computecloud 更偏 Job / Stage / Task / Attempt / durable execution。computecloud 不需要重新实现 Agent reasoning 或 Agent UI。

### 借鉴

优先研究 Agent backend 抽象、Agent Server 边界、Sandbox backend、Agent protocol / capability、第三方 Agent 接入方式。

不建议复制 conversation-first 领域模型，也不把产品核心放在 Agent UI。

## 4. OpenAI Agents API

OpenAI Agents API 的公开定位是创建和运行 cloud agents，覆盖长时间运行、工具、代码执行、文件和环境，并可使用 hosted sandbox 或外部执行基础设施。

官方资料：

- https://openai.com/index/introducing-the-agents-api/

它验证了一个重要趋势：

> Agent Harness 与 Environment / Execution Infrastructure 正在解耦。

computecloud 不应重做 Codex / Agent Harness，而应该允许把这类托管 Agent 服务也接成 Runtime：

~~~text
Runtime
├── codex-cli
├── claude-code
├── openhands
├── managed-agent-api
└── custom-agent
~~~

computecloud 的价值继续放在 where to run、when to run、credential / quota、Worker selection、retry safety、Artifact validity 和 enterprise policy。

## 5. Microsoft Agent Framework / Durable Extension

Microsoft Agent Framework 和 Durable Extension 强调 Agent、Tool、Session、Workflow、checkpoint、failure recovery、distributed worker 和 human-in-the-loop 等能力。

官方资料：

- https://learn.microsoft.com/en-us/agent-framework/

它和 computecloud 的可靠性问题高度相关：

~~~text
Microsoft
Durable Agent / Workflow

computecloud
Durable Agent Job Execution
~~~

值得重点借鉴 checkpoint、durable state、failure recovery、human-in-the-loop、long-running execution 和 distributed workers。

但 computecloud 不应扩展成完整 Agent Framework。

核心区别保持：

~~~text
Microsoft:
Agent 是框架内部对象

computecloud:
Agent 是被调度的 Runtime
~~~

## 6. Cursor Cloud Agents

Cursor Cloud Agents 代表成熟的“远程 Coding Agent Job”产品体验：远程环境、repo、依赖、secret、network access、长时间异步运行、日志和最终代码结果。

官方资料：

- https://www.cursor.com/
- https://docs.cursor.com/

最值得借鉴的不是 IDE，而是 Environment / Workspace：

~~~text
Repository
+
Dependencies
+
Runtime
+
Tools
+
Secrets
+
Network policy
      ↓
Prepared Workspace
~~~

这与 computecloud 的 Workspace Lifecycle、RuntimeCapability、CredentialRef、ToolCapability 高度一致。

长期可考虑 Workspace Template、warm workspace、repo baseline、dependency preload、reproducible environment 和 artifact evidence。

## 7. GitHub Copilot Cloud Agent

GitHub Copilot Cloud Agent 代表 Issue / Prompt -> Agent Job -> Branch / PR 的异步任务产品。

官方资料：

- https://docs.github.com/en/copilot/

第三方 Agent 接入方向也说明未来企业很可能同时使用多个 Coding Agent。

Runtime abstraction 应保持通用：

~~~text
Job
  ↓
runtime requirement
  ↓
Codex / Claude / Custom / Other Agent
~~~

不要设计成 /run-codex、/run-claude 这类产品 API。

## 8. Codex / Claude Code / Jules

这些产品更适合作为 computecloud 的 Runtime，而不是 computecloud 要复制的产品。

它们持续增强 background execution、subagent、tool、MCP、checkpoint / session、repository work 和 long-running task。

对 computecloud 的意义是：

> Agent Runtime 会越来越强，执行控制层更不应该重新实现 Agent 本身。

computecloud 应负责：

~~~text
where to run
when to run
which runtime
which credential
which workspace
which tools
whether retry is safe
which artifact is accepted
~~~

## 9. Daytona / E2B / Modal

这三类产品代表 Agent Sandbox / Execution Infrastructure。

Daytona：
- https://www.daytona.io/
- https://www.daytona.io/docs/

E2B：
- https://e2b.dev/

Modal：
- https://modal.com/
- https://modal.com/docs/

它们更适合承担 Sandbox lifecycle，而 computecloud 负责 Agent Job lifecycle。

未来可以抽象：

~~~text
SandboxProvider
├── local-process
├── container
├── vm
├── kubernetes
├── daytona
├── e2b
└── modal
~~~

computecloud 不需要自己发展完整 Sandbox Cloud。

## 10. LangGraph / LangSmith Agent Server

LangGraph / LangSmith 的核心更偏 Agent Application Runtime / Workflow：Graph、Node、State、Thread、Run、Checkpoint，并提供后台 worker 和持久状态能力。

官方资料：

- https://docs.langchain.com/langgraph/
- https://docs.langchain.com/langsmith/

核心区别：

~~~text
LangGraph
Graph / State / Thread / Run

computecloud
Job / Stage / Task / Attempt / Worker
~~~

LangGraph 更适合应用内部 workflow；computecloud 更适合调度外部 Agent Runtime。

不建议 computecloud 为追赶 LangGraph 而发展通用 Graph DSL。

## 11. Temporal

Temporal 不是 Agent 产品，但其 Durable Execution 是 computecloud 可靠性设计的重要参照。

官方资料：

- https://temporal.io/
- https://docs.temporal.io/

值得借鉴 durable state、activity retry、heartbeat、timeout、cancellation、idempotency 和 crash recovery。

对应关系：

| Temporal | computecloud |
| --- | --- |
| Workflow | Job / Stage |
| Activity | Task / Attempt |
| Retry | Retry Safety |
| Heartbeat | Attempt heartbeat |
| Durability | Store + reconciliation |

不建议直接把 computecloud 发展成通用 Durable Workflow Engine。

## 12. Ray Jobs

Ray Jobs API 是传统 Remote Job 的重要参照：远程提交、提交端断开后继续执行、Runtime Environment、Job status。

官方资料：

- https://docs.ray.io/en/latest/cluster/running-applications/job-submission/

它说明一个风险：

> 如果 computecloud 只做到 Job + Worker + Process，就容易成为“Agent 版本的 Ray Jobs”。

因此 Agent-aware 是关键差异。

## 13. 产品比较矩阵

| 产品 | Agent Runtime | Durable Job | Multi Worker | Sandbox | Tool / Agent Semantics | Artifact / Workspace | Self-hosted | Agent-aware Scheduling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| OpenHands | 强 | 中 | 中 | 强 | 强 | 强 | 强 | 中 |
| OpenAI Agents API | 强 | 强 | 托管 | 强 | 强 | 强 | 部分/BYO | 托管 |
| Microsoft Agent Framework | 强 | 强 | 强 | 中 | 强 | 中 | 强 | 中 |
| Cursor Cloud Agents | 强 | 强 | 托管 | 强 | 强 | 强 | 弱 | 黑盒 |
| GitHub Copilot Agent | 强 | 强 | 托管 | 强 | 强 | 强 | 弱 | 黑盒 |
| Daytona | 弱 | 弱 | 强 | 很强 | 弱 | 环境级 | 强 | 弱 |
| E2B | 弱 | 弱 | 强 | 很强 | 弱 | 环境级 | 部分 | 弱 |
| Modal | 弱 | 中 | 很强 | 强 | 弱 | 环境级 | 弱 | 通用资源 |
| LangGraph | 强 | 强 | 中 | 外部 | 很强 | State/Checkpoint | 强 | Workflow-oriented |
| Temporal | 无 | 很强 | 很强 | 无 | 无 | 外部 | 强 | Workflow-oriented |
| Ray Jobs | 无 | 中 | 很强 | Runtime env | 无 | 弱 | 强 | Resource-oriented |
| computecloud 目标 | 多 Runtime | 很强 | 强 | Provider 化 | **Agent-aware** | **一等可靠性对象** | **强** | **核心能力** |

该表用于产品定位，不作为严格功能认证矩阵；各产品能力以其当前官方文档为准。

## 14. 最值得借鉴的能力

| 产品 | 最值得借鉴 |
| --- | --- |
| OpenHands | Agent backend / protocol / sandbox abstraction |
| OpenAI Agents API | Harness 与 execution environment 解耦 |
| Microsoft Agent Framework | durable execution / checkpoint / HITL |
| Cursor | prepared workspace / environment / evidence UX |
| GitHub Copilot | agent assignment / PR lifecycle / third-party agent |
| Daytona / E2B / Modal | Sandbox Provider 抽象 |
| LangGraph | checkpoint / interrupt / run recovery |
| Temporal | heartbeat / retry / durable correctness |
| Ray Jobs | 简洁 Remote Job API |

## 15. 明确不建议复制

computecloud 不建议复制：

- 自研通用 Agent Harness；
- 通用 Agent Graph / Workflow DSL；
- Sandbox Cloud；
- Model Serving；
- LLM Gateway；
- GPU Cloud；
- IDE / Coding Agent SaaS；
- 通用 Durable Workflow Engine。

这些能力可以作为 Runtime / Provider / Integration 接入。

## 16. 市场空位

目前最值得占据的位置是：

> **Self-hosted + Multi-Agent Runtime + Durable Job Semantics + Agent-aware Scheduling**

具体组合：

~~~text
Codex / Claude / Custom Agent
            ↓
      Runtime API
            ↓
Job -> Stage -> Task -> Attempt
            ↓
Generation / Fencing / Retry Safety
Artifact provenance / Workspace lifecycle
            ↓
Runtime-aware
Tool-aware
Credential-aware
Repo-aware
Workspace-aware
Network-aware
Scheduler
            ↓
Bare Metal / VM / Container / Private Cloud
~~~

这是 computecloud 相比单纯 Agent Framework、Sandbox、Generic Job Scheduler 更清晰的差异化。

## 17. 对长期路线图的影响

调研结果支持当前路线，不建议重新回到 AI Execution OS。

v0.3 继续优先 Stage、Attempt Generation / Fencing、Retry Safety、Artifact Lifecycle、Workspace Lifecycle 和 Long-running Job。

v0.4 重点是 Runtime API v2、RuntimeCapability、ToolCapability、Approval、SessionRef 和 Sandbox Provider。

v0.5 重点从 generic resource-aware 固定为 Agent-aware Scheduler，核心信号为：

~~~text
Runtime
Permission
Credential
Workspace
Repository
Network / Tool
Queue
Resource
~~~

v0.6+ 加强企业治理与规模化，不扩展产品领域。

## 18. 产品定位结论

综合行业格局，computecloud 不应该定义成：

- AI Execution OS；
- GPU Scheduler；
- Workflow Engine；
- Coding Agent；
- Sandbox Cloud。

长期更准确的定义是：

> **computecloud = Agent-aware Distributed Job Execution Platform**

它负责 Agent Job 的提交、拆分、调度、执行、长任务、重试、恢复、Workspace、Artifact、Credential、Tool、安全、审计和多 Worker。

Agent reasoning、模型 Serving、Sandbox Cloud 和通用 Workflow 交给各自专业系统。

## 19. 持续跟踪清单

建议长期跟踪：

1. OpenHands：多 Agent backend 与 self-hosted 执行；
2. OpenAI Agents API：managed agent execution；
3. Microsoft Agent Framework：durable agent / workflow；
4. Cursor Cloud Agents：远程 Agent Job 产品体验；
5. GitHub Copilot Cloud Agent：代码任务交付模型；
6. Daytona / E2B / Modal：Sandbox infra；
7. LangGraph：Agent state / checkpoint；
8. Temporal：durable correctness；
9. Ray Jobs：remote job simplicity。

当出现以下变化时应重新 Review 路线：

- 跨 Agent Runtime 标准化协议成熟；
- Agent Sandbox 成为通用基础设施；
- Durable Agent Job 出现事实标准；
- Agent-aware Scheduling 成为公开产品能力；
- Coding Agent 平台开放企业私有 Worker；
- MCP / ACP 等协议发生重大标准化变化。
