# computecloud Agent Job Executor 长期演进路线图

- 项目：computecloud
- 日期：2026-09-24
- 当前稳定基线：v0.2.0
- 产品定位：**Agent Job Executor**
- 架构依据：[Agent Job Executor 总体架构](../design/agent-job-executor-architecture.md)
- 范围决策：[ADR-003](../adr/0003-agent-job-executor-product-scope.md)
- 当前实现依据：[v0.2 实施计划](v0.2-plan.md)、[v0.2 验证记录](../validation/v0.2-results.md)、[v0.2 运行指南](v0.2-runbook.md)

> 本路线图推翻此前“v0.2 线性演变为 AI Execution OS”的结论。v0.2 的本质是 Agent Job Executor，后续版本继续沿这条架构自然演进。

## 1. 长期目标

computecloud 不追求覆盖所有 AI 基础设施，而是把一个问题持续做深：

> **可靠地把 Agent / Tool Job 分配到远程 Worker 执行，并保证任务状态、重试、取消、恢复、产物、权限和多节点调度可控。**

最终产品形态：

~~~text
                       Agent Job Executor
                              │
             ┌────────────────┼────────────────┐
             │                │                │
         Job Gateway     Job Controller     Governance
             │                │
        HTTP / MCP       Task / Attempt
                              │
                          Scheduler
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
       Worker A            Worker B            Worker N
          │                   │                   │
     Runtime/Tool        Runtime/Tool        Runtime/Tool
          │                   │                   │
     Workspace           Workspace           Workspace
          └───────────────────┼───────────────────┘
                              │
                     Artifact / Result
~~~

## 2. 版本演进总览

~~~text
v0.2.x
生产做实 / 真实环境
   ↓
v0.3.x
可靠性内核 + 任务模型增强
   ↓
v0.4.x
Runtime / Tool 生态
   ↓
v0.5.x
资源感知 + 分布式调度
   ↓
v0.6.x
多租户 + 企业治理
   ↓
v0.7.x
规模化 + 高可用
   ↓
v1.0
稳定 Agent Job Executor
~~~

| 阶段 | 核心主题 | 主要目标 |
| --- | --- | --- |
| v0.2.x | 生产做实 | 真实 CLI、真实 MCP、双机、故障、容量、部署 |
| v0.3.x | 可靠性内核 | 自动重试、Attempt fencing、长任务、公平调度、优先级 |
| v0.4.x | Runtime / Tool 生态 | Runtime API、更多 Agent、Shell/Browser/MCP、容器执行 |
| v0.5.x | 资源感知调度 | Worker capability、CPU/Mem/GPU 可选资源、亲和性、队列优化 |
| v0.6.x | 企业能力 | Multi-tenant、RBAC、Quota、Secret、Audit、Isolation |
| v0.7.x | 规模与韧性 | 大 Worker 集群、GC、容量治理、HA 仅在真实触发时加入 |
| v1.0 | 稳定版本 | API 稳定、兼容策略、运维体系、插件边界、生产 SLA |

## 3. v0.2.x — 当前能力做实

### 3.1 目标

不扩产品边界，先把已有能力在真实环境验证完整。

### 3.2 重点

- 固定 Codex / Claude 版本；
- 真实 Codex exec；
- 真实 Claude print；
- 真实 Codex MCP；
- 至少两台独立 Worker；
- TLS 与 Worker 身份；
- single Job；
- 跨 Worker Map/Reduce；
- Worker SIGKILL；
- Server SIGKILL；
- 网络中断；
- Artifact 校验；
- SQLite busy / WAL / backup / restore；
- 真实长任务；
- 长时间运行；
- 发布包升级 / 回退；
- G1 模型网关保持可选，不升级为产品中心能力。

### 3.3 基础指标

必须建立：

~~~text
request_id
job_id
task_id
attempt_id
worker_id
runtime
artifact_id
~~~

以及：

- submit latency；
- queue latency；
- schedule latency；
- start latency；
- job duration；
- Worker RSS / CPU；
- Server RSS / CPU；
- SQLite write / busy；
- Artifact bytes；
- failure / cancel / reconcile rate。

### 3.4 退出门槛

- 两台独立主机跑通；
- 真实 Codex / Claude 跑通；
- MCP 客户端跑通；
- 关键故障矩阵有证据；
- 24h+ 稳定性测试；
- 容量基线；
- backup / restore / rollback 通过。

