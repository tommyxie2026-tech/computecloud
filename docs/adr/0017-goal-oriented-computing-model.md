# ADR-017：Goal-oriented Computing Model

- 日期：2026-09-28
- 状态：Accepted
- 依赖：[ADR-003：Agent Job Executor 产品边界](0003-agent-job-executor-product-scope.md)、[ADR-004：Agent-aware 执行语义](0004-agent-aware-execution-semantics.md)
- 影响范围：领域模型、执行图、调度、Artifact、Evaluator、Retry/Re-plan、长期路线图

## Context

computecloud 已经稳定形成 `Job -> Stage -> Task -> Attempt` 可靠执行内核，并围绕 generation fencing、Retry Safety、Artifact Lifecycle、Workspace、Runtime、Tool、Environment 和 Agent-aware Scheduler 持续演进。

但该模型描述的是“一个已经被拆好的 Job 如何可靠执行”，还不能完整描述 AI Agent 的目标驱动计算过程：

- 用户或上游系统首先给出的是 Goal，而不是 Task；
- Goal 需要被解析为可版本化 Plan；
- Plan 需要编译为可执行 Execution Graph；
- 执行结果不仅需要成功退出，还需要语义 Evaluator 判断是否真正满足目标；
- Evaluator 失败后可能不是简单 Retry，而是需要修改 Plan 并产生新一代 Execution Graph；
- Agent Worker、Runtime、Tool、Artifact 和执行环境都是目标完成过程中的执行资源与证据。

因此需要在现有执行内核之上建立统一的 Goal-oriented Computing Model，而不是继续以 Map/Reduce 或固定 Stage 作为最高层计算抽象。

## Decision

computecloud 的长期计算模型升级为：

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
  └──────────────→ Plan / Execution Graph (next generation)
~~~

该模型定义为 **Goal-oriented Computing**：系统接收目标，通过计划、执行图、调度、执行、产物和评估闭环，将 Goal 收敛为 Verified Outcome。

### 1. Goal

Goal 表示调用方真正希望系统完成的结果，而不是某个具体命令。

Goal 至少包含：

- goal_id；
- objective；
- constraints；
- acceptance criteria；
- context references；
- policy / budget / deadline；
- owner / project；
- lifecycle state。

Goal 默认不可因执行过程被静默改写；Re-plan 只能产生新的 Plan revision。

### 2. Plan

Plan 是 Goal 的一个可版本化求解策略：

~~~text
Goal
 ├── Plan revision 1
 ├── Plan revision 2
 └── Plan revision N
~~~

Plan 至少记录：

- plan_id；
- goal_id；
- revision；
- assumptions；
- decomposition；
- dependencies；
- required capabilities；
- evaluation strategy；
- provenance。

Planner 可以来自上游 AI Execution OS、外部 Agent、内置 Planner Provider 或人工输入。computecloud 不要求所有 Planner 必须内置。

### 3. Execution Graph

Plan 必须先编译为 Execution Graph，再进入调度。

Execution Graph 是**受控、版本化、可恢复的执行图**，不是任意通用 Workflow DSL。

~~~text
Plan revision
      ↓ compile
Execution Graph generation
      ├── Node A
      ├── Node B
      └── Node C
~~~

每个 Graph Node 最终映射到现有可靠执行内核：

~~~text
Execution Graph Node
        ↓
Job / Stage
        ↓
Task
        ↓
Attempt
~~~

因此现有 `Job -> Stage -> Task -> Attempt` 不被废弃，而是成为 Execution Graph 的 durable execution substrate。

Graph 必须支持：

- dependency；
- fan-out / fan-in；
- barrier；
- conditional continuation；
- bounded dynamic expansion；
- graph generation；
- graph/node status；
- provenance；
- cancellation / fencing。

不引入为了替代 Temporal/Airflow 而设计的任意循环语言。

### 4. Scheduler

Scheduler 消费的是 Ready Graph Node，而不是未经解释的 Goal。

调度仍坚持 Agent-aware 原则：

1. Runtime / Agent Capability；
2. Security / Permission；
3. Credential / Account Availability；
4. Environment / Workspace / Repository Affinity；
5. Network / Tool Reachability；
6. Queue / Concurrency；
7. CPU / Memory / Disk；
8. Optional Accelerator。

Scheduler 不负责修改 Goal 或重新制定 Plan。

### 5. Worker

Worker 是 Execution Graph Node 的实际执行承载点。

Worker 继续负责 Runtime、Tool、Environment、Workspace、Attempt、lease、heartbeat、fencing、event 和 Artifact upload，不承担全局 Planner/Scheduler/Evaluator 决策。

### 6. Artifact

Artifact 从“任务输出文件”升级为“计算闭环中的证据对象”。

Artifact 可以包括：

- code / patch；
- document；
- test report；
- logs；
- structured result；
- metrics；
- intermediate findings；
- verifier evidence；
- plan/evaluation evidence。

Artifact 必须继续绑定 Attempt generation，并保留 provenance。Evaluator 只能消费 ACCEPTED/PUBLISHED 或明确允许的 STAGED Artifact。

### 7. Evaluator

Evaluator 是 Goal-oriented Computing 与传统 Job Scheduler 的关键区别。

传统执行成功通常只表示：

~~~text
process exit = 0
~~~

Goal-oriented Computing 的成功必须回答：

~~~text
Does the produced outcome satisfy the Goal acceptance criteria?
~~~

Evaluator 输入至少包括：

