# computecloud Agent-aware Distributed Job Execution Platform 长期路线图

- 项目：computecloud
- 日期：2026-09-27
- 当前稳定发布基线：v0.3.2；main 功能基线：v0.3.3
- 产品类别：**Agent Job Executor**
- 长期定位：**Agent-aware Distributed Job Execution Platform**
- 总体架构：[Agent-aware 总体架构](../design/agent-job-executor-architecture.md)
- 产品边界：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)
- 执行语义：[ADR-004](../adr/0004-agent-aware-execution-semantics.md)
- 当前实现依据：[v0.3.3 Workspace Lifecycle](v0.3.3-plan.md)、[v0.3.3 验证记录](../validation/v0.3.3-results.md)
- 当前实施跟踪：v0.3.0–v0.3.3 代码与自动化已完成；Workspace Lifecycle [Issue #18](https://github.com/tommyxie2026-tech/computecloud/issues/18) 完成；Production Baseline 继续由 [Tracker #9](https://github.com/tommyxie2026-tech/computecloud/issues/9) 跟踪
- 产品调研依据：[Agent-aware 产品与竞品调研（2026）](../research/agent-job-execution-product-landscape-2026.md)
- 客户端路线依据：[Control 客户端技术方案](../design/client-control-plane.md)、[ADR-008](../adr/0008-client-control-plane.md)

> 本路线图继续坚持 v0.2 的 Agent Job Executor 本质，不再向 AI Execution OS 演变。长期差异化来自 **Agent-aware execution semantics**，而不是扩大成通用 AI 基础设施。

## 1. 长期目标

computecloud 只持续做深一个问题：

> **如何可靠、可恢复、可治理地把 Agent Job 分配到远程 Worker 执行，并理解 Agent Runtime、Tool、Credential、Workspace、Repository、Approval 和 Artifact provenance。**

核心模型：

~~~text
Job
  ↓
Stage
  ↓
Task
  ↓
Attempt
~~~

围绕该模型逐步增强：

~~~text
可靠性
  ↓
Runtime / Tool 生态
  ↓
Agent-aware Scheduling
  ↓
企业治理
  ↓
规模化与按需 HA
~~~

## 2. 产品调研后的演进原则

本轮产品调研进一步确认：computecloud 应建设“Agent Job Control Layer”，而不是继续向 Agent Harness、Sandbox Cloud、LLM Gateway 或通用 Workflow Engine 扩张。

### 2.1 必须自己做深的能力

这些能力构成 computecloud 的核心差异化，不能外包给通用基础设施：

- Job / Stage / Task / Attempt；
- Attempt generation / fencing；
- Retry Safety；
- Artifact provenance；
- Workspace lifecycle；
- Agent Runtime / Tool capability；
- Credential / account-aware scheduling；
- Workspace / repository affinity；
- Worker / queue / concurrency control；
- self-hosted / private worker governance。

### 2.2 优先通过 Adapter / Provider 接入的能力

以下能力优先集成，不默认自研完整平台：

~~~text
Agent Harness
  -> Codex / Claude / OpenHands / Managed Agent API / Custom Agent

Sandbox / Environment
  -> process / container / VM / Kubernetes / external sandbox provider

Tool
  -> Shell / Git / Browser / MCP / HTTP / Verifier

Storage
  -> Local / Shared File / Object

Durable Workflow
  -> 只借鉴可靠性语义，不复制通用 Workflow Engine
~~~

核心原则：

> **Build the Agent Job control plane; integrate the Agent, Sandbox and infrastructure ecosystems.**

### 2.3 Environment 成为 Runtime 之外的独立扩展点

行业产品显示，Prepared Environment / Sandbox 对远程 Agent Job 的启动速度、隔离和可重复性很重要。因此长期模型增加：

~~~text
Runtime
+
Tool
+
Environment
+
Workspace
~~~

其中 Environment 负责“任务在哪种隔离与依赖环境中运行”，Runtime 负责“哪个 Agent 执行任务”。

Environment 不升级为新的一级业务对象，而作为 Worker capability / provider。

### 2.4 兼容多种 Agent 接入形态

Runtime API 不只面向本地 CLI，还应覆盖：

~~~text
local CLI agent
remote CLI agent
API-backed agent
managed cloud agent
self-hosted agent server
custom enterprise agent
~~~

因此 Runtime API v2 必须避免把“本地子进程”写死在公共协议中。

### 2.5 Prepared Workspace 成为重要优化方向

参考远程 Coding Agent 产品，Workspace 长期不仅是临时目录，还需要支持：

- repository baseline；
- dependency/image/template；
- warm workspace；
- cached checkout；
- preinstalled tools；
- network / secret policy；
- reproducible environment fingerprint。

这属于 Agent Job 性能与可恢复性优化，不发展成通用开发环境产品。

## 3. 长期版本主线

~~~text
v0.2.x
真实环境做实
   ↓
v0.3.x
可靠性内核
   ↓
v0.4.x
Agent Runtime / Tool / Environment 生态
   ↓
v0.5.x
Agent-aware Scheduler
   ↓
v0.6.x
企业治理
   ↓
v0.7.x
规模化与韧性
   ↓
v1.0
稳定 Agent-aware Job Platform
~~~

| 版本 | 主题 | 核心交付 |
| --- | --- | --- |
| v0.2.x | Production Baseline | 真实 Codex/Claude/MCP、多机、故障、容量、部署 |
| v0.3.x | Reliability Kernel | Stage、Attempt fencing、Retry Safety、Artifact/Workspace lifecycle、长任务 |
| v0.4.x | Runtime / Tool / Environment Ecosystem | Runtime API v2、ToolCapability、EnvironmentProvider、Prepared Workspace、更多 Agent、Approval |
| v0.5.x | Agent-aware Scheduler | Capability、Credential、Environment readiness、Workspace/Repo affinity、Network、Fair Queue、Resource |
| v0.6.x | Enterprise Governance | Multi-tenant、RBAC、Quota、Secret、Policy、Audit、Private Worker / Trust Domain |
| v0.7.x | Scale & Resilience | Worker Group、GC、调度扩展、容量治理、按需 HA |
| v1.0 | Stable Platform | 稳定协议、SDK、兼容矩阵、SLO、运维体系 |

### 3.1 Control 客户端横向能力线

Control 是既有 Agent Job Executor 的产品表面，不是第二个调度器，也不建立独立事实源。Server 继续负责 Job、Stage、Task、Attempt、Worker、租约、Artifact 和审计；客户端仅持有可重建投影。保持单 Go Server + SQLite 的轻量部署，首版不引入独立 BFF、PostgreSQL、Redis 或消息队列。

| Control 里程碑 | 对齐主版本 | 核心交付 | 状态/门槛 |
| --- | --- | --- | --- |
| C0 Design Baseline | v0.3.2 | 调研、ADR-008、API/安全/UX 技术方案 | 当前完成 |
| C1 Observe PWA | v0.3.3+ | Job/Task/Attempt/Worker/Artifact 只读投影、稳定分页、SSE、attention | Stage、multi-Attempt、Retry Safety、Artifact Lifecycle 已完成；补查询与事件契约 |
| C2 Operate PWA | v0.4.x | 提交、取消、输入、审批、重试、Diff/测试审阅 | Runtime 原生输入/审批能力逐项验收；所有写操作幂等并带 generation fencing |
| C3 Mobile Beta | v0.5.x | Expo iOS/Android、QR 配对、Push、主机/运行时选择 | Agent-aware Scheduler 与设备身份可用 |
| C4 Governed Remote | v0.6.x | OIDC/RBAC、设备策略、审计、单写者 Lease、可选 E2EE Relay | 治理模型和威胁测试通过；Relay 不参与调度判断 |
| C5 Production | v0.7.x/v1.0 | 弱网、规模、兼容矩阵、应用商店/企业分发、SLO | Scale & Resilience 门槛完成 |

永久边界：手机不作为通用 Worker；离线客户端不排队取消、审批或重试等危险操作；Push 不携带提示词、代码或审批正文；公网 Relay 仅在直连/VPN 无法满足已验证需求时引入。

## 4. v0.2.x — Production Baseline

### 4.1 目标

不增加新的领域模型，先把 v0.2 已实现能力在真实环境做实。

### 4.2 必须完成

- 固定真实 Codex / Claude 版本；
- 真实 Codex exec；
- 真实 Claude print；
- 真实 Codex MCP；
- 至少两台独立 Worker；
- TLS / Worker identity；
- single；
- 跨 Worker Map/Reduce；
- Server / Worker SIGKILL；
- 网络中断；
- Artifact 哈希与下载；
- SQLite WAL / busy / backup / restore；
- 真实长任务；
- 24h+ 稳定运行；
- upgrade / rollback；
- fixture 结果与真实环境结果分开保存。

### 4.3 G1 处理原则

现有 Responses / SSE / compact 能力继续保留，但从现在开始只定义为：

> **Agent Runtime Support Adapter**

不继续扩展：

- 通用模型 Provider 管理；
- LLM Router；
- Model Gateway 产品能力；
- 推理服务调度。

### 4.4 基础 Trace

至少贯穿：

~~~text
request_id
job_id
task_id
attempt_id
worker_id
runtime
artifact_id
~~~

### 4.5 退出门槛

- 两台独立主机通过；
- 真实 Codex / Claude 通过；
- 真实 MCP 通过；
- 关键网络/进程故障有证据；
- 24h+ 稳定性通过；
- 容量基线形成；
- backup / restore / rollback 可复现。

## 5. v0.3.x — Reliability Kernel

v0.3 不以增加功能数量为目标，而以建立 Agent Job Executor 的正确性内核为目标。

### 5.1 v0.3.0 — Stage + Attempt Fencing

领域模型从当前：

~~~text
Job -> Task -> Attempt
~~~

演进为：

~~~text
Job -> Stage -> Task -> Attempt
~~~

Stage 只表达：

- fan-out；
- barrier；
- fan-in；
- reduce；
- verify；
- bounded stage dependency。

Map/Reduce 映射：

~~~text
Job
├── Map Stage
│   ├── Task A
│   ├── Task B
│   └── Task C
└── Reduce Stage
    └── Task D
~~~

同时实现：

- attempt generation；
- one active Attempt per Task；
- generation fencing；
- delayed heartbeat/event/artifact/complete rejection；
- completion CAS；
- old execution cleanup proof；
- cancel tombstone。

核心门槛：

> 网络抖动、Server 重启、Worker 重连都不能产生两个有效 Attempt。

### 5.2 v0.3.1 — Retry Safety

实现当前 R1，但严格受限：

- replay_safe；
- retryable error taxonomy；
- max_attempts；
- exponential backoff；
- retry budget；
- remaining deadline；
- attempt history；
- side-effect policy；
- late result rejection。

默认：

~~~text
Read-only / analysis        可重试
Explicitly idempotent       可配置重试
External mutation / publish 默认不自动重试
Execution state unknown     RECONCILING，不重试
~~~

### 5.3 v0.3.2 — Artifact Lifecycle

Artifact 进入可靠性内核，不等到 Runtime 扩展阶段。

最小字段：

~~~text
job_id
stage_id
task_id
attempt_id
generation
artifact_id
checksum
state
owner
~~~

v0.3.2 采用“状态 + 显式不可变引用”模型：

~~~text
STAGED
 ├──> ACCEPTED
 │      ├── task_result
 │      ├── reduce_input
 │      └── job_result
 │
 └──> ORPHANED
          ↓
      DELETING
          ↓
       DELETED
~~~

规则：

- 只有有效 current generation 的 Artifact 可以 ACCEPT；
- 只有 ACCEPTED Artifact 可以进入下一 Stage 或正式结果；
- Reference 只能指向 ACCEPTED，创建后不可变；
- Reduce input 与 Job result 都通过显式 reference 固化 provenance；
- old/retried generation Artifact 进入 ORPHANED，不进入正式结果；
- ORPHANED 只有在安全窗口到期且无 reference 时才可进入 GC；
- 文件删除使用可恢复的 DELETING -> DELETED tombstone；
- 用户 TTL、全历史 retention 与 tombstone 最终 GC 延后到规模化治理阶段。

### 5.4 v0.3.3 — Workspace Lifecycle

Workspace 同样进入可靠性内核：

- Attempt ownership；
- generation；
- repository baseline；
- writable state；
- cleanup state；
- retention；
- TTL；
- disk quota；
- optional checkpoint。

关键原则：

> 新旧 Attempt 不允许无约束共享同一个可写 Workspace。

### 5.5 v0.3.4 — Long-running Job

支持：

- progress heartbeat；
- liveness；
- no-output != hung；
- cancellation escalation；
- process tree cleanup；
- long lease renewal；
- explicit deadline extension policy；
- long-running event compaction / bounded retention。

### 5.6 v0.3.5 — Fair Scheduling

在正确性稳定之后再增加：

- priority；
- per-project concurrency；
- per-account concurrency；
- per-job concurrency；
- fair queue；
- aging；
- queue blocker reason；
- backpressure。

### 5.7 v0.3 退出门槛

- Stage schema 与 v0.2 Job 兼容策略明确；
- Retry 不制造双执行；
- old generation 永远不能覆盖新 generation；
- Artifact provenance 完整；
- Workspace cleanup / ownership 可验证；
- 24h+ Job 正确续租；
- cancel/retry/timeout race 有自动测试；
- fair queue 有 starvation 测试。

### 5.8 v0.3.1 Retry Safety — 已完成

在 v0.3.0 Stage / multi-Attempt / fencing 基础上，v0.3.1 引入保守自动 Retry：

- Execution 显式 `replay_safe`；
- `max_attempts_per_task=1..3`；
- cleanup-confirmed 才允许 Retry；
- Server 固定 retryable error allowlist；
- `retry_after` 持久 backoff；
- deadline 不重置；
- retry scheduled / exhausted events；
- 独立 GitHub Actions `retry-flow`。

执行状态不确定、非 replay-safe、验证失败、deadline/cancel 等场景均不自动 Retry。

### 5.9 v0.3.2 Artifact Lifecycle — 已完成

在 Retry Safety 之后补齐 Artifact provenance 与删除恢复：

- Server schema v6；
- ACCEPTED-only immutable references；
- `task_result / reduce_input / job_result`；
- failed/retried generation 自动 ORPHANED；
- ORPHANED safety window；
- `ORPHANED -> DELETING -> DELETED` 两阶段删除；
- restart 后恢复 DELETING；
- 独立 GitHub Actions `artifact-flow`；
- package gate 同时依赖 verify / task-flow / retry-flow / artifact-flow。

v0.3.2 只解决 Agent Job Artifact 正确性，不提前实现通用 Storage Provider、用户 TTL 或全历史 GC。

### 5.10 v0.3.3 Workspace Lifecycle — 已完成

在 Artifact provenance 之后补齐 Worker 本地可写状态的可靠性边界：

- Worker schema v3；
- Attempt/task/generation/repository baseline/path immutable ownership；
- `PREPARING -> READY -> IN_USE -> RETAINED -> DELETING -> DELETED`；
- cleanup unknown -> `QUARANTINED`；
- Runtime spawn 前持久 `IN_USE`；
- Retention GC 同时要求过期与 Server completion ack；
- restart 可恢复 DELETING；
- legacy Workspace 只按已知 local run 安全接管；
- `workspace_max_bytes` 运行期 quota；
- Attempt-scoped Reduce input 随 cleanup proof 清理；
- 独立 GitHub Actions `workspace-flow`。

v0.3.3 仍坚持每个 Attempt 独占 writable Workspace；Prepared/warm Workspace 和跨 Attempt 复用留给 v0.4 的受控优化，不允许破坏 generation isolation。

完成证据：PR #20 已合并为 `fa48c199829ad322a2976d0f6354c92368e9406a`；PR-head CI `36252887159` 与 main CI `36253102444` 均通过，main package Gate 通过。

## 6. v0.4.x — Agent Runtime、Tool 与 Environment 生态

这一阶段扩大“Agent Job 能做什么、能在哪里安全运行、能如何快速准备工作环境”，但不扩大产品领域。产品调研明确要求这一阶段采用 **Adapter / Provider-first** 策略：优先接入现有 Agent 和 Sandbox 生态，而不是自建完整 Harness 或 Sandbox Cloud。

### 6.1 Runtime API v2

Runtime 是 Agent 执行载体：

~~~text
Runtime
├── Codex
├── Claude
└── Custom Agent
~~~

统一契约：

~~~text
Name
Version
Prepare
Start
Inspect
Stop
RuntimeCapabilities
~~~

RuntimeCapability 示例：

~~~text
structured_output
stream_output
session_resume
interactive_input
approval
workspace_checkpoint
network_required
container_supported
~~~

### 6.2 ToolCapability

Tool 与 Runtime 分离：

~~~text
Tool
├── Shell
├── Git
├── Browser
├── MCP
├── HTTP/API
└── Verifier
~~~

ToolCapability 至少包含：

- permission；
- timeout；
- credential_ref；
- network policy；
- isolation requirement；
- audit identity。

### 6.3 Agent Runtime 扩展

保持 Codex / Claude，并逐步支持：

- 其他 CLI Agent；
- 企业自研 Agent；
- API-backed Agent；
- 本地 Agent Runtime。

每个 Runtime 必须通过统一 contract test。

### 6.4 SessionRef / Approval

Session 不升级为平台一级领域模型，只作为 Runtime capability：

- stable session_ref；
- worker affinity；
- native session mapping；
- explicit resume；
- approval request / response；
- interrupt；
- optional interactive input。

### 6.5 EnvironmentProvider / SandboxProvider

Environment 与 Runtime 分离：

~~~text
Runtime
  = 谁执行 Agent Job

Environment
  = Agent Job 在什么隔离、依赖和网络环境里运行
~~~

建议 Provider：

~~~text
local-process
container
vm
kubernetes
external-sandbox
~~~

外部 Sandbox 产品作为 Provider 接入，不进入 computecloud 核心领域模型。

EnvironmentCapability 至少包含：

- isolation level；
- image / template；
- CPU / Memory / Disk；
- network policy；
- filesystem mode；
- startup latency；
- checkpoint/snapshot capability；
- trusted / untrusted execution class。

### 6.6 Prepared Workspace / Workspace Template

在 v0.3 Workspace Lifecycle 基础上增加“可重复准备环境”：

- workspace_template_id；
- repository baseline；
- dependency/image fingerprint；
- preinstalled Runtime / Tool；
- warm workspace；
- cached checkout；
- environment fingerprint；
- startup / prepare latency。

目标是降低远程 Agent Job 冷启动和重复准备成本，而不是构建 IDE/Dev Environment 产品。

### 6.7 API-backed / Managed Agent Adapter

Runtime API v2 必须支持不仅是 Worker 本地进程，也包括：

- local CLI Agent；
- remote CLI Agent；
- self-hosted Agent Server；
- API-backed Agent；
- managed cloud Agent。

因此 Runtime contract 需要把：

~~~text
Job semantics
Attempt lifecycle
events
artifacts
cancel
capabilities
~~~

与具体进程模型解耦。

### 6.8 Trigger / Delivery Integration

借鉴 Coding Agent Cloud 的任务交付方式，增加轻量集成 Adapter：

- GitHub / GitLab issue or PR trigger；
- CI trigger；
- webhook trigger；
- branch / patch / report delivery；
- verifier result；
- PR/commit reference。

这些都是 Job ingress / result delivery adapter，不改变 Job 核心模型。

### 6.9 v0.4 退出门槛

- 3+ Runtime 共享同一 Runtime API；
- 至少覆盖 local CLI 与 API-backed 两种 Runtime 形态；
- 新 Runtime 不修改核心 Scheduler；
- RuntimeCapability / ToolCapability / EnvironmentCapability 分离；
- 至少 2 类 Environment Provider 通过统一 contract test；
- Prepared Workspace 能显著降低重复 Job prepare latency；
- Approval 可审计；
- Tool 越权 / 网络越权有负向测试。

## 7. v0.5.x — Agent-aware Scheduler

v0.5 不做通用资源调度器，而是回答：

> **这个 Agent Task 在哪个 Worker 上最适合、最安全、最可能成功？**

### 7.1 调度优先级

长期信号顺序：

~~~text
1 Runtime / Agent Capability
2 Security / Permission
3 Credential / Account Availability
4 Environment / Isolation Compatibility
5 Prepared Workspace / Repository Affinity
6 Network / Tool Reachability
7 Queue / Concurrency
8 CPU / Memory / Disk
9 Optional Accelerator
~~~

### 7.2 Worker Capability

Worker 报告：

~~~text
labels
os / arch
runtime + version
tool capability
environment capability
credential/account class
network zone
repository/workspace hints
prepared workspace/template hints
isolation / trust class
environment startup cost
cpu
memory
disk
optional accelerator
slots
~~~

不报告 Secret 原文。

### 7.3 Filter

硬约束：

- Runtime/version；
- Tool；
- project；
- credential/account；
- repository；
- network；
- isolation；
- Worker pool；
- minimum resources。

### 7.4 Queue / Fairness

- project fair share；
- account concurrency；
- priority；
- aging；
- queue limit；
- backpressure；
- blocker visibility。

### 7.5 Score

主要评分：

- Environment readiness / startup cost；
- Prepared Workspace affinity；
- Repository affinity；
- warm Runtime；
- Worker load；
- free slots；
- credential availability；
- historical latency；
- failure rate；
- resource headroom。

### 7.6 Bind 与解释性

保存至少：

~~~text
candidate workers
filter reasons
major score factors
selected worker
fallback reason
~~~

能够回答：

> 为什么 Task-123 被分配到 Worker-07？

### 7.7 资源边界

CPU / Memory / Disk / optional GPU 可以作为约束，但不建设：

- GPU topology scheduler；
- model placement；
- KV locality；
- model serving placement。

### 7.8 Artifact 跨节点

当 Server 本地文件不能满足需求时，引入通用 Provider：

~~~text
Local
Shared File
Object
~~~

仅解决 Workspace / Artifact 交接，不建设 Storage Fabric。

### 7.9 Private Worker / BYO Worker Pool

产品调研显示企业私有执行环境是 computecloud 的重要差异化，因此 v0.5 开始把 private worker 作为一等部署模式验证：

~~~text
Central Control Plane
        ↓
Outbound Worker Connection
        ↓
Enterprise Private Network
        ↓
Private Worker Pool
~~~

要求：

- Worker 主动出站连接；
- 不要求控制面直接入站访问企业 Worker；
- Worker identity / certificate；
- Worker pool / trust class；
- project -> worker pool policy；
- credential 不离开授权 trust domain；
- Artifact 可选择保留在私有域。

此能力不等于多集群控制面，而是 Agent Job Executor 的企业执行边界。

### 7.10 v0.5 退出门槛

- Capability 过滤正确；
- credential/account 不超并发；
- fair queue 不 starvation；
- affinity 有实际收益证据；
- 调度原因可解释；
- 16 / 32 / 64+ Worker 容量曲线可测；
- Artifact 跨节点可靠；
- Private Worker Pool 能通过 outbound-only 模式运行；
- Environment readiness / startup cost 能被调度观测和解释。

## 8. v0.6.x — Enterprise Governance

### 8.1 Tenant / Project

稳定对象：

~~~text
Tenant
Project
User / Service Account
Token
Role
Policy
Quota
CredentialRef
~~~

### 8.2 RBAC

至少：

- submit；
- read；
- cancel；
- artifact download；
- runtime use；
- tool use；
- approval；
- audit read；
- admin。

### 8.3 Secret / Credential

JobSpec 不保存 Secret 原文。

统一使用：

~~~text
credential_ref
secret_ref
runtime_identity
~~~

要求：

- scoped；
- revocable；
- rotatable；
- auditable；
- Worker 只获得最小必要凭据。

### 8.4 Policy

控制：

- repository；
- runtime；
- tool；
- network；
- isolation；
- timeout；
- artifact；
- verifier；
- retry class。

### 8.5 Audit

记录：

- submit；
- cancel；
- retry；
- approval；
- secret use；
- tool invocation；
- artifact download；
- policy change。

### 8.6 Metering

围绕 Agent Job：

- Job / Stage / Task / Attempt duration；
- Runtime duration；
- CPU time；
- optional accelerator time；
- Artifact bytes；
- Tool calls；
- model tokens（Runtime 能提供时）。

### 8.7 Worker Trust Domain

企业治理需要把 Worker 视为安全边界，而不仅是资源节点。

至少定义：

- worker_pool；
- trust_domain；
- isolation_class；
- allowed_projects；
- allowed_credentials；
- allowed_repositories；
- allowed_network_zones；
- artifact residency policy。

控制面只下发 Job 所需的最小 credential reference 和 policy，不让 Worker 获得跨项目 Secret。

### 8.8 v0.6 退出门槛

- Tenant 数据隔离；
- Secret 不泄漏到普通 event/artifact；
- RBAC 完整自动测试；
- Quota 并发竞态不超限；
- Audit 能追到 actor -> Job -> Stage -> Task -> Attempt。

## 9. v0.7.x — Scale & Resilience

### 9.1 Lifecycle / GC

建立：

- event retention；
- log retention；
- artifact TTL；
- workspace GC；
- orphaned artifact GC；
- attempt history；
- tombstone GC；
- DB vacuum / maintenance。

原则：

> 仍被 Job / Stage / Attempt 引用的数据不得删除。

### 9.2 Scheduler Scaling

优化：

- Worker heartbeat batching；
- capability index；
- credential/account availability index；
- queue index；
- scheduler scan；
- event batching；
- command batching。

### 9.3 Worker Group

支持：

~~~text
default
trusted
sandbox
browser
high-memory
dedicated-team
special-tool
restricted-network
~~~

### 9.4 HA 仅按真实瓶颈触发

继续保留单 Server + SQLite，直到出现：

- RTO 不满足 SLO；
- SQLite write contention 成为真实瓶颈；
- maintenance / backup 窗口不可接受；
- 必须 active controller failover。

届时另立 ADR 评估：

- External SQL；
- leader election；
- active/passive；
- transactional outbox。

版本号到 v0.7 本身不是引入 HA 的理由。

### 9.5 Ecosystem Compatibility Matrix

规模化阶段不仅验证 Worker 数量，也要维护生态兼容矩阵：

~~~text
Runtime
Environment Provider
Tool
OS / Arch
Worker version
Protocol version
~~~

对外明确 certified / experimental / unsupported 状态。

避免 Runtime、Sandbox 和 Worker 生态扩展后形成不可维护的隐式组合。

### 9.6 v0.7 退出门槛

- 历史数据可治理；
- 大规模 heartbeat 不拖垮 Server；
- 调度延迟增长可控；
- GC 可恢复；
- RTO 有实测；
- 是否需要 HA 有数据结论。

## 10. v1.0 — Stable Agent-aware Job Platform

v1.0 不以功能多为标准，而以契约成熟和生产边界清晰为标准。

### 10.1 稳定协议

稳定：

- Job / Stage / Task / Attempt schema；
- Runtime API；
- RuntimeCapability；
- ToolCapability；
- EnvironmentCapability / EnvironmentProvider contract；
- Worker protocol；
- Event schema；
- Artifact lifecycle；
- Workspace lifecycle；
- auth / quota / audit semantics；
- deprecation policy。

### 10.2 正式支持场景

至少：

~~~text
Single Agent Job
Map/Reduce
Fan-out / Barrier / Fan-in
Code Modify + Verify
Long-running Agent Job
CI / Automation Job
Tool-enabled Agent Job
Session-resume Agent Job
Sandboxed Agent Job
~~~

### 10.3 SDK / Integration

稳定：

- HTTP API；
- MCP；
- CLI；
- Go client；
- 根据需求提供 Python SDK。

### 10.4 运维

完整：

- install；
- upgrade；
- rollback；
- backup / restore；
- capacity planning；
- metrics / alert；
- incident runbook；
- compatibility matrix；
- supported scale。

### 10.5 v1.0 必须回答

- 最大已验证 Worker 数；
- 并发 Job / Task / Attempt 边界；
- SQLite 单 Server 的实测边界；
- Runtime compatibility matrix；
- Tool capability matrix；
- 哪些故障自动恢复；
- 哪些 Job 允许 retry；
- Artifact / Workspace retention；
- 支持的 isolation；
- 升级 / 回退流程。

## 11. 永久边界

以下方向不作为本产品线性演进：

~~~text
AI Execution OS
Model Serving Platform
Model Deployment
KV Cache Fabric
Model Locality Scheduler
Training Scheduler
General GPU Cloud
General Workflow Engine
General LLM Gateway
~~~

如果未来出现需求：

~~~text
External AI Platform
        ↓
Agent Job API
        ↓
computecloud
~~~

通过 API 复用 Agent Job Executor，而不是把 computecloud 改造成另一套平台。

## 12. 单二进制 / SQLite 原则

继续坚持：

> **先模块化，再服务化。**

只要单 Server + SQLite 满足真实 SLO，就保留。

不因为假设性未来规模提前引入：

- PostgreSQL；
- Redis；
- Kafka；
- etcd；
- 微服务拆分。

## 13. 代码模块演进

建议逐步整理：

~~~text
internal/
├── api/
├── job/
│   ├── controller/
│   ├── stage/
│   ├── scheduler/
│   ├── retry/
│   └── verifier/
├── runtime/
│   ├── codex/
│   ├── claude/
│   └── custom/
├── environment/
│   ├── local/
│   ├── container/
│   ├── vm/
│   └── provider/
├── tool/
│   ├── shell/
│   ├── git/
│   ├── browser/
│   └── mcp/
├── worker/
├── workspace/
├── artifact/
├── auth/
├── quota/
├── audit/
└── store/
~~~

模块化不等于服务化。

## 14. 测试策略

长期保持四层：

~~~text
L1 Unit
L2 Fixture Integration
L3 Multi-process Fault Injection
L4 Real Runtime / Multi-host
~~~

重点新增：

- generation fencing；
- Artifact old-generation rejection；
- Workspace ownership；
- Stage barrier recovery；
- retry safety；
- credential/account race；
- Tool permission；
- Runtime contract；
- scheduler starvation；
- GC recovery。

## 15. ADR 演进序列

已完成：

1. ADR-003：Agent Job Executor 产品边界；
2. ADR-004：Agent-aware 执行语义与长期边界；
3. ADR-005：Stage / Multi-Attempt / Fencing；
4. ADR-006：Retry Safety；
5. ADR-007：Artifact Lifecycle；
6. ADR-008：客户端控制面采用薄客户端与服务端事实源；
7. ADR-009：Workspace Lifecycle。

后续建议：

8. Runtime API v2；
9. RuntimeCapability / ToolCapability；
10. EnvironmentProvider / Prepared Workspace；
11. Agent-aware Scheduling；
12. Private Worker / Trust Domain；
13. Multi-tenant / RBAC；
14. History / GC；
15. HA Trigger / State Backend（仅需要时）。

## 16. 方向判断规则

任何新能力进入路线图前回答：

1. 它是否直接提升 Agent Job 的可靠性、执行能力、调度效率、安全性或可管理性？
2. 它是否能自然落入 Job -> Stage -> Task -> Attempt 模型？
3. 它是否增强 Agent-aware 语义？
4. 如果去掉“Agent Job”前提，它是否其实属于另一个平台？

若第 4 条成立，应优先建设独立系统。

## 17. 市场触发的路线重审条件

产品调研不是一次性结论。出现以下情况时，应重新进行架构 Review，而不是自动跟随行业加功能：

- Agent Runtime 出现事实标准协议；
- MCP / ACP 或类似协议形成稳定跨 Agent runtime 标准；
- Agent Sandbox 形成广泛通用基础设施标准；
- Durable Agent Job 出现事实上的公共执行模型；
- 主流 Coding Agent 开放企业私有 Worker / BYO execution；
- Managed Agent API 普遍支持外部 durable job controller；
- Agent-aware Scheduling 被上游 Runtime 原生覆盖；
- private worker / credential / workspace 需求发生明显变化。

Review 的判断问题是：

> 该行业变化应该成为 computecloud 的核心能力、Adapter/Provider，还是应该完全交给外部系统？

默认优先顺序：

~~~text
reuse protocol
  ↓
build adapter
  ↓
build provider
  ↓
only then consider core model change
~~~

## 18. 最终路线一句话

~~~text
v0.2  能稳定运行真实 Agent Job
  ↓
v0.3  不重复、不丢结果、可安全重试
  ↓
v0.4  接入更多 Agent / Tool / Environment，并加速 Workspace 准备
  ↓
v0.5  根据 Agent、Credential、Environment、Workspace 语义智能选择 Worker
  ↓
v0.6  多团队安全治理
  ↓
v0.7  大规模长期运行
  ↓
v1.0  稳定 Agent-aware Distributed Job Execution Platform
~~~
