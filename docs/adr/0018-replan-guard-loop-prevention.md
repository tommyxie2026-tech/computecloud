# ADR-018：Re-plan Guard 与自治循环防护

- 日期：2026-09-28
- 状态：Accepted
- 依赖：[ADR-017：Goal-oriented Computing Model](0017-goal-oriented-computing-model.md)
- 影响范围：Evaluator、Re-plan、Goal Budget、Plan Revision、Execution Graph Generation、Approval、审计与可观测性

## Context

ADR-017 将 computecloud 的最高层计算模型定义为：

~~~text
Goal -> Plan -> Execution Graph -> Scheduler -> Worker -> Artifact -> Evaluator -> Re-plan
~~~

该模型允许 Evaluator 在发现当前策略无法满足 Goal 时触发 Re-plan。与普通 Retry 不同，Re-plan 会产生新的 Plan revision 与 Execution Graph generation，因此具备更强的自适应能力，也带来新的系统风险：

- Planner 与 Evaluator 可能形成无限自治循环；
- 不同 Plan revision 可能只是语义改写，没有实质变化；
- 同一种失败可能在多个 Plan 中重复出现；
- Token、成本、时间和 Worker 资源可能被无上限消耗；
- LLM Evaluator 可能因为非确定性反复给出 RE_PLAN；
- 旧 Graph generation 的迟到结果可能误触发新的 Re-plan；
- 自动系统可能在没有新证据时不断尝试“换一种说法再做一次”。

因此 Re-plan 必须被建模为**受控、高成本、可审计的控制动作**，而不是普通循环边。

## Decision

computecloud 引入独立的 **Re-plan Guard**。

任何 RE_PLAN verdict 在进入 Planner 前都必须经过 Re-plan Guard。只有全部强制约束通过后，系统才能创建新的 Plan revision 与 Execution Graph generation。

核心判定：

~~~text
Re-plan Allowed
=
BudgetAvailable
AND NewEvidencePresent
AND MaterialPlanChangeExpected
AND NoLoopDetected
AND FailurePolicyAllows
AND GenerationIsCurrent
AND ApprovalPolicyAllows
~~~

任一条件不满足，Re-plan 必须被拒绝，并进入 NEEDS_APPROVAL、FAILED 或策略指定的终止状态。

## 1. Re-plan Guard 所处位置

~~~text
Artifact
   ↓
Evaluator
   ↓
RE_PLAN
   ↓
Re-plan Guard
   ├── Budget Guard
   ├── Evidence Guard
   ├── Duplicate / Similarity Guard
   ├── Failure-class Guard
   ├── Generation Guard
   ├── Progress Guard
   └── Approval / Policy Guard
          ↓
     allowed ?
      /    \
    yes     no
    ↓        ↓
Planner   NEEDS_APPROVAL / FAILED
    ↓
new Plan revision
    ↓
new Execution Graph generation
~~~

Evaluator 负责提出 RE_PLAN，但没有权力绕过 Guard 直接创建 Plan revision。

## 2. 硬预算：任何 Re-plan 都必须有边界

Goal 必须配置或继承 Re-plan Budget。

最小预算字段：

~~~yaml
replan_budget:
  max_replans: 4
  max_total_attempts: 12
  max_wall_time_seconds: 7200
  max_tokens: 200000
  max_cost_units: 100
  deadline: optional
~~~

预算遵循以下原则：

1. max_replans 是硬上限，不允许 Planner/Evaluator 自行提高；
2. max_total_attempts 跨 Plan revision 累计；
3. wall time 从 Goal 开始计算，不因 Re-plan 重置；
4. token/cost 预算跨 Runtime、Worker、Plan 累计；
5. deadline 到达后不得自动创建新 Plan；
6. Budget 更新只能来自授权调用方或治理层，不能由 Planner 自我扩容。

预算耗尽时默认：

~~~text
Budget Exhausted
   ↓
NEEDS_APPROVAL
~~~

对于明确的 unattended batch policy，可以配置为 FAILED。

## 3. Evidence Guard：没有新证据，不允许 Re-plan

Evaluator 输出 RE_PLAN 时必须给出结构化 evidence：

~~~yaml
verdict: RE_PLAN
reason_code: ASSUMPTION_INVALIDATED
evidence:
  - artifact_id: test-report-17
    type: test_failure
    fact: oauth callback path conflicts with current jwt middleware
~~~

允许触发 Re-plan 的 evidence 类型包括但不限于：

- deterministic test failure；
- verifier failure；
- missing capability；
- permission / policy conflict；
- dependency / environment mismatch；
- invalidated assumption；
- repository / schema / API discovery；
- human feedback；
- externally changed state；
- accepted Artifact 中的新事实。

以下情况不能单独作为新 evidence：

- “结果不够好”；
- “再尝试一种方案”；
- LLM 的无来源主观判断；
- 与上一次 Evaluation 完全相同的 failure evidence；
- 没有 Artifact/provenance 的自然语言推测。

系统必须维护 evidence_fingerprint。若当前 Re-plan 请求没有新增 evidence fingerprint，则默认拒绝自动 Re-plan。