## 4. v0.3.x — 可靠性内核与任务模型增强

这一阶段优先解决“Job Executor 最难的问题”：重复执行、失败恢复和长任务。

### 4.1 Attempt Generation / Fencing

实现：

- attempt generation；
- 单 Task 一个 active Attempt；
- generation fencing；
- 迟到 heartbeat / event / artifact / complete 拒绝；
- completion CAS；
- old execution cleanup proof。

核心原则：

> 不能因为网络抖动或 Server 重启制造两个真实执行。

### 4.2 受限自动重试

实现当前 R1：

- replay_safe；
- max_attempts；
- retryable error classification；
- exponential backoff；
- retry budget；
- remaining deadline；
- attempt history；
- late result rejection。

默认规则：

~~~text
纯分析 / 只读任务       可重试
有明确幂等边界任务       可配置
修改外部系统 / 发布任务   默认不自动重试
执行状态不明             不重试，进入 RECONCILING
~~~

### 4.3 长任务

增加：

- long-running heartbeat；
- progress；
- liveness；
- no-output != hung；
- deadline extension policy（若允许必须显式）；
- cancellation escalation；
- process tree cleanup。

### 4.4 调度增强

保持轻量：

- priority；
- per-project concurrency；
- per-account concurrency；
- per-job concurrency；
- fair queue；
- aging；
- worker load；
- runtime capability。

### 4.5 有限任务依赖

不做通用 DAG，只增加 Agent Job 真正需要的模式：

~~~text
single
map_reduce
fan_out
barrier
fan_in
bounded stages
~~~

### 4.6 退出门槛

- 自动重试不会造成双执行；
- cancel / retry / timeout 竞态确定；
- 旧 Attempt 结果不会覆盖新 Attempt；
- 长任务 24h 仍能正确续租；
- Server / Worker 重启后恢复正确；
- 调度公平性有自动测试。

## 5. v0.4.x — Runtime 与 Tool 生态

这一阶段扩大的是“能执行什么 Agent Job”，不是产品领域。

### 5.1 Runtime API v2

统一：

~~~text
Prepare
Start
Inspect
Stop
Capabilities
Version
~~~

Capability 示例：

~~~text
structured_output
stream_output
session_resume
interactive_input
approval
workspace_checkpoint
container
browser
network
~~~

Scheduler 按 capability 匹配。

### 5.2 Agent Runtime

保持：

- Codex；
- Claude；

可逐步增加：

- 其他 CLI Agent；
- 企业自研 Agent；
- API-backed Agent；
- 本地 Agent Runtime。

每个 Runtime 必须通过统一 contract test。

### 5.3 Tool Runtime

逐步支持：

- Shell；
- Git；
- Browser；
- MCP；
- HTTP/API；
- verifier；
- test runner。

Tool 必须带：

- permission；
- timeout；
- environment；
- credential reference；
- network policy；
- audit identity。

### 5.4 Container / Sandbox

对于不可信代码或更强隔离：

~~~text
process
   ↓
container
   ↓
VM / sandbox（按需求）
~~~

隔离级别成为 Worker capability。

### 5.5 Workspace

增强：

- repo checkout；
- worktree；
- input package；
- checkpoint；
- cleanup；
- disk quota；
- workspace TTL；
- result manifest。

### 5.6 Artifact

扩展统一 Artifact：

- patch；
- report；
- source evidence；
- logs；
- test result；
- archive；
- checkpoint。

仍以 ID、checksum、owner 为核心，不暴露具体存储实现。

### 5.7 退出门槛

- 至少 3 类 Runtime 使用同一执行契约；
- Runtime 不需要修改核心 Scheduler；
- Tool 权限可审计；
- Container 执行可选开启；
- Workspace 泄漏 / 越权有测试；
- Artifact contract 稳定。

## 6. v0.5.x — 资源感知与分布式调度

这一阶段提升“放到哪个 Worker 更合适”。

### 6.1 Worker Capability

Worker 注册：

~~~text
labels
os / arch
cpu
memory
disk
optional accelerator
runtime
tool
isolation
repository/cache hints
~~~

GPU 可以作为某些 Agent Tool 的资源约束，但不扩展为模型 Serving 调度。

### 6.2 调度四阶段

~~~text
Filter
   ↓
Queue / Fairness
   ↓
Score
   ↓
