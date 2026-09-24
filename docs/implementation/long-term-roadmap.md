# computecloud 长期演进路线图（专家评审修订版）

- 项目：computecloud
- 日期：2026-09-24
- 当前稳定基线：v0.2.0
- 目标定位：从轻量多 Agent / Job 调度器演进为 **AI Execution Infrastructure / AI Execution OS**
- 架构依据：[AI Execution OS 总体架构](../design/ai-execution-os-architecture.md)
- 当前实现依据：[v0.2 实施计划](v0.2-plan.md)、[v0.2 验证记录](../validation/v0.2-results.md)、[v0.2 运行指南](v0.2-runbook.md)

> 本文是长期技术演进顺序，不是固定发布日期承诺。每个阶段都必须先满足可验证的退出门槛，再进入下一阶段。

## 0. 专家评审后的架构修订

本次评审保留“轻量、渐进、按真实瓶颈引入复杂度”的总方向，但做五项关键修正：

1. **Workload 不再等同于 Execution**：长期分为 `Execution`、`Deployment`、`Session` 三种生命周期。Execution 面向有限任务；Deployment 面向长驻模型服务；Session 面向跨 Turn 的交互上下文。
2. **Admission Control 前移**：明确 `Admission = 是否允许执行`、`Router = 偏好去哪里`、`Scheduler = 当前实际放哪里`、`Runtime = 如何执行`。
3. **Runtime 改为 Capability Interface**：避免单一巨型 Runtime 接口；按 Execution、Serving、Streaming、Session、Checkpoint、Tool 等能力组合。
4. **Locality Metadata 前移**：v0.5 Storage 阶段开始建设 Location Directory，v0.6 增加 CacheLocation，v0.7 再用于调度决策。
5. **Observability / Security 前移**：v0.9 仍负责完整生产治理，但 request/workload/execution/task/attempt/placement/node/runtime ID、credential_ref、policy_ref 等基础对象从 P0/P1 开始贯穿。

目标生命周期模型：

    Workload
    ├── Execution  -> Stage -> Task -> Attempt
    ├── Deployment -> Replica -> Instance
    └── Session    -> Turn -> Execution

这三类对象共享 Admission、Scheduler、Placement、Node/Runtime、Memory、Storage 和 Tool 基础设施，但不共享同一种生命周期状态机。

## 1. 当前实现基线

v0.2 已经具备一个可构建、可部署、可恢复的轻量执行系统雏形。

### 1.1 已实现

- 单 Go 二进制；
- 单活动 Server；
- SQLite 持久化；
- 多节点 Worker；
- gRPC Worker 控制流；
- Token HTTP Job API；
- MCP 工具入口；
- Job / Task / Attempt 基础执行模型；
- single 与显式 Map -> Reduce；
- Codex / Claude CLI Runtime；
- 静态模型、凭据、项目和 Worker 能力配置；
- 节点、账号、项目并发限制；
- 幂等提交；
- 持久命令与 Worker 去重；
- lease / heartbeat；
- cancel / timeout / reconcile；
- Artifact 上传、哈希校验、Reduce 输入冻结；
- 默认关闭的 Responses / SSE / compact 模型网关；
- Linux amd64 / arm64 发布包；
- CI、race、故障烟测与 fixture 容量矩阵。

当前代码主要集中在：

```text
internal/server      API、调度、Job、Gateway、MCP
internal/worker      Worker、执行、事件、恢复
internal/adapter     Codex / Claude Runtime 适配
internal/job         Job 规格与报告
internal/store       SQLite / migration
internal/workspace   Workspace
internal/maintenance Backup
api/agent            Worker / Runtime RPC
api/job              Job Schema
```

### 1.2 当前尚未完成

- 真实 Codex / Claude 固定版本生产验收；
- 独立主机间 TLS / 网络 / 故障矩阵；
- 真实上游模型兼容与计费对账；
- 真实模型负载下容量验证；
- 自动重试和多代 Attempt；
- 通用 Execution / Stage；
- ResourceClaim / Placement；
- 推理 Runtime；
- 模型 / 数据 / Artifact 的统一 Storage Fabric；
- KV / Context Memory Fabric；
- Locality-aware Scheduler；
- Session / 交互审批；
- 通用 Tool Fabric；
- 多租户生产隔离；
- 控制面 HA；
- 多集群全局调度。

