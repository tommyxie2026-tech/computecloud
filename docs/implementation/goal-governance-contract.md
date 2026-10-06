# Goal governance contract

Status: implementation staged; RPG remains PARTIAL.

Only a server-configured human governance identity with `goals:approve` may decide.
Actor identity is derived from authentication, never supplied by a Planner,
Evaluator or HTTP payload. Decisions bind owner/project, Goal version, Plan
revision and Graph generation. An operation ID and bounded reason are mandatory.
Same-operation same-body replay returns its durable receipt even after a later
transition; a different body or actor conflicts. New decisions after terminal are
rejected. Runtime approvals keep their existing independent truth.

Supported governance records: APPROVE_NEXT_REPLAN (single-use permission, does
not publish a Plan), INCREASE_BUDGET (explicit absolute limits), PROVIDE_EVIDENCE
(bounded immutable evidence), CHANGE_CONSTRAINT (bounded named constraint
provenance; never rewrites a frozen Job), REJECT and ABORT. A decision alone must
not create execution. Re-plan publication must consume permission and commit
Guard/Plan/Job atomically. ABORT must enter the existing Job cancellation path in
the same transaction before Goal cancellation can be considered effective.

Token/cost reports bind a durable Attempt, source and unique usage ID. Values are
nullable; incomplete reports stay incomplete and block autonomous re-planning
under a finite budget. Conflicting repeated usage IDs are rejected. Accounting
ceilings cannot claim a runtime hard cap: runtimes without enforceable per-Attempt
limits cannot opt into autonomous finite token/cost execution.

This contract does not introduce policy DSL, broad RBAC, Planner implementation,
or arbitrary Workflow Engine. RPG CLOSED requires actual publication, cancellation,
usage enforcement and Control bridge integration plus negative/recovery gates.
