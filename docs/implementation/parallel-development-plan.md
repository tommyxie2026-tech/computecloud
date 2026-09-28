# computecloud 3–5 人并行开发计划

- 日期：2026-09-28
- 适用周期：未来 4–6 周
- 当前稳定发布基线：v0.3.2
- 当前 main 功能基线：v0.4.x Runtime / Tool / Environment Ecosystem
- 当前产品主线：Prepared Workspace / Workspace Template
- 并行旁路：RPG-4 FINAL / Goal Governance 收尾、Agent Control Approval
- 原则：**并行开发不等于架构拆服务；继续保持单 Go Server + SQLite，优先通过 package / contract / CI Gate 隔离并行工作。**

## 1. 目标

本计划解决一个工程组织问题：

> 如何让 3–5 名开发者同时开发 computecloud 的不同特性，又不因为共享 Server、SQLite schema、Worker lifecycle 和公共 contract 产生大量互相阻塞。

目标不是让每个人“各做一个功能”，而是把工作切成：

~~~text
共享契约
   ↓
独立实现
   ↓
独立 CI Gate
   ↓
受控集成
   ↓
主线验收
~~~

计划优先保证：

1. 每条 workstream 有独立目录 ownership；
2. 公共 contract 在每个开发周期开始时冻结；
3. schema 修改集中管理；
4. 每个 workstream 拥有自己的 CI Gate；
5. 并行开发不会破坏 Job -> Stage -> Task -> Attempt 的可靠性不变量；
6. Prepared Workspace 是产品主线，其他任务不能抢占主线架构方向；
7. RPG-4 完成后 RPG 正式关闭，不继续 RPG-5；
8. 任何新增能力必须能回到长期路线图中的既有主线。

## 2. 并行开发总体结构

建议采用 5 条可独立推进的 Workstream：

~~~text
                    Shared Contracts
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                 │
        ▼                 ▼                 ▼
 WS-A Prepared       WS-B Workspace     WS-C Environment /
 Workspace Core      Cache & Metrics    Scheduler Signals
        │                 │                 │
        └──────────┬──────┴──────┬──────────┘
                   │             │
                   ▼             ▼
             WS-D Goal       WS-E Control /
             Governance      Approval + CI
                   │             │
                   └──────┬──────┘
                          ▼
                    Integration Gate
                          ▼
                         main
~~~

这 5 条线并不要求 5 人才能启动。

### 2.1 3 人配置

~~~text
Dev-1
WS-A Prepared Workspace Core
+ schema owner

Dev-2
WS-B Cache / Template / Metrics
+ WS-C Scheduler signal foundation

Dev-3
WS-D RPG-4 Goal Governance
+ WS-E Approval / CI integration
~~~

### 2.2 4 人配置

~~~text
Dev-1  WS-A Prepared Workspace Core
Dev-2  WS-B Workspace Cache / Template / Metrics
Dev-3  WS-C Environment / Scheduler Signal Foundation
Dev-4  WS-D RPG-4 + WS-E Approval / CI
~~~

### 2.3 5 人配置

~~~text
Dev-1  WS-A Prepared Workspace Core
Dev-2  WS-B Workspace Template / Cache / Warm Path
Dev-3  WS-C Environment / Scheduler Signal Foundation
Dev-4  WS-D RPG-4 Goal Governance
Dev-5  WS-E Agent Control Approval / CI / Integration
~~~

推荐长期采用 4–5 人配置；3 人配置适合当前阶段启动。

# 3. Workstream A — Prepared Workspace Core

## 3.1 目标

建立 Prepared Workspace 的核心领域 contract，使 Workspace 从“Attempt 临时目录”扩展为：

~~~text
Immutable Template / Baseline
          ↓
Prepare
          ↓
Attempt-owned Writable Workspace
~~~

必须保持 ADR-009 不变量：

~~~text
two generations never share writable workspace
workspace ownership immutable per Attempt
old generation cannot contaminate new generation
cleanup unknown fails closed
~~~

## 3.2 Owner

建议 Dev-1。

代码 ownership：