因此推荐的演进顺序修订为：

```text
v0.2 做实 + Observability baseline
   ↓
Execution Kernel + Admission + Spec/Status
   ↓
Workload Model + Runtime Capability + Compute Fabric
   ↓
Storage Fabric + Locality Directory v1
   ↓
Memory / Cache Fabric + CacheLocation
   ↓
AI-native Locality-aware Scheduler
   ↓
Agent Session + Tool Execution
   ↓
Production Control Plane
   ↓
Multi-cluster / HA
```

## 2. 长期目标

最终希望形成：

```text
                       AI Execution OS
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
      API Fabric        Execution Fabric      Governance
          │                   │
   Token Gateway        Execution Kernel
   Intelligent Router         │
                      Scheduler / Placement
                              │
             ┌────────────────┼────────────────┐
             │                │                │
      Compute Fabric     Memory Fabric    Storage Fabric
             │                │                │
       GPU/CPU/VM         KV/Context      Model/Dataset/
             │                              Artifact
             └────────────────┬────────────────┘
                              │
                       Runtime / Tool
```

产品边界：

> computecloud 不只是一个 GPU 调度器、模型代理或 Agent RPC Server，而是一套统一管理 **Model + Agent + Tool + Compute + Memory + Storage + Workflow** 的 AI Execution Infrastructure。

## 3. 演进总览

| 阶段 | 建议版本 | 核心目标 | 主要交付 |
| --- | --- | --- | --- |
| P0 | v0.2.x | 真实环境做实 | 双机、真实 CLI、真实 MCP、真实上游、容量与故障基线 |
| P1 | v0.3 | Execution Kernel + Admission | Execution/Stage、Spec/Status、Admission、ResourceClaim、Placement、多代 Attempt |
| P2 | v0.4 | Workload + Runtime + Compute Fabric | Execution/Deployment/Session 模型、Runtime Capability、Node Capability、推理 Runtime、GPU 资源模型 |
| P3 | v0.5 | Storage Fabric + Locality v1 | Storage URI、Provider、Model/Artifact/Workspace Store、本地缓存、Location metadata |
| P4 | v0.6 | Memory Fabric + CacheLocation | KV/Context Directory、多级 Cache、Cache Provider、CacheLocation、fail-open |
| P5 | v0.7 | AI-native Locality-aware Scheduling | Model/KV/Data/Workspace Locality、Intelligent Router、动态 Score、Placement explainability |
| P6 | v0.8 | Agent Execution Fabric | Session/Turn 落地、交互、审批、Tool Fabric、受控 DAG |
| P7 | v0.9 | Production Control Plane | 多租户、Quota/Billing、GC、SLO、隔离、治理 |
| P8 | v1.0 | 稳定 AI Execution Platform | 稳定 API、兼容策略、生产发布边界 |
| P9 | v1.x | Multi-cluster / HA | HA、Global Scheduler、跨地域、多 Provider |

优先级建议：

```text
近期：P0 -> P1 -> P2 -> P3
中期：P4 -> P5 -> P6
长期：P7 -> P8 -> P9
```

## 4. P0：v0.2.x — 先把当前系统做实

### 4.1 目标

不扩大架构范围，优先验证现有 v0.2 能否在真实受信环境稳定工作。

### 4.2 工作项

- 固定 Codex / Claude CLI 版本；
- 至少两台独立 Linux Worker；
- Server / Worker TLS；
- 节点 Token、断线重连；
- 真实 single Job；
- 跨 Worker Map/Reduce；
- Worker SIGKILL；
- Server 重启；
- 网络中断；
- MCP 真实客户端提交、查询、取消；
- G1 使用合法上游验证 Responses / SSE / compact；
- input/output token 与上游账单对账；
- 真实 Agent 任务容量压测；
- SQLite WAL / busy / write latency；
- queue latency；
- Server / Worker RSS / CPU；
- 长时间稳定运行；
- upgrade / backup / restore / rollback。

### 4.3 不做

- HA；
- PostgreSQL；
- Redis；
- 外部消息队列；
- 通用 DAG；
- GPU 调度；
- Cache Fabric。

### 4.4 退出门槛

必须具备：