## 4. Plan Fingerprint 与循环检测

每个 Plan revision 必须计算 canonical fingerprint。

推荐 fingerprint 输入：

~~~text
goal_id
normalized strategy
task/dependency topology
required capabilities
key assumptions
evaluation strategy
side-effect class
~~~

不把自然语言措辞、时间戳、随机 ID 等无意义差异加入 fingerprint。

系统至少检测三类循环：

### 4.1 Exact Duplicate

~~~text
Plan v1 fingerprint == Plan v3 fingerprint
~~~

直接判定 PLAN_LOOP_DETECTED。

### 4.2 Semantic Near-Duplicate

允许 Planner Provider 同时生成 strategy_signature 或 canonical structured plan，用于检测“换措辞但策略不变”。

若：

~~~text
same failure class
+
same dependency topology
+
same strategy signature
~~~

则视为近重复 Plan。

### 4.3 Alternating Cycle

系统保留最近 N 个 plan fingerprints，例如：

~~~text
A -> B -> A
A -> B -> C -> A
~~~

检测到短周期回环时，停止自动 Re-plan。

## 5. Failure-class Guard：同一种失败不能无限跨 Plan 重复

每次 Evaluation 必须产生 failure_class。

示例：

~~~text
TEST_FAILURE
MISSING_CAPABILITY
PERMISSION_DENIED
ARCHITECTURE_CONFLICT
DEPENDENCY_UNAVAILABLE
POLICY_REJECTED
TIMEOUT
RESOURCE_EXHAUSTED
INVALID_ASSUMPTION
UNKNOWN
~~~

策略示例：

~~~yaml
failure_policy:
  same_failure_max_replans: 2
  unknown_failure_max_replans: 1
  policy_rejected_auto_replan: false
  permission_denied_auto_replan: false
~~~

如果同一 failure_class 连续出现，下一次 Re-plan 必须满足“策略实质变化”条件，例如：

- Runtime 改变；
- Tool/Environment 改变；
- dependency graph 改变；
- algorithm/approach 改变；
- capability requirement 改变；
- human constraint/feedback 改变。

否则拒绝。

## 6. Progress Guard：Re-plan 必须降低不确定性

每个 Re-plan proposal 必须结构化回答：

~~~yaml
replan_proposal:
  what_changed: ...
  invalidated_assumptions:
    - ...
  new_evidence:
    - ...
  strategy_delta:
    - ...
  expected_improvement: ...
  unresolved_risks:
    - ...
~~~

Guard 重点验证：

- 是否有新的事实；
- 是否淘汰了至少一个错误假设；
- 是否改变了导致失败的关键策略；
- 是否只是扩大搜索空间而没有收敛；
- 是否反复增加 Task，却没有提高 acceptance criteria 的满足度。

系统可以维护简单的 progress_vector：

~~~text
accepted_checks
failed_checks
unknown_checks
resolved_assumptions
unresolved_blockers
artifact_quality
~~~

如果连续多轮没有任何正向变化，则触发 NO_PROGRESS 并停止自动 Re-plan。

初期实现不要求复杂机器学习评分，先采用确定性计数与状态比较。

## 7. Generation Guard：只允许当前世代触发 Re-plan

RE_PLAN verdict 必须绑定：

~~~text
goal_id
plan_id
plan_revision
graph_id
graph_generation
evaluation_id
evidence_set
~~~

仅当前 active Plan revision + active Graph generation 的 Evaluation 可以申请 Re-plan。

以下情况必须拒绝：

- old graph generation late completion；
- superseded Plan 的 Evaluator verdict；
- orphan Attempt 结果；
- stale Artifact；
- 重复 delivery 的 Evaluation event。

这与 Attempt generation fencing 一致，但作用在 Goal-oriented Computing 层。

## 8. Re-plan 与 Retry 的升级顺序

系统默认遵循：

~~~text
transient execution failure
   ↓
Retry / Re-route
   ↓
still failing + evidence shows strategy problem
   ↓
Re-plan
~~~

不应把以下问题优先升级成 Re-plan：

- Worker 暂时不可用；
- 网络抖动；
- Runtime crash；
- lease timeout；
- 单次 transient API error。

这些优先由 Retry / Re-route 处理。

只有当 evidence 表明“当前 Plan 的逻辑或假设本身不成立”时才进入 Re-plan。

## 9. 人工升级

以下情况默认进入 NEEDS_APPROVAL：

- max_replans 即将或已经耗尽；
- loop detected；
- no progress；
- repeated same failure；
- Goal acceptance criteria 存在歧义；
- Re-plan 需要扩大权限；
- Re-plan 需要新的 credential；
- Re-plan 需要突破成本/时间预算；
- 涉及不可逆 side effect；
- Planner 请求修改 Goal 本身。

Human Approval 可以：

~~~text
APPROVE_NEXT_REPLAN
INCREASE_BUDGET
CHANGE_CONSTRAINT
PROVIDE_EVIDENCE
CHANGE_GOAL
ABORT
~~~