~~~text
internal/workspace/**
internal/worker/workspace_*
docs/adr/*prepared-workspace*
docs/implementation/*prepared-workspace*
~~~

Dev-1 同时作为本周期 SQLite schema owner。

## 3.3 第一阶段 contract

定义：

~~~text
WorkspaceTemplate
├── template_id
├── repository_ref
├── base_commit
├── dependency_fingerprint
├── environment_fingerprint
├── runtime_fingerprint
├── tool_fingerprint
└── version

PreparedWorkspaceRef
├── template_id
├── provider
├── immutable_ref
└── prepared_at
~~~

Attempt Workspace 仍然必须：

~~~text
attempt_id
task_id
generation
path
writable=true
~~~

Template 永远不允许被 Runtime 直接写入。

## 3.4 实现任务

### PW-1 Contract

- WorkspaceTemplate model；
- canonical fingerprint；
- template version；
- validation；
- immutable TemplateRef。

### PW-2 Provider

建议接口：

~~~text
Describe
PrepareTemplate
InspectTemplate
MaterializeAttempt
ReleaseTemplate
~~~

v1 先实现 local filesystem provider。

### PW-3 Attempt materialization

~~~text
Template
  ↓
copy / reflink / cached checkout
  ↓
Attempt Workspace
  ↓
READY
  ↓
IN_USE
~~~

仍然遵守原 Workspace lifecycle。

### PW-4 Recovery

Worker restart 后：

- Template 可以 Inspect；
- Attempt Workspace 仍按 ADR-009 恢复；
- Template 不参与 Attempt cleanup proof；
- corrupt Template 不得继续 materialize。

## 3.5 CI Gate

新增：

~~~text
prepared-workspace-contract
prepared-workspace-recovery
~~~

必须测试：

- 两 Attempt 不共享 writable path；
- Template 不可写；
- Template fingerprint mismatch；
- Worker restart；
- stale TemplateRef；
- materialization crash；
- cleanup 不删除仍被引用 Template。

## 3.6 DoD

~~~text
同一 repository baseline
连续执行 10 个 Job
至少 90% 可复用相同 Template
且不破坏 Attempt isolation
~~~

# 4. Workstream B — Workspace Template / Cache / Warm Path

## 4.1 目标

在 WS-A contract 之上实现实际性能收益。

该线不修改 Workspace ownership 语义，只优化：

~~~text
prepare latency
checkout latency
dependency setup latency
warm-hit ratio
disk usage
~~~

## 4.2 Owner

建议 Dev-2。

代码 ownership：

~~~text
internal/workspace/cache/**
internal/workspace/template/**
internal/worker/*workspace_cache*
internal/maintenance/*workspace*
scripts/ci_prepared_workspace*
docs/validation/prepared-workspace*
~~~

原则：Dev-2 不修改 WS-A 的 public contract；需要修改必须通过 Contract Change PR。

## 4.3 实现任务

### PWC-1 Cached Repository Baseline

支持：

~~~text
repository_ref + base_commit
→ cached immutable checkout
~~~

### PWC-2 Dependency Fingerprint

初期不做通用 package manager。

只定义：

~~~text
dependency_fingerprint
~~~

由 Job / Workspace Template Provider 传入。

未来 npm/pip/go/maven adapter 可以独立扩展。

### PWC-3 Materialization Strategy

按宿主能力选择：

~~~text
reflink
hardlink-safe-copy
recursive copy
git worktree
~~~

Writable files 最终必须隔离。

### PWC-4 Warm Workspace Pool

可选：

~~~text
template_id
   ↓
N prepared materialization slots
~~~

但 warm slot 在绑定 Attempt 后必须获得新的 writable ownership。

### PWC-5 GC

Template Cache：

~~~text
ACTIVE
STALE
DELETING
DELETED
~~~

GC 与 Attempt Workspace GC 分离。

## 4.4 Metrics

新增：

~~~text
workspace_prepare_seconds
workspace_template_hit_total
workspace_template_miss_total
workspace_materialize_seconds
workspace_template_bytes
workspace_template_gc_total
~~~

## 4.5 Benchmark

最少建立：

~~~text
cold checkout
warm template hit
dependency-heavy fixture
10 concurrent attempts
worker restart after materialize
~~~

目标：

~~~text
warm prepare P50 <= cold prepare 的 40%
warm prepare P95 <= cold prepare 的 60%
~~~

具体数值后续以真实 benchmark 调整。

## 4.6 CI Gate

~~~text
prepared-workspace-cache
prepared-workspace-benchmark
~~~

benchmark Gate 初期只防明显性能回退，不用严格绝对阈值卡 main。

# 5. Workstream C — Environment Readiness / Scheduler Signal Foundation

## 5.1 目标

为 v0.5 Agent-aware Scheduler 提前准备数据，但**不提前重写 Scheduler**。

Prepared Workspace 完成后，Worker 应能报告：

~~~text
environment readiness
workspace template availability
repository affinity
startup cost
prepare latency
~~~

本阶段只建设 signal contract 和可观测数据。

## 5.2 Owner

建议 Dev-3。

代码 ownership：

~~~text
internal/environment/**
internal/job/*requirements*
internal/server/*scheduler*
internal/worker/*capability*
docs/adr/*scheduler*
docs/design/*scheduling*
~~~

注意：

> 本阶段不改变 Scheduler 的 Plan，不引入模型调度，不做 GPU topology scheduler。

## 5.3 Signal Contract

建议：

~~~text
WorkerExecutionSignal
├── runtime_ready
├── environment_ready
├── template_ids[]
├── repository_refs[]
├── workspace_prepare_p50_ms
├── environment_startup_p50_ms
├── free_slots
└── observed_at
~~~

区分：

~~~text
Capability
= 能不能做

Readiness
= 现在做的成本

Affinity
= 在哪里做更划算
~~~

## 5.4 实现任务

### SIG-1 Advertisement extension

WorkerHello / capability projection 增加 readiness hints。

### SIG-2 Scheduler observation

Scheduler 能读取信号，但第一阶段只记录：

~~~text
candidate
filter reason
readiness hints
selected worker
~~~

不改变现有主排序。

### SIG-3 Explainability

增加 scheduler decision evidence：

~~~text
worker-a:
  template_hit: true
  repo_affinity: true
  estimated_prepare_ms: 120

worker-b:
  template_hit: false
  estimated_prepare_ms: 3500
~~~

### SIG-4 Feature flag scoring

第二阶段才允许：

~~~text
prepared_workspace_score_enabled=false
~~~

默认关闭。

CI 验证后再打开实验模式。

## 5.5 CI Gate

~~~text
scheduler-readiness-contract
scheduler-explainability
~~~

负向测试：

- stale readiness 不参与决策；
- capability 不满足时 affinity 不能覆盖硬过滤；
- credential/security constraint 永远高于 workspace affinity。

# 6. Workstream D — RPG-4 FINAL / Goal Governance

## 6.1 目标

完成 RPG 最后一阶段，并正式关闭 RPG。

这条线不是新的产品主线，只补齐：

~~~text
NEEDS_APPROVAL
      ↓
durable decision
      ↓
resume / abort / budget change
~~~

## 6.2 Owner

建议 Dev-4；3–4 人配置下可由 Dev-3/Dev-4 合并负责。

代码 ownership：

~~~text
internal/goal/**
internal/governance/**
docs/adr/0018-*
docs/implementation/rpg-*
~~~

## 6.3 范围冻结

RPG-4 只实现：

1. durable approval；
2. approval idempotency；
3. actor / reason / audit；
4. token / cost budget；
5. budget increase authorization；
6. constraint change authorization；
7. approve-next-replan；
8. reject / abort。

明确不实现：

- 通用 RBAC 平台；
- GUI；
- 多级审批工作流；
- Policy DSL；
- LLM 自动审批；
- RPG-5。

## 6.4 Goal Governance API

建议内部 contract：

~~~text
ApprovalRequest
├── approval_id
├── goal_id
├── expected_plan_revision
├── expected_graph_generation
├── action
├── requested_change
├── reason
└── created_at

ApprovalDecision
├── actor
├── decision
├── reason
├── decided_at
└── resulting_goal_version
~~~

## 6.5 支持动作

~~~text
APPROVE_NEXT_REPLAN
INCREASE_BUDGET
PROVIDE_EVIDENCE
CHANGE_CONSTRAINT
REJECT
ABORT
~~~

CHANGE_GOAL 不作为普通 approval action。

真正改变 objective 应创建新 Goal 或显式 Goal revision ADR，避免偷偷修改原目标。

## 6.6 Token / Cost

增加：

~~~text
max_tokens
consumed_tokens
max_cost_units
consumed_cost_units
~~~

Runtime 无法报告 token 时：

~~~text
usage_complete=false
~~~

不得伪造 0 消耗。

## 6.7 DoD

RPG 完成条件：

~~~text
RPG-1 ✅
RPG-2 ✅
RPG-3 ✅
RPG-4 ✅
--------------
RPG CLOSED
~~~

完成后：

- 删除 roadmap 中“下一 RPG”概念；
- 后续治理进入 v0.6 Enterprise Governance；
- Re-plan Guard 只做维护。

## 6.8 CI Gate

~~~text
goal-governance
replan-approval-negative
~~~

必须测试：

- stale approval；
- duplicate approval；
- unauthorized budget increase；
- approval replay；
- approval after Goal terminal；
- approval 后 generation fencing；
- token/cost budget exhausted；
- Planner/Evaluator 不能自批准。

# 7. Workstream E — Agent Control Approval / Integration / CI

## 7.1 目标

让 Agent Control 的 Approval 表面复用 WS-D 的 Goal Governance / Runtime Approval 能力，而不是创建第二套 approval truth。

## 7.2 Owner

建议 Dev-5；3–4 人配置下与 WS-D 合并。

代码 ownership：

~~~text
api/control/**
internal/control/**
internal/server/control_*
internal/worker/control_*
scripts/ci_agent_control_*
docs/design/agent-control-protocol.md
docs/implementation/agent-control-protocol-plan.md
~~~

## 7.3 关键原则

已有：

~~~text
api/control/v1alpha1/approval.schema.json
~~~

但 Approval 的事实源必须区分两类：

~~~text
Runtime Approval
= 当前 Attempt/Session 中的工具/动作审批

Goal Governance Approval
= Re-plan / Budget / Constraint 的控制面审批
~~~

它们可以共享：

~~~text
operation id
actor
audit envelope
idempotency
generation fencing
~~~

但不能混成同一个领域对象。

## 7.4 实现任务

### ACP-A Approval projection

Control API 能读取 pending approval。

### ACP-B Decision command

复用 durable control operation ledger：

~~~text
principal_id
operation_id
expected_generation
request_hash
receipt
~~~

### ACP-C Runtime capability fencing

Runtime 没有 approval capability：

~~~text
CAPABILITY_UNAVAILABLE
~~~

不能模拟成功。

### ACP-D Goal Governance bridge

Control Client 可以展示 Goal approval，但调用的是 Goal Governance API，不由 Runtime Adapter 处理。

### ACP-E CI matrix

~~~text
Codex unsupported approval -> fail closed
Claude unsupported approval -> fail closed
Goal governance approval -> durable
stale generation -> rejected
duplicate operation -> replay same receipt
~~~

# 8. 共享 Contract 冻结机制

并行开发最大的风险不是代码量，而是共享 contract 被频繁改动。

因此每个 1–2 周开发周期开始时必须冻结：

~~~text
C1 WorkspaceTemplate contract
C2 PreparedWorkspaceRef
C3 Environment readiness signal
C4 Goal Approval contract
C5 Control Approval envelope
C6 schema target version
~~~

## 8.1 Contract Change PR

若开发过程中必须修改公共 contract：

~~~text
feature PR
    ✗ 不直接改共享 contract

先提交：
contract-change PR
    ↓
2 reviewers
    ↓
merge
    ↓
各 workstream rebase
~~~

## 8.2 Schema Owner

每个周期仅 1 人拥有：

~~~text
internal/store/migrations.go
internal/store/migrations_test.go
server schema target
~~~

推荐 Dev-1。

其他开发者需要 schema：

1. 提 migration request；
2. schema owner 分配版本 / column/table；
3. 单独 migration PR；
4. feature PR 在其后合并。

避免多人同时修改 migration 文件。

# 9. Git / PR 并行策略

采用短分支：

~~~text
feat/pw-template-contract
feat/pw-cache
feat/scheduler-readiness
feat/goal-governance
feat/control-approval
~~~

每个 PR：

- 尽量 < 800 行有效代码；
- 一个领域变更；
- 一个独立 CI Gate；
- 不混 unrelated refactor；
- 不顺便格式化其他目录。

## 9.1 合并顺序

每个周期按：

~~~text
1 contract
2 migration
3 independent implementation
4 integration
5 docs / README
~~~

并行实现可以同时进行，但 main merge 要遵守依赖顺序。

# 10. CI 架构

新增 Gate 必须避免全部串行。

建议矩阵：

~~~text
Fast Contract Gates
├── unit
├── schema
├── prepared-workspace-contract
├── scheduler-readiness-contract
├── goal-governance
└── agent-control-schema

Integration Gates
├── prepared-workspace-recovery
├── prepared-workspace-cache
├── environment-execution-flow
├── agent-control-dispatch
├── replan-approval-negative
└── task-flow

Slow / Nightly
├── benchmark
├── capacity
├── long-running
├── fault injection
└── multi-host
~~~

PR 必须通过 Fast + 相关 Integration。

不要求每个小 PR 都等待全部长耗时 Gate。

main / release candidate 再跑完整矩阵。

# 11. 4 周并行实施计划

## Week 0 / 2–3 天 — Contract Freeze

所有人共同完成：

- Prepared Workspace ADR；
- WorkspaceTemplate / PreparedWorkspaceRef；
- readiness signal contract；
- Goal Governance Approval contract；
- schema vNext 规划；
- CI Gate 名称；
- code ownership。

退出条件：

~~~text
public contracts reviewed
migration allocation frozen
directory ownership frozen
~~~

## Week 1 — 独立骨架

### Dev-1 / WS-A

- WorkspaceTemplate model；
- Provider interface；
- local provider skeleton；
- unit tests。

### Dev-2 / WS-B

- cache layout；
- template index；
- repository baseline cache；
- benchmark fixture。

### Dev-3 / WS-C

- readiness signal model；
- Worker advertisement；
- scheduler observation only。

### Dev-4 / WS-D

- Goal approval schema；
- durable approval store；
- token/cost budget schema。

### Dev-5 / WS-E

- Control approval projection；
- operation envelope；
- approval negative fixture。

Week 1 原则：

> 各线均可单独编译测试，但不要求端到端功能完整。

## Week 2 — 核心行为

### WS-A

- MaterializeAttempt；
- lifecycle integration；
- restart recovery；
- corruption fencing。

### WS-B

- cache hit/miss；
- materialization strategy；
- GC；
- metrics。

### WS-C

- scheduler explainability；
- stale signal handling；
- feature flag scoring skeleton。

### WS-D

- approve/reject/abort；
- budget increase；
- approval idempotency；
- generation fencing。

### WS-E

- Runtime approval read/write；
- Goal Governance projection；
- Codex/Claude fail-closed matrix。

Week 2 退出条件：

~~~text
each workstream has dedicated CI gate
~~~

## Week 3 — 集成周

本周减少新功能，重点交叉集成。

### Integration 1

~~~text
Prepared Workspace
        +
EnvironmentProvider
        +
Attempt lifecycle
~~~

### Integration 2

~~~text
Prepared Workspace metrics
        +
Scheduler readiness signal
~~~

### Integration 3

~~~text
RPG-4 Goal Governance
        +
Agent Control Approval
~~~

### Integration 4

~~~text
Goal governance
        +
generation fencing
        +
audit
~~~

重点故障注入：

- Worker restart；
- Server restart；
- duplicate operation；
- stale generation；
- corrupted cache；
- template GC race；
- Approval replay。

## Week 4 — 收敛 / Release Candidate

不新增架构能力。

完成：

- benchmark；
- CI flake 清理；
- migration rollback / backup verification；
- docs；
- README；
- changelog；
- compatibility matrix；
- production checklist。

目标状态：

~~~text
Prepared Workspace MVP      ✅
Workspace Cache MVP         ✅
Readiness Signal Foundation ✅
RPG-4                       ✅
RPG CLOSED                  ✅
Approval integration        ✅
~~~

然后主线正式进入：

~~~text
Prepared Workspace optimization
        ↓
v0.5 Agent-aware Scheduler
~~~

# 12. 6 周扩展版本

如果团队希望留出更低风险节奏：

~~~text
Week 1  Contract
Week 2  Core implementation
Week 3  Core implementation
Week 4  Integration
Week 5  Fault / Performance
Week 6  RC / Documentation
~~~

5 人团队推荐 4 周；
3 人团队推荐 6 周。

# 13. 每人交付边界

| Workstream | Primary Owner | 可以修改 | 尽量不要修改 |
| --- | --- | --- | --- |
| WS-A Prepared Workspace Core | Dev-1 | workspace / worker workspace / migrations | control / goal / scheduler |
| WS-B Cache & Warm Path | Dev-2 | workspace cache / maintenance / benchmark | core lifecycle contract |
| WS-C Readiness & Scheduler Signal | Dev-3 | environment / capability / scheduler observation | goal / control |
| WS-D Goal Governance | Dev-4 | goal / governance / budget | workspace / runtime |
| WS-E Control Approval & CI | Dev-5 | control / server control / worker control / scripts | goal internal decision logic |

共享文件：

~~~text
README.md
CHANGELOG.md
docs/implementation/long-term-roadmap.md
internal/store/migrations.go
Makefile
.github/workflows/ci.yml
~~~

采用指定 owner，其他人不要在 feature PR 中顺手修改。

# 14. Daily / Weekly 协作机制

## Daily

每人只报告：

~~~text
1. yesterday merged
2. today's merge target
3. contract blocker
4. CI blocker
~~~

重点只讨论 blocker，不做状态朗读。

## 每周两次 Integration Window

例如：

~~~text
Tue
Contract / dependency integration

Fri
main stabilization / full CI
~~~

Integration Window 前停止修改公共 contract。

# 15. Definition of Ready

任务可以开始并行开发前必须满足：

- owner 明确；
- 输入 contract 已存在；
- 不变量已写清；
- 依赖 PR 明确；
- 测试入口明确；
- 预计修改目录明确；
- 是否需要 migration 已确认。

否则任务不能进入开发，只能留在 design。

# 16. Definition of Done

任何 Feature 完成必须同时具备：

~~~text
Code
+ Unit Test
+ Negative Test
+ Dedicated CI Gate
+ Recovery Semantics
+ Metrics / Observable Evidence
+ Docs
~~~

涉及 durable state 的还必须：

~~~text
restart test
idempotency test
stale-generation test
migration test
~~~

# 17. 主线合并 Gate

Prepared Workspace 主线要进入下一阶段，至少满足：

### Correctness

- 不共享 writable Workspace；
- retry generation 完全隔离；
- Template immutable；
- stale TemplateRef rejected；
- restart recovery；
- GC 可恢复。

### Performance

- warm path 有可重复收益；
- cache hit/miss 可观测；
- disk growth 可控制。

### Scheduler readiness

- readiness signal 可读取；
- capability hard filter 优先；
- affinity 只作为 soft signal。

### Governance

- RPG-4 完成；
- RPG CLOSED；
- approval durable；
- token/cost budget durable；
- no hidden autonomy。

### CI

- workspace contract / recovery；
- scheduler readiness；
- goal governance；
- approval negative；
- existing task / retry / artifact / environment / control Gates 全部保持通过。

# 18. 后续并行开发模式

完成本周期后，下一阶段可继续用同样的 5 路结构：

~~~text
A Scheduler Filter / Capability
B Scheduler Score / Affinity
C Private Worker / Trust Domain
D Governance / Policy
E Control Client / Observability
~~~

注意这不是新的五个产品，而是同一个 Agent Job Executor 的并行工程分工。

# 19. 推荐的当前团队排期

如果现在立即启动 5 人：

| Dev | 未来 4 周主任务 | Secondary |
| --- | --- | --- |
| Dev-1 | Prepared Workspace Core | schema owner |
| Dev-2 | Template Cache / Warm Path | benchmark |
| Dev-3 | Readiness Signal / Scheduler Foundation | Environment metrics |
| Dev-4 | RPG-4 FINAL / Goal Governance | token-cost budget |
| Dev-5 | Agent Control Approval / Integration | CI orchestration |

如果只有 3 人：

| Dev | 主任务 |
| --- | --- |
| Dev-1 | WS-A + schema |
| Dev-2 | WS-B + WS-C |
| Dev-3 | WS-D + WS-E |

优先级始终保持：

~~~text
P0 Prepared Workspace correctness
P0 Reliability invariants
P1 RPG-4 closure
P1 Workspace performance
P1 Readiness signal
P2 Control UI / convenience
~~~

# 20. 最终原则

这轮并行计划的核心不是“让五个人同时写代码”，而是：

> **让五个人可以同时产生可合并、可验证、低冲突的增量。**

因此统一采用：

~~~text
Contract First
   ↓
Directory Ownership
   ↓
Independent CI Gate
   ↓
Small PR
   ↓
Integration Window
   ↓
Mainline
~~~

并保持 computecloud 的架构原则：

> **一个 Agent Job Control Plane、一套 durable execution truth、一个 Server state authority；并行开发不能演化成多套控制面。**