```text
2+ 独立主机
真实 Codex + Claude
真实 MCP
真实上游
故障恢复
容量曲线
账单对照
升级 / 回退证据
```

只有这一阶段完成后，才开始扩大执行模型。

## 5. P1：v0.3 — Execution Kernel + Admission

这是整个架构最重要的协议定型阶段。P1 不只是新增 Execution 对象，还要把 Admission、Spec/Status、Placement 和重试 fencing 一起固定下来。

### 5.0 Spec / Status 与 Admission

从 P1 开始明确：`Spec` 表示用户/控制面希望系统做什么，接受后尽量保持不可变；`Status` 表示系统当前实际状态，由 Controller 更新。不要把用户意图、调度结果和 Runtime 状态混在同一结构中。

Admission 在 Scheduler 之前执行，负责身份、项目、Quota、Budget、并发、Runtime/Model 权限、CredentialRef 和 PolicyRef。Admission 只回答“能不能执行”，不选择节点。

### 5.1 从 Job 演进到 Execution

建议兼容映射：

```text
v0.2 Job      -> Execution
Map/Reduce    -> Stage
Task          -> Task
Attempt       -> Attempt
```

目标模型：

```text
Execution
├── Stage
│   ├── Task
│   │   └── Attempt
│   └── Task
└── Dependency
```

现有 Job API 不立即删除，而作为兼容层。

### 5.2 ResourceClaim

Task 从“指定静态 profile”逐步变成“声明需要什么能力”。

例如：

```yaml
resource_claim:
  runtime:
    any_of:
      - codex
      - claude
  cpu: 4
  memory: 8Gi
  capabilities:
    - git
    - shell
```

P1 不实现任意资源表达式，仅提供结构化固定字段。

### 5.3 Placement

新增一等 Placement 对象：

```text
ResourceClaim
   ↓
Filter
   ↓
Score
   ↓
Select
   ↓
Placement
   ↓
Attempt
```

把“选择节点”和“启动 Attempt”从概念上分离。

### 5.4 多代 Attempt / R1

实现当前已延后的自动重试：

- generation；
- 一个 Task 仅一个 active Attempt；
- 旧 generation 的 renew / event / upload / complete 全部拒绝；
- replay-safe 标记；
- 可重放 Runtime 白名单；
- backoff；
- max attempts；
- deadline remaining；
- retry accounting；
- 不确定旧执行不自动重跑；
- cancel 优先级高于 retry。

### 5.5 Store Interface 与代码演进

SQLite 继续作为当前正式状态库，但业务层开始通过 Store Interface 访问，避免未来状态后端变化时把数据库细节扩散到 Execution/Scheduler。

新增：

```text
internal/execution/
internal/admission/
internal/scheduler/
internal/resource/
```

现有：

```text
internal/server/job_schedule.go
internal/server/tasks.go
```

逐步下沉到新模块，Server 只负责 API 和 orchestration。

### 5.6 退出门槛

- 多代 Attempt 故障测试通过；
- Server 重启不产生双执行；
- 旧 Attempt 迟到结果不可覆盖；
- cancel / retry / timeout 竞态确定；
- v0.2 Job API 兼容测试通过；
- migration / downgrade 边界有明确结果。

## 6. P2：v0.4 — Workload Model + Runtime & Compute Fabric

### 6.1 Workload 生命周期模型

正式定义三类一级生命周期：

```text
Workload
├── Execution  -> Stage -> Task -> Attempt
├── Deployment -> Replica -> Instance
└── Session    -> Turn -> Execution
```

Execution 面向有限任务；Deployment 面向长驻模型服务；Session 面向跨 Turn 的交互上下文。P2 先固定 Deployment / Session 的 Spec、Status 和生命周期边界，不要求一次实现完整 Serving/Session 功能。

### 6.2 Runtime Capability Interface

把当前 Codex / Claude adapter 抽象为最小基础 Runtime + 可组合能力接口，而不是要求所有 Runtime 实现同一组方法。

```go
type Runtime interface {
    Name() string
    Version() string
    Capabilities() CapabilitySet
}

type ExecutionRuntime interface { /* Start/Stop/Status execution */ }
type ServingRuntime interface { /* Deploy/Scale/Stop serving */ }
type StreamingRuntime interface { /* stream input/output */ }
type SessionRuntime interface { /* create/resume/turn */ }
type CheckpointRuntime interface { /* checkpoint/restore */ }
type ToolRuntime interface { /* tool execution */ }
```

