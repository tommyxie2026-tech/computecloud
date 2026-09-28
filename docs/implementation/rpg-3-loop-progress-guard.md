# RPG-3 Loop / Failure / Progress Guard 实施记录

- 日期：2026-09-28
- 状态：Implemented on main, CI pending
- 依赖：[ADR-018](../adr/0018-replan-guard-loop-prevention.md)、[RPG-2](rpg-2-evidence-plan-guard.md)
- 范围：strategy signature / short-cycle detection / repeated failure guard / progress guard / structured strategy_delta

## 1. 目标

RPG-2 已能阻断：

~~~text
same evidence
same exact plan
~~~

RPG-3 进一步识别“形式变化但没有真正收敛”的情况：

~~~text
Plan 文本不同
但策略族相同

Evidence 不同
但 failure_class 一直相同

Plan 一直变化
但 acceptance / blockers 没有改善
~~~

## 2. Strategy Signature

PlanCanonical 新增：

~~~text
strategy_class
~~~

完整 Plan fingerprint 仍用于 exact duplicate；strategy signature 用于更粗粒度的循环识别。

当前 strategy signature：

~~~text
strategy_class
+ dependency_signature
+ side_effect_class
    ↓
SHA-256
~~~

因此如果两个 Plan 属于同一 strategy_class、相同 dependency topology 与 side-effect class，则会落入同一个 strategy signature。

## 3. Short-cycle Guard

默认窗口：

~~~text
cycle_window = 6 plan revisions
~~~

候选 Plan 的 strategy signature 若在最近窗口出现过：

~~~text
LOOP_DETECTED
→ NEEDS_APPROVAL
~~~

DUPLICATE_PLAN 用于完全相同 Plan；LOOP_DETECTED 用于不同完整 fingerprint、但同策略族的回环。

## 4. Structured Strategy Delta

ReplanRequest 新增：

~~~text
StrategyDelta
├── changed_dimensions[]
└── summary
~~~

自动 Re-plan 必须明确说明哪些决策维度发生变化，以及为什么当前候选 Plan 与上一轮不同。

当前 RPG-3 只验证其结构存在且有界；更复杂的 policy-based delta validation 可在后续增强。

## 5. Repeated Failure Guard

replan_history 持久化每轮：

~~~text
failure_class
evidence_fingerprint
plan_fingerprint
strategy_signature
progress
~~~

默认策略：

~~~text
same_failure_max_replans = 2
~~~

即连续第三次相同 failure_class 时：

~~~text
REPEATED_FAILURE
→ NEEDS_APPROVAL
~~~

防止 Planner 通过持续换 Plan 来掩盖同一个根因一直没有解决。

## 6. Progress Snapshot

Evaluator / controller 为每次 Re-plan proposal 提供：

~~~text
accepted_checks
failed_checks
unknown_checks
resolved_assumptions
unresolved_blockers
~~~

只要满足任一项，即视为有进展：

~~~text
accepted_checks ↑
failed_checks ↓
unknown_checks ↓
resolved_assumptions ↑
unresolved_blockers ↓
~~~

## 7. No-progress Guard

系统允许一轮停滞，避免过于敏感：

~~~text
round N
no improvement
→ still allow

round N+1
again no improvement
→ NO_PROGRESS
→ NEEDS_APPROVAL
~~~

如果中间出现一次真实 progress，停滞状态会被打断，不会误判为连续无进展。

## 8. Re-plan History

Server schema v11 新增：

~~~text
replan_history
~~~

字段包括：

~~~text
goal_id
ordinal
evaluation_id
plan_revision
graph_generation
failure_class
evidence_fingerprint
plan_fingerprint
strategy_signature
progress_json
progressed
created
~~~

它不是普通日志，而是下一轮 Re-plan Guard 的 durable decision input。

## 9. Guard 顺序

RPG-3 后当前顺序：

~~~text
terminal goal?
current generation?
attempt budget?
replan budget?
wall-time?
deadline?
new evidence?
exact new plan?
strategy not in recent cycle?
failure class not repeatedly unresolved?
progress not stalled for second consecutive round?
    ↓
ALLOW
~~~

## 10. 测试覆盖

新增：

- same strategy class / different plan text → LOOP_DETECTED；
- 连续第三次相同 failure_class → REPEATED_FAILURE；
- 第一轮无进展仍允许；
- 连续第二轮无进展 → NO_PROGRESS；
- 中间出现 progress 后停滞状态重置；
- schema v11 replan_history uniqueness / progress fields。

## 11. 当前边界

RPG-3 仍然不引入：

~~~text
embedding similarity
LLM-based semantic loop judge
token/cost budget
human approval API
project-level policy template
~~~

前两项当前仍刻意避免，以保持 Guard deterministic。

## 12. 下一步 RPG-4

~~~text
Human Approval API
Token / Cost Accounting
Project Re-plan Policy
Budget Increase Authorization
Goal / Constraint Change Authorization
Approval Audit
~~~

原则：

> RPG-1 保证“有限”；RPG-2 保证“有新证据”；RPG-3 保证“正在收敛”；RPG-4 负责“越过自治边界后如何安全交给人”。