所有操作必须审计。

## 10. Re-plan Policy

建议公共策略：

~~~yaml
replan_policy:
  max_replans: 4
  require_new_evidence: true
  reject_exact_duplicate_plan: true
  detect_short_cycles: true
  cycle_window: 6

  same_failure_max_replans: 2
  no_progress_max_replans: 1

  require_strategy_delta: true
  require_current_generation: true

  on_budget_exhausted: NEEDS_APPROVAL
  on_loop_detected: NEEDS_APPROVAL
  on_no_progress: NEEDS_APPROVAL
  on_repeated_same_failure: NEEDS_APPROVAL
  on_stale_generation: REJECT
~~~

该策略可以由 Project/Goal 收紧，但 Planner/Evaluator 无权放宽。

## 11. 状态机

~~~text
EVALUATING
    ↓
 evaluator verdict = RE_PLAN
    ↓
REPLAN_GUARDING
    ├── allowed
    │      ↓
    │   REPLANNING
    │      ↓
    │   PLAN_REVISION_CREATED
    │      ↓
    │   GRAPH_GENERATION_CREATED
    │      ↓
    │   GRAPH_READY
    │
    ├── approval required
    │      ↓
    │   NEEDS_APPROVAL
    │
    └── terminal reject
           ↓
        FAILED
~~~

新增状态不能覆盖已有历史 Plan/Graph，而要以 revision/generation 追加。

## 12. 数据模型

建议增加：

~~~text
goal_budget
- goal_id
- max_replans
- max_attempts
- max_wall_time
- max_tokens
- max_cost
- deadline
- consumed_*

replan_request
- replan_request_id
- goal_id
- evaluation_id
- from_plan_revision
- from_graph_generation
- reason_code
- failure_class
- evidence_fingerprint
- strategy_delta
- status
- guard_decision
- guard_reason

plan_fingerprint
- plan_id
- revision
- canonical_hash
- strategy_signature
- dependency_signature

replan_history
- goal_id
- ordinal
- plan_revision
- graph_generation
- failure_class
- evidence_fingerprint
- progress_snapshot
- created_at
~~~

## 13. 可观测性

至少增加指标：

~~~text
goal_replan_total
goal_replan_allowed_total
goal_replan_rejected_total
goal_replan_budget_exhausted_total
goal_replan_loop_detected_total
goal_replan_no_progress_total
goal_replan_same_failure_total
goal_needs_approval_total
goal_replan_depth
goal_total_attempts
goal_total_cost
goal_wall_time
~~~

Trace 增加：

~~~text
goal_id
plan_revision
graph_generation
evaluation_id
replan_request_id
failure_class
evidence_fingerprint
plan_fingerprint
guard_decision
~~~

## 14. Reliability Invariants

以下不变量必须长期保持：

1. Evaluator 无权直接创建新 Plan；
2. Planner 无权修改 Re-plan Budget；
3. 每次 Re-plan 必须有新的 revision 与 generation；
4. stale generation 不能触发 Re-plan；
5. 无新 evidence 默认不能自动 Re-plan；
6. duplicate/short-cycle Plan 不能自动执行；
7. Re-plan 次数、时间、attempt、token、cost 至少一种硬预算必须存在，生产模式建议全部存在；
8. Re-plan 不能静默修改 Goal；
9. 提升权限、扩大预算、改变 Goal 必须走授权路径；
10. Guard 决策必须 durable、幂等、可审计；
11. Guard restart 后不能重复消费同一个 RE_PLAN verdict 创建两个 Plan revision；
12. 自动 Re-plan 永远是 bounded autonomy，而不是无限自治。

## 15. 实施顺序

建议分四步：

~~~text
RPG-1
Budget + max_replans + current-generation guard

RPG-2
evidence fingerprint + failure_class + duplicate fingerprint

RPG-3
short-cycle detection + progress guard + structured replan proposal

RPG-4
approval escalation + cost/token accounting + policy templates
~~~

第一版不依赖 embedding/vector DB。优先使用结构化 Plan、canonical fingerprint、failure class 和确定性状态比较，保持系统轻量。

## Consequences

正面影响：

- 从架构层面消除无限 Re-plan；
- 将 Agent 自治限制在明确预算和证据范围内；
- Re-plan 与 Retry、Re-route 的边界更清楚；
- 失败可以稳定升级到人工，而不是持续烧成本；
- 能解释“为什么又规划了一次”以及“为什么系统停止继续规划”；
- 保持单 Server + SQLite 仍可实现。

代价：

- Goal 需要累计预算状态；
- Plan 需要 canonical representation/fingerprint；
- Evaluator contract 需要 failure_class/evidence；
- Planner contract 需要 strategy delta；
- 状态机和测试矩阵增加。

## Canonical Rule

从本 ADR 起，自动 Re-plan 的统一规则为：

> **No new evidence, no re-plan. No material strategy change, no re-plan. No remaining budget, no re-plan. Detected loop, no re-plan.**

Re-plan 是 bounded control transition，而不是无限循环能力。