Scheduler 根据 Capability 匹配，不根据 Runtime 名称猜能力。

### 6.3 Node Capability

Worker 注册升级为 Node Capability：

```text
CPU
Memory
GPU / Accelerator
Local Storage
Network
Runtime
Tool
Model
Topology
```

### 6.4 推理 Runtime

至少接入一种推理 Runtime，使平台第一次同时支持：

```text
Agent Execution
+
Model Inference
```

这一步之后 computecloud 才真正从 Agent Scheduler 扩展为 AI Execution Platform。

### 6.5 GPU 资源模型

新增：

- GPU count；
- GPU model；
- VRAM；
- topology；
- allocation；
- runtime occupancy。

P2 先做整卡或显式资源单元，不急于实现复杂 GPU sharing。

### 6.6 退出门槛

- Execution / Deployment / Session 生命周期不混用；
- Codex / Claude / 推理 Runtime 通过 Capability 接入；
- Scheduler 不需要按 Runtime 写大量特殊分支；
- Node capability 能做硬过滤；
- GPU 资源不会超分配；
- Runtime start/stop/status 语义一致。

## 7. P3：v0.5 — Storage Fabric + Locality Directory v1

这一阶段把当前本地 Artifact / Workspace 文件路径升级为平台级数据访问抽象。

### 7.1 统一 URI

建议：

```text
model://
dataset://
artifact://
workspace://
checkpoint://
```

禁止上层协议依赖具体挂载路径。

### 7.2 Storage Provider

定义：

```text
Open
Read
Write
Stat
List
Delete
Checksum
Resolve
```

支持至少：

- Local Storage Provider；
- High Performance Shared File Storage Provider；
- Object Storage Provider。

具体内部共享存储产品不进入公共架构和平台协议。

### 7.3 Store

提供：

- Model Store；
- Artifact Store；
- Workspace Store；
- Dataset Store；
- Checkpoint Store。

### 7.4 Node Local Cache

模型文件支持：

```text
Shared / Object Storage
       ↓
Node Local NVMe
       ↓
Runtime
       ↓
GPU
```

把模型冷启动成本纳入可观测指标。

### 7.5 代码结构

```text
internal/storage/
├── resolver/
├── model/
├── artifact/
├── workspace/
└── provider/
    ├── local/
    ├── sharedfs/
    └── object/
```

### 7.6 Locality Directory v1

从 Storage 阶段开始记录位置，但暂不做复杂调度打分。最小对象建议包含 `ResourceRef / Location / Node / Tier / Version / Checksum / LastSeen / State`。

至少记录 Model Location、Dataset Location、Artifact Location、Workspace Location、Runtime Location。Scheduler 此阶段可以查询位置，但仍以资源正确性优先，不把 locality 作为强制策略。

### 7.7 退出门槛

- 上层无具体存储路径依赖；
- Provider 可替换；
- Location metadata 可查询且有版本/过期语义；
- Artifact checksum / version / ownership 稳定；
- Workspace 故障恢复明确；
- Local cache 可以删除后自动恢复；
- Storage 故障不能产生静默数据错误。

## 8. P4：v0.6 — Memory Fabric + CacheLocation

### 8.1 目标

为长上下文、Agent、RAG 和推理场景提供统一可淘汰缓存层。

内容：

- KV Cache；
- Prefix Cache；
- Context Cache；
- Embedding Cache；
- Tool / RAG Cache。

### 8.2 层级

```text
L0 GPU HBM
   ↓
L1 Node DRAM
   ↓
L2 Node NVMe / Cluster Cache
   ↓
L3 Shared Backend
```

### 8.3 Cache Directory

统一：

```text
Locate
Load
Store
Promote
Demote
Evict
Replicate
Invalidate
```

### 8.4 CacheLocation

在 Locality Directory v1 上增加缓存位置语义：cache namespace、model/runtime compatibility、location、tier、coverage、freshness、size、state。Location 信息允许过期，Scheduler 必须有 stale/unknown 降级路径。

### 8.5 KV Cache Key

不能只用 Prompt Hash，至少考虑：