- Goal / acceptance criteria；
- active Plan revision；
- Execution Graph generation；
- Artifact/evidence；
- verifier/test result；
- execution metadata。

Evaluator 输出必须结构化：

~~~text
ACCEPT
RETRY
RE_ROUTE
RE_PLAN
FAIL
NEEDS_APPROVAL
~~~

Evaluator 可以是 deterministic verifier、test suite、policy engine、LLM judge、human approval 或组合，但最终决策必须可审计。

### 8. Re-plan

Re-plan 不是 Task Retry。

~~~text
Retry:
same logical task
same plan
new Attempt generation

Re-plan:
same Goal
new Plan revision
new Execution Graph generation
~~~

Re-plan 必须满足：

- 显式 reason；
- 旧 Plan/Graph 不覆盖；
- graph generation fencing；
- 已接受 Artifact 可按 provenance 被新 Plan 引用；
- 不允许旧 generation 的迟到结果污染新 generation；
- 受 max_replans / budget / deadline / policy 限制；
- 必要时进入 human approval。

### 9. 两层稳定模型

computecloud 以后同时维护两层模型：

~~~text
Goal-oriented Computing Layer
Goal -> Plan -> Execution Graph -> Evaluator -> Re-plan
                         |
                         v
Durable Execution Layer
Job -> Stage -> Task -> Attempt -> Artifact/Workspace
                         |
                         v
Execution Plane
Scheduler -> Worker -> Runtime/Tool/Environment
~~~

上层负责“做什么、是否完成、是否需要改变策略”。

下层负责“如何可靠地执行一次具体工作”。

### 10. 状态闭环

推荐的主状态循环：

~~~text
GOAL_CREATED
   ↓
PLANNED
   ↓
GRAPH_READY
   ↓
RUNNING
   ↓
EVALUATING
   ├── ACCEPT ─────→ SUCCEEDED
   ├── RETRY ──────→ RUNNING
   ├── RE_ROUTE ───→ SCHEDULING
   ├── RE_PLAN ────→ PLANNING (new revision/generation)
   ├── NEEDS_APPROVAL
   └── FAIL ───────→ FAILED
~~~

## Compatibility

现有 Job API 保持有效。

兼容模式：

~~~text
Legacy Job Submit
      ↓
Synthetic Goal / Plan / Graph
      ↓
existing Job -> Stage -> Task -> Attempt
~~~

因此升级不要求立即重写 v0.2-v0.4 客户端和 Worker。

新 API 可以逐步增加：

- Goal API；
- Plan revision API；
- Execution Graph API；
- Evaluation API；
- Re-plan event/API。

## Reliability Invariants

新增以下长期不变量：

1. 一个 Goal 可以有多个 Plan revision，但只有一个 active revision；
2. 一个 Plan revision 可以生成多个 Graph generation，但旧 generation 不得提交为当前最终结果；
3. Re-plan 不复用旧 generation 的可写 Workspace；
4. Evaluator 决策必须有 evidence/provenance；
5. ACCEPT 必须指向明确的 Artifact set / Verified Outcome；
6. Retry 与 Re-plan 在状态和审计上严格区分；
7. Scheduler 不修改 Plan；
8. Worker 不决定全局 Goal 是否完成；
9. 任何 late completion 都必须通过 generation fencing；
10. Re-plan 必须有次数、预算、时间或策略上界，避免无界自治循环。

## Consequences

正面影响：

- 从固定 Job 执行模型升级为完整 Agent 目标计算模型；
- 保留现有可靠性内核，不需要推翻 Job/Stage/Task/Attempt；
- Planner、Scheduler、Worker、Evaluator 职责清晰；
- 支持真正的执行后反馈与动态策略调整；
- Artifact provenance 可以直接成为 Evaluation evidence；
- 为未来多 Agent、异构 Runtime、远程客户端和上游 AI Execution OS 提供稳定契约。

代价：

- 需要新增 Goal/Plan/Graph/Evaluation 的持久化对象；
- Graph generation 与 Attempt generation 形成两级 fencing；
- API、事件和 Trace 需要增加 goal_id/plan_id/graph_id/evaluation_id；
- Planner/Evaluator contract tests 必须建设；
- 必须严格限制 Re-plan，防止系统退化成不可审计的自治循环。

## Product Boundary

本 ADR 不把 computecloud 改成通用 AI Execution OS。

computecloud 负责：

- Goal execution contract；
- versioned Plan/Graph；
- durable execution；
- Agent-aware scheduling；
- Artifact evidence；
- evaluation/re-plan state machine。

Planner/Evaluator 的“智能实现”允许外置为 Provider，由独立 AI Execution OS 或其他 Agent 系统提供。

因此长期组合可以是：

~~~text
External AI Execution OS / Planner
             ↓
Goal / Plan Contract
             ↓
computecloud Goal-oriented Execution
             ↓
Worker / Runtime / Tool / Environment
~~~

也可以是：

~~~text
Client
  ↓ Goal
computecloud
  ├── Planner Provider
  ├── Execution Graph
  ├── Scheduler
  ├── Worker
  └── Evaluator Provider
~~~

## Canonical Model

从本 ADR 起，computecloud 的最高层计算模型统一写为：

> **Goal -> Plan -> Execution Graph -> Scheduler -> Worker -> Artifact -> Evaluator -> Re-plan**

而 `Job -> Stage -> Task -> Attempt` 统一定义为其下层 Durable Execution Model。
