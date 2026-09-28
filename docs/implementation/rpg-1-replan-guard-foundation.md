# RPG-1 Re-plan Guard Foundation 实施记录

- 日期：2026-09-28
- 状态：Implemented on main
- 依赖：[ADR-017](../adr/0017-goal-oriented-computing-model.md)、[ADR-018](../adr/0018-replan-guard-loop-prevention.md)
- 范围：Re-plan Budget / Goal Attempt Budget / Wall-time & Deadline / Current-generation Guard / Idempotent Evaluation

## 1. 目标

RPG-1 先解决一个问题：

> 在 Evidence / Loop / Progress 智能判断尚未实现前，系统也绝不能无限 Re-plan 或无限消耗 Attempt。

因此本阶段只实现确定性、可恢复、可审计的硬边界，不引入 embedding、vector DB 或 LLM 相似度判断。

## 2. 已实现

### 2.1 Server schema v9

新增：

~~~text
goals
replan_requests
~~~

Goal 持久化：

~~~text
active_plan_revision
active_graph_generation
max_replans
max_total_attempts
max_wall_time_ms
deadline
consumed_replans
consumed_attempts
version
~~~

Re-plan request 持久化：

~~~text
goal_id
evaluation_id
expected_plan_revision
expected_graph_generation
reason_code
state
decision_code
next_plan_revision
next_graph_generation
~~~

(goal_id, evaluation_id) 唯一，保证同一 Evaluation 重放不会重复消耗 Re-plan Budget。

### 2.2 Re-plan Guard

新增 internal/goal/replan_guard.go。

当前判定顺序：

~~~text
terminal goal?
   ↓
current plan revision / graph generation?
   ↓
attempt budget available?
   ↓
replan budget available?
   ↓
wall-time available?
   ↓
deadline available?
   ↓
ALLOW
~~~

ALLOW 时通过同一个 SQLite transaction：

1. 写入 replan_requests；
2. consumed_replans + 1；
3. active_plan_revision + 1；
4. active_graph_generation + 1；
5. Goal 进入 REPLANNING。

因此 Guard 决策与 generation reservation 不会分裂提交。

### 2.3 Current-generation fencing

RE_PLAN proposal 必须携带：

~~~text
expected_plan_revision
expected_graph_generation
~~~

若与 Goal active generation 不一致：

~~~text
REJECTED
decision_code = STALE_GENERATION
~~~

旧 Graph generation 的 late evaluator result 不能触发新的 Plan。

### 2.4 Goal-level Attempt Budget

新增 ReserveAttempt：

~~~text
consumed_attempts < max_total_attempts
~~~

该预算位于 Task retry policy 之上。

未来 Graph Node 接入后：

~~~text
Task retry limit
AND
Goal total attempt budget
~~~

必须同时允许才能创建 Attempt。

当前 RPG-1 先提供 durable primitive；Goal/Graph API 接入阶段再把它接到实际 Attempt creation path。

### 2.5 Wall-time 与 Deadline

Wall-time 从 Goal created 开始累计，不随 Re-plan 重置。

~~~text
now - goal.created >= max_wall_time
    -> NEEDS_APPROVAL

now >= deadline
    -> NEEDS_APPROVAL
~~~

### 2.6 Idempotency

同一：

~~~text
goal_id + evaluation_id
~~~

重复提交 Guard 时返回已保存 Decision，不再次：

- 增加 consumed_replans；
- 增加 revision；
- 增加 graph generation。

## 3. 决策码

当前 RPG-1：

~~~text
ALLOWED
BUDGET_EXHAUSTED
ATTEMPT_BUDGET_EXHAUSTED
WALL_TIME_EXCEEDED
DEADLINE_EXCEEDED
STALE_GENERATION
GOAL_TERMINAL
~~~

预算类阻断默认进入 NEEDS_APPROVAL；stale generation 和 terminal goal 直接 REJECT。

## 4. 测试覆盖

新增测试覆盖：

- allow + revision/generation advance；
- evaluation replay idempotency；
- stale graph generation rejection；
- max_replans stop；
- max_total_attempts stop；
- deadline stop；
- schema v9 + replan request uniqueness。

## 5. 当前边界

RPG-1 尚未实现：

~~~text
new evidence fingerprint
failure_class
plan fingerprint
short-cycle detection
progress guard
strategy_delta
token/cost accounting
human approval API
~~~

这些分别进入 RPG-2 / RPG-3 / RPG-4。

## 6. 下一步：RPG-2

下一阶段优先：

~~~text
Evaluator Contract
   ├── failure_class
   ├── evidence[]
   └── evidence_fingerprint

Plan
   └── canonical fingerprint

Re-plan Guard
   ├── require_new_evidence
   └── reject_exact_duplicate_plan
~~~

保持原则：

> RPG-2 增加“为什么允许重新规划”的证据约束；RPG-1 已经保证即使智能判断失效，系统仍然有确定性的硬停止边界。