- model revision；
- tokenizer revision；
- runtime version；
- KV format / dtype；
- TP / PP；
- attention / RoPE compatibility；
- cache schema version；
- prefix hash。

### 8.6 Fail-open

Memory Fabric 必须满足：

```text
cache hit         -> reuse
cache miss        -> recompute
cache timeout     -> recompute
cache corrupt     -> discard
cache unavailable -> bypass
```

Cache 是 performance dependency，不是 availability dependency。

### 8.7 指标

至少：

- token cache hit ratio；
- L0/L1/L2 hit；
- KV load latency；
- KV write latency；
- cache bytes；
- eviction；
- TTFT；
- TPOT；
- prefill saved tokens；
- network throughput。

### 8.8 退出门槛

只有真实负载证明：

```text
TTFT下降
GPU Prefill下降
吞吐提升
且故障可旁路
```

才允许默认启用。

## 9. P5：v0.7 — AI-native Locality-aware Scheduler

### 9.1 Locality Directory

维护：

- Runtime Location；
- Model Location；
- KV Location；
- Dataset Location；
- Workspace Location；
- Artifact Location。

例如：

```text
Model A
├── Node01 local cache
├── Node02 local cache
└── Shared Storage

Prefix X
├── Node02 GPU
├── Node01 DRAM
└── Cluster Cache
```

### 9.2 Scheduler 四阶段

固定：

```text
Filter -> Score -> Select -> Bind
```

Filter 只处理硬约束。

Score 处理：

```text
Resource
Model Locality
KV Locality
Dataset Locality
Workspace Locality
Queue
Network
Cost
```

### 9.3 Intelligent Router

Router 与 Scheduler 分开：

```text
Router
    -> Placement Preference

Scheduler
    -> Placement Decision
```

Router 可面向 API 请求做：

- model-aware；
- cache-aware；
- load-aware；
- locality-aware；
- cost-aware；
- policy-aware。

### 9.4 Workload Profile

不同工作负载使用不同 Score：

```text
Inference:
    KV / Model / GPU

Agent:
    Workspace / Tool / Runtime

Training:
    GPU Topology / Network / Dataset
```

### 9.5 Placement Explainability

每次调度至少保存候选集合摘要、Filter 拒绝原因、Score breakdown、最终选中节点以及 fallback 原因，使系统能够回答“为什么选择这个 Node”。

### 9.6 退出门槛

必须用 A/B 压测证明 locality 调度相对 round-robin / least-load 在目标工作负载上有稳定收益，并且：

- 不导致 starvation；
- 不破坏 quota；
- 不降低故障恢复能力；
- Score 可解释；
- 调度决定可追踪；
- Locality 数据缺失或过期时能退化到安全的资源调度策略。

## 10. P6：v0.8 — Agent Execution Fabric

P2 已经定义 Session 生命周期；P6 才实现完整 Session/Turn、交互、审批和恢复能力，避免到这一阶段才重新设计一级对象。

### 10.1 Session

实现：

- stable session ID；
- Runtime-native session mapping；
- session lease；
- worker affinity；
- version compatibility；
- checkpoint；
- resume。

禁止模糊使用“last session”。

### 10.2 交互

增加：

- SendInput；
- RespondApproval；
- interrupt；
- queue next；
- steer current。

不同 Runtime 的能力必须通过 capability negotiation，不假定全部支持。

### 10.3 Tool Fabric

抽象：

```text
Shell
Git
Browser
MCP
API
Database
Custom Tool
```

Tool 作为可调度 Capability，并显式携带 permission、credential_ref、network policy 与 audit identity。

### 10.4 受控 DAG

在 Execution Kernel 已稳定后，再从固定 Map/Reduce 扩展为：

```text
Stage A
  ├─ Task A1
  └─ Task A2
        ↓
Stage B
        ↓
Stage C
```

不在这一阶段直接做无限复杂 Workflow DSL。

### 10.5 退出门槛

- Agent 跨重启可以恢复；
- Session 不发生双写；
- Approval 有审计记录；
- Tool 权限隔离；
- DAG failure/cancel/retry 语义明确。

## 11. P7：v0.9 — Production Control Plane

这一阶段重点不是新 Runtime，而是“能否成为生产平台”。

