# computecloud Agent-aware Distributed Job Execution Platform 长期路线图

- 项目：computecloud
- 日期：2026-09-24
- 当前稳定基线：v0.2.0
- 产品类别：**Agent Job Executor**
- 长期定位：**Agent-aware Distributed Job Execution Platform**
- 总体架构：[Agent-aware 总体架构](../design/agent-job-executor-architecture.md)
- 产品边界：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)
- 执行语义：[ADR-004](../adr/0004-agent-aware-execution-semantics.md)
- 当前实现依据：[v0.2 实施计划](v0.2-plan.md)、[v0.2 验证记录](../validation/v0.2-results.md)

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

## 2. 长期版本主线

~~~text
v0.2.x
真实环境做实
   ↓
v0.3.x
可靠性内核
   ↓
v0.4.x
Agent Runtime / Tool 生态
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
| v0.4.x | Runtime & Tool Ecosystem | Runtime API v2、ToolCapability、更多 Agent、Approval、Sandbox |
| v0.5.x | Agent-aware Scheduler | Capability、Credential、Workspace/Repo affinity、Network、Fair Queue、Resource |
| v0.6.x | Enterprise Governance | Multi-tenant、RBAC、Quota、Secret、Policy、Audit、Isolation |
| v0.7.x | Scale & Resilience | Worker Group、GC、调度扩展、容量治理、按需 HA |
| v1.0 | Stable Platform | 稳定协议、SDK、兼容矩阵、SLO、运维体系 |

## 3. v0.2.x — Production Baseline

### 3.1 目标

不增加新的领域模型，先把 v0.2 已实现能力在真实环境做实。

### 3.2 必须完成

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

### 3.3 G1 处理原则

现有 Responses / SSE / compact 能力继续保留，但从现在开始只定义为：

> **Agent Runtime Support Adapter**

不继续扩展：

- 通用模型 Provider 管理；
- LLM Router；
- Model Gateway 产品能力；
- 推理服务调度。

### 3.4 基础 Trace

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

### 3.5 退出门槛

- 两台独立主机通过；
- 真实 Codex / Claude 通过；
- 真实 MCP 通过；
- 关键网络/进程故障有证据；
- 24h+ 稳定性通过；
- 容量基线形成；
- backup / restore / rollback 可复现。

## 4. v0.3.x — Reliability Kernel

v0.3 不以增加功能数量为目标，而以建立 Agent Job Executor 的正确性内核为目标。

### 4.1 v0.3.0 — Stage + Attempt Fencing

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

### 4.2 v0.3.1 — Retry Safety

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

### 4.3 v0.3.2 — Artifact Lifecycle

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

生命周期：

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

规则：

- 只有有效 generation 的 Artifact 可以 ACCEPT；
- 只有 ACCEPTED Artifact 可以进入下一 Stage；
- 旧 Attempt Artifact 只能诊断，不进入正式结果；
- Artifact upload 与 completion 必须有稳定幂等语义。

### 4.4 v0.3.3 — Workspace Lifecycle

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

### 4.5 v0.3.4 — Long-running Job

支持：

- progress heartbeat；
- liveness；
- no-output != hung；
- cancellation escalation；
- process tree cleanup；
- long lease renewal；
- explicit deadline extension policy；
- long-running event compaction / bounded retention。

### 4.6 v0.3.5 — Fair Scheduling

在正确性稳定之后再增加：

- priority；
- per-project concurrency；
- per-account concurrency；
- per-job concurrency；
- fair queue；
- aging；
- queue blocker reason；
- backpressure。

### 4.7 v0.3 退出门槛

- Stage schema 与 v0.2 Job 兼容策略明确；
- Retry 不制造双执行；
- old generation 永远不能覆盖新 generation；
- Artifact provenance 完整；
- Workspace cleanup / ownership 可验证；
- 24h+ Job 正确续租；
- cancel/retry/timeout race 有自动测试；
- fair queue 有 starvation 测试。

## 5. v0.4.x — Agent Runtime 与 Tool 生态

这一阶段扩大“Agent Job 能做什么”，但不扩大产品领域。

### 5.1 Runtime API v2

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

### 5.2 ToolCapability

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

### 5.3 Agent Runtime 扩展

保持 Codex / Claude，并逐步支持：

- 其他 CLI Agent；
- 企业自研 Agent；
- API-backed Agent；
- 本地 Agent Runtime。

每个 Runtime 必须通过统一 contract test。

### 5.4 SessionRef / Approval

Session 不升级为平台一级领域模型，只作为 Runtime capability：

- stable session_ref；
- worker affinity；
- native session mapping；
- explicit resume；
- approval request / response；
- interrupt；
- optional interactive input。

### 5.5 Sandbox

按风险逐级：

~~~text
process
  ↓
container
  ↓
VM / sandbox（按业务需要）
~~~

隔离级别进入 Worker / Tool capability。

### 5.6 v0.4 退出门槛

- 3+ Runtime 共享同一 Runtime API；
- 新 Runtime 不修改核心 Scheduler；
- RuntimeCapability / ToolCapability 分离；
- Approval 可审计；
- Container 可选运行；
- Tool 越权 / 网络越权有负向测试。

## 6. v0.5.x — Agent-aware Scheduler

v0.5 不做通用资源调度器，而是回答：

> **这个 Agent Task 在哪个 Worker 上最适合、最安全、最可能成功？**

### 6.1 调度优先级

长期信号顺序：

~~~text
1 Runtime / Agent Capability
2 Security / Permission
3 Credential / Account Availability
4 Workspace / Repository Affinity
5 Network / Tool Reachability
6 Queue / Concurrency
7 CPU / Memory / Disk
8 Optional Accelerator
~~~