Bind
~~~

Filter：

- runtime；
- tool；
- labels；
- project；
- resource；
- isolation；
- credential constraints。

Score：

- worker load；
- free slots；
- workspace affinity；
- repository affinity；
- warm runtime；
- historical latency；
- failure rate。

### 6.3 Affinity

支持：

- preferred worker；
- session / workspace affinity；
- repository affinity；
- anti-affinity；
- worker pool；
- dedicated pool。

### 6.4 Queue

增加：

- project fair share；
- priority；
- aging；
- queue limits；
- backpressure；
- admission reason；
- blocker visibility。

### 6.5 资源配额

面向 Agent Job：

- concurrent jobs；
- concurrent tasks；
- CPU；
- memory；
- disk；
- optional accelerator；
- Artifact bytes。

### 6.6 多节点 Artifact

当本地文件不能满足多节点时，引入通用 Provider：

~~~text
Local
Shared File
Object
~~~

这里只解决 Workspace / Artifact 交接，不建设通用 Storage Fabric。

### 6.7 退出门槛

- 资源不会超分配；
- Worker capability 过滤正确；
- fair queue 不饿死低优先级 Job；
- affinity 有性能或稳定性收益证据；
- 16/32/64+ Worker 容量曲线可测；
- Artifact 跨节点交付可靠。

## 7. v0.6.x — 多租户与企业治理

### 7.1 Tenant / Project

对象：

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
- admin；
- audit read。

### 7.3 Credential

禁止把真实 Secret 写进 JobSpec。

统一：

~~~text
credential_ref
secret_ref
runtime_identity
~~~

并提供 scope、rotation、revocation、audit。

### 7.4 Policy

针对：

- repository；
- runtime；
- tool；
- network；
- container；
- timeout；
- artifact；
- verifier。

### 7.5 Audit

关键操作：

- submit；
- cancel；
- approval；
- secret use；
- artifact download；
- runtime/tool invocation；
- policy change。

### 7.6 Metering

面向 Agent Job：

- Job duration；
- Task duration；
- Runtime duration；
- CPU time；
- optional GPU time；
- Artifact bytes；
- tool calls；
- model token（若 Runtime 可提供）。

计量先服务 quota / chargeback，不要求发展成复杂 Billing 平台。

### 7.7 退出门槛

- Tenant 间不能读取彼此 Job / Artifact；
- Secret 不进入普通事件和 Artifact；
- RBAC 自动测试完整；
- Quota 在并发竞态下不超限；
- Audit 可追溯到 actor / Job / Attempt。

## 8. v0.7.x — 规模化、生命周期与韧性

### 8.1 History / GC

增加：

- event retention；
- log retention；
- artifact TTL；
- workspace GC；
- attempt history；
- tombstone GC；
- database vacuum / maintenance。

原则：

> 不删除仍被 Job / Artifact 引用的数据。

### 8.2 大集群调度

优化：

- Worker heartbeat 批处理；
- capability index；
- queue index；
- scheduler scan；
- command batching；
- event batching；
- Artifact metadata path。

### 8.3 Worker Group

支持：

~~~text
default
trusted
sandbox
high-memory
browser
dedicated-team
special-tool
~~~

便于规模管理，不引入复杂资源云模型。

### 8.4 控制面 HA：只在触发时做

当前不要默认替换单 Server + SQLite。

触发条件：

- 单 Server 恢复时间无法满足 SLO；
- SQLite write contention 已经成为瓶颈；
- backup / maintenance 窗口不可接受；
- 必须支持 active controller failover。

届时再通过 ADR 评估：

- External SQL；
- leader election；
- active/passive controller；
- transactional outbox。

不是因为版本到了 v0.7 就必须引入。

### 8.5 退出门槛

- 长期历史数据不会无限增长；
- 大规模 Worker heartbeat 不拖垮 Server；
- 调度时间随 Worker 数增长仍可控；
- 故障恢复 RTO 有明确指标；
- 是否需要 HA 有真实数据结论。

## 9. v1.0 — 稳定 Agent Job Executor

v1.0 的目标不是“功能最多”，而是核心契约成熟。

### 9.1 稳定协议

稳定：

- Job API；
- Task / Attempt 模型；
- Runtime API；
- Worker protocol；
- Event schema；
- Artifact schema；
- Capability schema；
- auth / quota 语义；
- deprecation policy。