### 11.1 多租户

- tenant；
- project；
- identity；
- RBAC；
- quota；
- resource pool；
- policy；
- secret references。

### 11.2 计量与 Billing

统一：

- execution duration；
- CPU / GPU allocation time；
- model token；
- storage；
- cache；
- network；
- tool execution。

先保证 metering 可追踪，再增加 chargeback。

### 11.3 Lifecycle / GC

解决当前长期运行必然出现的问题：

- old events；
- attempts；
- logs；
- artifacts；
- workspace；
- cache；
- checkpoints；
- tombstones。

GC 必须可恢复、可审计、不可删除仍被引用的数据。

### 11.4 SLO

正式建立：

- API availability；
- scheduling latency；
- queue latency；
- task launch latency；
- recovery time；
- artifact durability；
- cache hit / TTFT；
- failure rate。

### 11.5 安全隔离

根据租户可信级别支持：

- process；
- container；
- VM / sandbox。

安全隔离与 Scheduler capability 结合。

## 12. P8：v1.0 — 稳定 AI Execution Platform

v1.0 不应以“功能很多”为标准，而应满足：

### API

- Workload 生命周期模型稳定；
- Execution API 稳定；
- Deployment API 稳定；
- Session API 稳定；
- Runtime Interface 稳定；
- ResourceClaim 稳定；
- Artifact / Storage URI 稳定；
- Event schema 稳定；
- deprecation policy 明确。

### Operations

- install / upgrade / rollback；
- backup / restore；
- observability；
- capacity planning；
- incident runbook；
- version compatibility matrix。

### Workloads

至少稳定支持三类：

```text
Model Inference
Agent Execution
Multi-stage Job
```

### Production Boundary

清晰声明：

- 支持规模；
- 单集群限制；
- SQLite / state backend 限制；
- Runtime 兼容矩阵；
- Storage Provider 兼容矩阵；
- 故障边界。

## 13. P9：v1.x — Multi-cluster / HA

这一阶段必须由真实规模触发，不能提前做。

### 13.1 HA Control Plane

触发条件示例：

- 单 Server 故障恢复时间不满足 SLO；
- Server 已成为显著可用性单点；
- state write QPS 超过 SQLite 舒适区；
- 多团队需要持续控制面可用。

届时再评估：

```text
External SQL
Leader Election
Transactional Outbox
Event Bus
Multiple Controllers
```

而不是默认将它们全部引入。

### 13.2 Hierarchical Scheduler

```text
Global Scheduler
      ↓
Cluster Placement
      ↓
Cluster Scheduler
      ↓
Node Placement
```

全局层考虑：

- Region；
- GPU pool；
- model availability；
- data locality；
- cost；
- tenant policy。

### 13.3 多 Provider

支持：

- Bare Metal；
- Kubernetes；
- VM / KubeVirt；
- Cloud GPU；
- Remote Inference Provider。

全部通过 Compute / Runtime Provider 抽象接入。

## 14. SQLite 与单 Server 的长期策略

当前不要主动替换 SQLite。

建议定义“升级触发器”。

### 14.1 保留 SQLite 的条件

如果满足：

- 单活动 Controller 足够；
- write latency 可接受；
- WAL 可控；
- 数据库大小可维护；
- backup 时间可接受；
- recovery 达到 SLO；

则继续使用 SQLite。

### 14.2 考虑外部数据库的触发条件

只有实测出现：

- 持续 write contention；
- busy/backoff 明显影响调度；
- state 数据量导致维护窗口过长；
- 需要多个活动 Controller；
- 单机恢复时间无法满足 SLO；

才进入 ADR 评估。

迁移方向应抽象 Store 接口，而不是直接让业务代码依赖某个数据库。

## 15. 单进程与服务拆分策略

现阶段保持单二进制有明显优势：

- 部署简单；
- 调试简单；
- 低资源；
- 状态边界清晰。

不要为了“微服务架构”拆服务。

只有出现独立扩容或故障域需求时再拆，例如：

```text
Gateway QPS 与 Scheduler 完全不同
Memory Directory 需要独立扩展
Global Scheduler 跨集群
Billing / Metrics 数据量独立增长
```

即：

> **先模块化，再服务化。**

## 16. 向后兼容策略

每个阶段都需要：