### 6.2 Worker Capability

Worker 报告：

~~~text
labels
os / arch
runtime + version
tool capability
credential/account class
network zone
repository/workspace hints
isolation
cpu
memory
disk
optional accelerator
slots
~~~

不报告 Secret 原文。

### 6.3 Filter

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

### 6.4 Queue / Fairness

- project fair share；
- account concurrency；
- priority；
- aging；
- queue limit；
- backpressure；
- blocker visibility。

### 6.5 Score

主要评分：

- Workspace affinity；
- Repository affinity；
- warm Runtime；
- Worker load；
- free slots；
- credential availability；
- historical latency；
- failure rate；
- resource headroom。

### 6.6 Bind 与解释性

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

### 6.7 资源边界

CPU / Memory / Disk / optional GPU 可以作为约束，但不建设：

- GPU topology scheduler；
- model placement；
- KV locality；
- model serving placement。

### 6.8 Artifact 跨节点

当 Server 本地文件不能满足需求时，引入通用 Provider：

~~~text
Local
Shared File
Object
~~~

仅解决 Workspace / Artifact 交接，不建设 Storage Fabric。

### 6.9 v0.5 退出门槛

- Capability 过滤正确；
- credential/account 不超并发；
- fair queue 不 starvation；
- affinity 有实际收益证据；
- 调度原因可解释；
- 16 / 32 / 64+ Worker 容量曲线可测；
- Artifact 跨节点可靠。

## 7. v0.6.x — Enterprise Governance

### 7.1 Tenant / Project

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

### 7.2 RBAC

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

### 7.3 Secret / Credential

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

### 7.4 Policy

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

### 7.5 Audit

记录：

- submit；
- cancel；
- retry；
- approval；
- secret use；
- tool invocation；
- artifact download；
- policy change。

### 7.6 Metering

围绕 Agent Job：

- Job / Stage / Task / Attempt duration；
- Runtime duration；
- CPU time；
- optional accelerator time；
- Artifact bytes；
- Tool calls；
- model tokens（Runtime 能提供时）。

### 7.7 v0.6 退出门槛

- Tenant 数据隔离；
- Secret 不泄漏到普通 event/artifact；
- RBAC 完整自动测试；
- Quota 并发竞态不超限；
- Audit 能追到 actor -> Job -> Stage -> Task -> Attempt。

## 8. v0.7.x — Scale & Resilience

### 8.1 Lifecycle / GC

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

### 8.2 Scheduler Scaling

优化：

- Worker heartbeat batching；
- capability index；
- credential/account availability index；
- queue index；
- scheduler scan；
- event batching；
- command batching。

### 8.3 Worker Group

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

### 8.4 HA 仅按真实瓶颈触发

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

### 8.5 v0.7 退出门槛

- 历史数据可治理；
- 大规模 heartbeat 不拖垮 Server；
- 调度延迟增长可控；
- GC 可恢复；
- RTO 有实测；
- 是否需要 HA 有数据结论。

## 9. v1.0 — Stable Agent-aware Job Platform

v1.0 不以功能多为标准，而以契约成熟和生产边界清晰为标准。

### 9.1 稳定协议

稳定：

- Job / Stage / Task / Attempt schema；
- Runtime API；
- RuntimeCapability；
- ToolCapability；
- Worker protocol；
- Event schema；
- Artifact lifecycle；
- Workspace lifecycle；
- auth / quota / audit semantics；
- deprecation policy。

### 9.2 正式支持场景

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

### 9.3 SDK / Integration

稳定：

- HTTP API；
- MCP；
- CLI；
- Go client；
- 根据需求提供 Python SDK。

### 9.4 运维

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

### 9.5 v1.0 必须回答

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

## 10. 永久边界

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

## 11. 单二进制 / SQLite 原则

继续坚持：

> **先模块化，再服务化。**

只要单 Server + SQLite 满足真实 SLO，就保留。

不因为假设性未来规模提前引入：

- PostgreSQL；
- Redis；
- Kafka；
- etcd；
- 微服务拆分。

## 12. 代码模块演进

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

## 13. 测试策略

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

## 14. ADR 演进序列

已完成：

1. ADR-003：Agent Job Executor 产品边界；
2. ADR-004：Agent-aware 执行语义与长期边界。

后续建议：

3. Stage Schema / Compatibility；
4. Attempt Generation / Fencing；
5. Artifact / Workspace Lifecycle；
6. Retry Safety；
7. Runtime API v2；
8. RuntimeCapability / ToolCapability；
9. Agent-aware Scheduling；
10. Multi-tenant / RBAC；
11. History / GC；
12. HA Trigger / State Backend（仅需要时）。

## 15. 方向判断规则

任何新能力进入路线图前回答：

1. 它是否直接提升 Agent Job 的可靠性、执行能力、调度效率、安全性或可管理性？
2. 它是否能自然落入 Job -> Stage -> Task -> Attempt 模型？
3. 它是否增强 Agent-aware 语义？
4. 如果去掉“Agent Job”前提，它是否其实属于另一个平台？

若第 4 条成立，应优先建设独立系统。

## 16. 最终路线一句话

~~~text
v0.2  能稳定运行真实 Agent Job
  ↓
v0.3  不重复、不丢结果、可安全重试
  ↓
v0.4  理解更多 Agent 与 Tool
  ↓
v0.5  根据 Agent 语义智能选择 Worker
  ↓
v0.6  多团队安全治理
  ↓
v0.7  大规模长期运行
  ↓
v1.0  稳定 Agent-aware Distributed Job Execution Platform
~~~
