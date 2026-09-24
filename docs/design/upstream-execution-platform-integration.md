# computecloud 与上层 AI Execution Platform 集成设计

- 项目：computecloud
- 日期：2026-09-24
- 状态：接口设计 / 迭代输入
- 产品边界：Agent-aware Distributed Job Execution Platform

## 1. 目的

computecloud 保持独立 Agent Job Executor 定位：

> 接收已经定义好的 Agent Job，并可靠地完成调度、远程执行、恢复、取消和结果交付。

它可以服务 CLI、CI、内部平台，也可以作为 aicloud 等上层 AI Execution Platform 的一个 Execution Backend。

**这种集成不会把 computecloud 变成 AI Execution OS。**

## 2. 边界

~~~text
Upstream Platform
 Goal / Planner / Router / Context / Verifier
                 │
                 │ Execution Contract
                 ▼
           computecloud API
                 │
 Job → Stage → Task → Attempt
                 │
 Agent-aware Scheduler
                 │
 Worker → Runtime → Tool / Workspace
                 │
          Result / Events
                 ▼
          Upstream Platform
~~~

computecloud 不负责：

- Goal interpretation；
- Planner；
- Model/Intelligence Router；
- RAG/Memory/Context semantics；
- business-level Verifier；
- Learning/Training Loop。

## 3. 外部 Contract 与内部模型隔离

外部请求表达 capability 和约束，不直接指定 Worker/Attempt：

~~~yaml
workload:
  type: agent
capability:
  required: [coding, git, shell]
input:
  goal: "Fix issue #123"
context:
  artifacts: [artifact://context/123]
constraints:
  timeout: 30m
  isolation: sandbox
~~~

computecloud 内部转换：

~~~text
External Execution Request
        ↓
Job
  ↓
Stage
  ↓
Task
  ↓
Attempt generation
  ↓
Worker / Runtime
~~~

禁止向上层暴露为稳定契约的内部字段包括：

~~~text
worker_id
lease_token
attempt generation internals
scheduler score internals
SQLite schema
process PID
local workspace path
~~~

## 4. 标准事件

对上层提供稳定事件：

~~~text
QUEUED
ASSIGNED
STARTED
PROGRESS
ARTIFACT_CREATED
WAITING_INPUT
COMPLETED
FAILED
CANCELLED
~~~

内部 Worker heartbeat、lease renewal、reconcile、generation fencing 不映射成上层业务语义。

## 5. Result 与 Outcome

computecloud 只保证 Execution Result：

~~~text
COMPLETED
 + output
 + artifact refs
 + logs
 + runtime metadata
 + execution metrics
~~~

它不声明上层 Goal 已完成。

~~~text
computecloud COMPLETED
       ≠
business / AI Goal VERIFIED
~~~

上层 Verifier 决定 Verified Outcome。

computecloud 内部仍可使用 Verifier 来保证 Artifact/Attempt 的执行正确性；这与上层业务 Verifier 是不同层次。

## 6. Context 与 Workspace

computecloud 接收 Context Reference / Artifact Reference：

~~~text
artifact://...
workspace://...
repository ref
credential ref
~~~

职责是安全 materialize、挂载、隔离、清理和 provenance，不负责 RAG、Memory selection 或 Context Compiler。

## 7. 独立性

computecloud 必须持续满足：

~~~text
CLI / CI ──────────────┐
Internal Platform ─────┼──→ computecloud
aicloud Adapter ───────┤
Other Platform ────────┘
~~~

任何上层系统都只是 API Client。

## 8. 迭代计划

### C0 — External Contract Boundary
- 明确 external execution API 与内部 Job 模型边界；
- capability/constraint schema；
- idempotency；
- version negotiation。

### C1 — Event API
- watch/stream events；
- stable status mapping；
- progress；
- Artifact event；
- reconnect/resume。

### C2 — Capability Discovery
- RuntimeCapability；
- ToolCapability；
- isolation capability；
- workspace/repository capability；
- API 查询与版本化。

### C3 — Artifact / Context Contract
- ArtifactRef；
- ContextRef；
- WorkspaceRef；
- provenance；
- checksum / retention；
- provider-neutral URI。

### C4 — Adapter Readiness
- conformance test kit；
- reference client；
- cancellation semantics；
- timeout semantics；
- error taxonomy；
- compatibility matrix。

### C5 — Execution Telemetry
- duration；
- retry / reconcile；
- runtime/tool usage；
- token/resource usage（可获取时）；
- failure classification；
- trace correlation id。

这些 telemetry 用于调用方 Evaluation，但 computecloud 不实现调用方的 Learning Loop。

## 9. 与 aicloud 联调路径

~~~text
aicloud ExecutionRequest
        ↓
Computecloud Provider Adapter
        ↓
computecloud Submit Job
        ↓
Task / Attempt / Worker / Runtime
        ↓
ExecutionEvent + Result + Artifact
        ↓
aicloud Verifier
        ↓
Verified Outcome
~~~

联调验收至少覆盖：

1. submit/idempotency；
2. event ordering/reconnect；
3. cancel；
4. timeout；
5. Worker failure + retry；
6. artifact provenance；
7. duplicate completion fencing；
8. provider completed but verifier failed；
9. trace correlation；
10. contract version compatibility。

## 10. 架构不变量

1. computecloud 不依赖 aicloud。
2. 上层系统不感知 Worker/Attempt 内部机制。
3. computecloud 只承诺 Execution Result，不承诺业务 Outcome。
4. Goal/Planner/Router/Learning 不进入 computecloud。
5. Contract 版本化，内部实现可独立演进。
6. 集成通过 Adapter/API，不共享数据库。
