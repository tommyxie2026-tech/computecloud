# RPG-2 Evidence Guard 与 Plan Fingerprint 实施记录

- 日期：2026-09-28
- 状态：Implemented on main
- 依赖：[ADR-018](../adr/0018-replan-guard-loop-prevention.md)、[RPG-1](rpg-1-replan-guard-foundation.md)
- 范围：failure_class / evidence fingerprint / canonical plan fingerprint / duplicate-plan guard

## 1. 目标

RPG-1 已解决“即使智能判断失效，系统也有硬停止边界”。

RPG-2 进一步解决：

> 为什么这次 Re-plan 值得发生？

自动 Re-plan 不再只看预算是否允许，还必须同时满足：

~~~text
new evidence
AND
new plan strategy fingerprint
~~~

## 2. Evaluator Evidence Contract

新增：

~~~text
FailureClass
Evidence[]
ReplanEvidence
~~~

当前 failure_class：

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

Evidence 至少包含：

~~~text
type
artifact_id (optional)
fact
~~~

规则：

- RE_PLAN 至少 1 条 evidence；
- evidence 必须有 type + fact；
- evidence 数量和单字段大小有上限；
- Guard 不接受空 evidence。

## 3. Evidence Fingerprint

Evidence 先 canonical sort，再进行 SHA-256：

~~~text
failure_class
+
sorted(type, artifact_id, fact)
   ↓
SHA-256
   ↓
evidence_fingerprint
~~~

因此 Evidence 的输入顺序不同不会制造新的“证据”。

Goal 级存储：

~~~text
goal_evidence
PRIMARY KEY(goal_id, evidence_fingerprint)
~~~

若同一个 Goal 已经消费过相同 Evidence：

~~~text
NO_NEW_EVIDENCE
→ NEEDS_APPROVAL
~~~

不会自动 Re-plan。

## 4. Canonical Plan Fingerprint

新增 PlanCanonical：

~~~text
strategy
dependency_signature
required_capabilities
key_assumptions
evaluation_strategy
side_effect_class
~~~

其中 required_capabilities / key_assumptions 先排序，再生成 SHA-256 fingerprint。

不进入 fingerprint：

- revision；
- timestamp；
- random id；
- 自然语言格式差异；
- 执行时生成的临时标识。

这样可以避免“换一种说法”绕过 duplicate guard。

## 5. Plan Fingerprint Registry

新增：

~~~text
plan_fingerprints
(goal_id, plan_revision) PRIMARY KEY
(goal_id, fingerprint) UNIQUE
~~~

初始 Plan 需要先通过 RegisterPlanFingerprint 注册。

Guard 收到候选 ProposedPlan 后：

~~~text
candidate fingerprint
   ↓
exists in this Goal?
   ├── yes -> DUPLICATE_PLAN -> NEEDS_APPROVAL
   └── no  -> continue
~~~

ALLOW 后，候选 fingerprint 与 next plan revision 在同一事务中持久化。

## 6. Guard 顺序

RPG-2 后：

~~~text
terminal?
current generation?
attempt budget?
replan budget?
wall-time?
deadline?
new evidence?
new plan?
   ↓
ALLOW
~~~

仍然遵守：

> Evaluator 只能提出 RE_PLAN proposal，不能直接创建新 Plan。

## 7. 幂等增强

同一：

~~~text
goal_id + evaluation_id
~~~

如果重放请求的 evidence fingerprint / proposed plan fingerprint 与第一次不同：

~~~text
IDEMPOTENCY_CONFLICT
~~~

避免同一个 evaluation_id 被用来“偷换”证据或候选 Plan。

## 8. Server schema v10

新增：

~~~text
goal_evidence
plan_fingerprints
~~~

并为 replan_requests 增加：

~~~text
failure_class
evidence_fingerprint
proposed_plan_fingerprint
~~~

## 9. 测试覆盖

新增/更新测试：

- Evidence fingerprint 与输入顺序无关；
- Plan set-like 字段排序后 fingerprint 稳定；
- 没有新 Evidence 时阻断；
- 重复候选 Plan 阻断；
- 不允许回到初始 Plan；
- evaluation replay 保持幂等；
- 同 evaluation 偷换 evidence 时返回 IDEMPOTENCY_CONFLICT；
- schema v10 对 evidence/plan fingerprint 做 Goal 级唯一约束。

## 10. 当前边界

RPG-2 仍然只检测 exact canonical duplicate，不做：

~~~text
A -> B -> A short cycle window
semantic near-duplicate
same failure repeated threshold
progress/no-progress vector
strategy_delta explanation
token/cost budget
human approval API
~~~

这些进入 RPG-3 / RPG-4。

## 11. 下一步 RPG-3

~~~text
replan_history
   ↓
recent plan fingerprint window
   ↓
A -> B -> A / A -> B -> C -> A detection

failure_class history
   ↓
same failure threshold

progress snapshot
   ↓
NO_PROGRESS guard

structured strategy_delta
~~~

原则：

> RPG-2 判断“有没有新证据、是不是同一个方案”；RPG-3 判断“虽然方案不同，但系统是不是仍然在原地打转”。