### 9.2 稳定工作负载

至少正式支持：

~~~text
Single Agent Job
Multi-Agent Fan-out/Fan-in
Map/Reduce
Code Modify + Verify
Long-running Agent Job
CI / Automation Job
Tool-enabled Agent Job
~~~

### 9.3 运维成熟

- install；
- upgrade；
- rollback；
- backup；
- restore；
- capacity planning；
- metrics；
- alerting；
- troubleshooting；
- incident runbook；
- compatibility matrix。

### 9.4 SDK / Integration

提供稳定：

- HTTP API；
- MCP；
- CLI；
- Go client；
- 必要时 Python SDK。

### 9.5 成功标准

v1.0 应能明确回答：

- 支持多少 Worker；
- 支持多少并发 Job / Task；
- SQLite 单 Server 的支持边界；
- 哪些 Runtime 已认证；
- 哪些故障可以自动恢复；
- 哪些任务允许自动重试；
- Artifact 保存多久；
- 如何升级和回退；
- 哪些能力明确不支持。

## 10. 明确不进入路线图的方向

以下内容不再作为 computecloud Agent Job Executor 的线性演进目标：

~~~text
AI Execution OS
Model Serving Platform
Deployment / Replica / Instance
KV Cache Fabric
Model Locality Scheduler
Training Scheduler
General GPU Cloud
General Workflow Engine
~~~

如果未来业务确实需要，应建立新的架构项目，通过 Agent Job API 复用 computecloud，而不是改变 computecloud 的领域模型。

## 11. 单二进制与 SQLite 策略

长期继续坚持：

> **先模块化，再服务化。**

只要满足单活动 Server 足够、SQLite write latency 可接受、WAL 可控、DB 可维护、recovery 达到 SLO，就继续保留。

不要因为“未来规模可能很大”提前引入 PostgreSQL、Redis、Kafka、etcd 或微服务拆分。

## 12. 代码模块演进建议

当前：

~~~text
internal/server
internal/worker
internal/adapter
internal/job
internal/store
internal/workspace
~~~

逐步整理为：

~~~text
internal/
├── api/
├── job/
│   ├── controller/
│   ├── scheduler/
│   ├── retry/
│   └── verifier/
├── runtime/
│   ├── codex/
│   ├── claude/
│   ├── shell/
│   └── tool/
├── worker/
├── workspace/
├── artifact/
├── auth/
├── quota/
├── audit/
└── store/
~~~

这首先是代码职责模块化，不代表拆成独立服务。

## 13. 测试演进

继续保持当前强项：

~~~text
L1 Unit
L2 Fixture Integration
L3 Multi-process Fault Injection
L4 Real Runtime / Multi-host
~~~

每个 Runtime 必须覆盖 version mismatch、start、cancel、timeout、crash、output parse、Artifact、permission failure。

每个可靠性能力必须有故障注入测试。

## 14. 建议 ADR 序列

1. ADR-003：Agent Job Executor 产品边界；
2. Attempt Generation / Fencing；
3. Retry Safety；
4. Runtime API v2；
5. Tool Capability；
6. Workspace / Artifact lifecycle；
7. Resource-aware Scheduling；
8. Multi-tenant / RBAC；
9. History / GC；
10. HA 触发条件与状态后端（仅在需要时）。

## 15. 路线图判断标准

每个新能力都先问三个问题：

1. 它是否直接提高 Agent Job 的可靠性、效率、可管理性或安全性？
2. 它是否可以在不破坏 Job / Task / Attempt 模型的前提下自然加入？
3. 如果拿掉“Agent Job”这个前提，这个能力是否变成了另一类平台？

如果第 3 个问题答案是“是”，优先放到独立系统，而不是继续扩大 computecloud。

## 16. 最终定义

computecloud 的长期演进目标是：

> **从当前可运行的多 Agent 调度器，演进成一个生产级、轻量、可靠、可扩展的分布式 Agent Job Executor。**

~~~text
v0.2  能运行
  ↓
v0.3  跑得可靠
  ↓
v0.4  能运行更多 Agent / Tool
  ↓
v0.5  更聪明地分配 Worker / Resource
  ↓
v0.6  多团队安全使用
  ↓
v0.7  大规模稳定运行
  ↓
v1.0  接口、运维、生态成熟
~~~

这条路线保持架构连续性，不再为了追求“大平台”强行改变系统本质。