### Schema

- monotonic version；
- forward incompatible 明确拒绝；
- migration 原子；
- backup before migration；
- downgrade boundary。

### API

新 API 优先新增，不直接破坏旧 API。

建议：

```text
/v1/jobs         保留兼容
/v1/executions   有限执行 API
/v1/deployments  长驻服务 API
/v1/sessions     交互会话 API
```

待 Execution API 稳定后，再定义 Job API deprecation 周期。

### Runtime

Runtime capability 带 version：

```text
runtime=codex
runtime_version=x
protocol_version=y
capabilities=[...]
```

Scheduler 不按名称猜能力。

## 17. 可观测性演进

### 当前

- Job / Task / Attempt；
- Worker；
- Event；
- basic process resource。

### P1-P3

增加：

- execution_id；
- placement；
- scheduler latency；
- runtime start latency；
- model load latency；
- storage latency。

### P4-P5

增加：

- cache location；
- token cache hit；
- KV load；
- locality score；
- routing reason。

### P7+

形成统一 Trace：

```text
Request
 -> Gateway
 -> Router
 -> Scheduler
 -> Placement
 -> Node Agent
 -> Runtime
 -> Memory
 -> Storage
 -> Tool
 -> Result
```

每一次 Placement 都应可回答：

> 为什么选择这个 Node？

## 18. 测试体系长期演进

保持当前 fixture 优势，但逐步增加真实层。

推荐四层：

```text
L1 Unit
L2 Fixture Integration
L3 Multi-process / Fault Injection
L4 Real Runtime / Multi-host
```

每个新 Provider 都必须至少提供：

- contract tests；
- failure tests；
- timeout；
- restart；
- wrong version；
- corruption / invalid input；
- observability。

对于 Scheduler 新策略必须保留 baseline 对比，不能只证明“能运行”。

## 19. 关键 ADR 清单

后续建议按实际进入阶段逐个写 ADR，而不是现在一次做完：

1. Execution / Stage / Spec / Status；
2. Admission Control；
3. ResourceClaim / Placement；
4. Workload：Execution / Deployment / Session；
5. Runtime Capability Interface；
6. GPU Resource Model；
7. Storage URI / Provider；
8. Locality Directory；
9. Memory / Cache Provider；
10. Router 与 Scheduler 边界；
11. Session / Turn；
12. Tool Capability；
13. State Backend 扩展触发条件；
14. Control Plane HA；
15. Multi-cluster Scheduling。

## 20. 专家评审要求长期避免的三类风险

### 20.1 过度抽象

Fabric / Provider / Registry 只有在直接服务 AI Workload 的执行、局部性、恢复或治理时才引入；不能为了架构完整性先造空抽象。

### 20.2 演变为通用 Workflow 平台

DAG、Approval、Retry、Tool 都必须服务 AI Execution，不以替代 Temporal/Airflow 等通用 Workflow 系统为目标。

### 20.3 退化为普通 GPU Scheduler

长期差异化应来自对 Model、KV、Dataset、Workspace、Agent、Runtime、GPU、Queue、Network、Cost 的联合理解和 Placement，而不是“也能调 GPU”。

## 21. 近中长期交付建议

### 近期：先完成 P0-P2

重点：

```text
真实环境
→ Execution Kernel + Admission
→ Workload Model + Runtime / GPU
```

这是最重要的主线。

### 中期：P3-P6

重点：

```text
Storage + Locality Metadata
→ Memory + CacheLocation
→ AI-native Locality Scheduler
→ Agent Session / Tool
```

这个阶段形成差异化的 AI-native 调度能力。

### 长期：P7-P9

重点：

```text
Production Governance
→ Stable v1
→ HA / Multi-cluster
```

只有真实业务规模到达后再做。

## 22. 路线图成功标准

长期演进不能只看“支持多少组件”，而应该观察：

```text
可靠性
可恢复性
端到端延迟
资源利用率
数据局部性
GPU效率
调度可解释性
部署复杂度
版本兼容性
运维成本
```

最终目标：

> 在不牺牲轻量部署、故障可理解性和兼容边界的前提下，把当前多 Agent 调度器逐步演进为可以统一管理 Execution、Deployment、Session、Model、Agent、Tool 的 AI Execution Infrastructure。
