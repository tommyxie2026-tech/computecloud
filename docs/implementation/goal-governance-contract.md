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

The staged `GuardAndPublishReplan` transaction contract lets a caller run
Guard, one-use approval consumption, and Plan/Job publication atomically. A
failed or empty publication rolls all three back; stale or repeated approval
consumption fails closed. An allowed decision replay returns the existing decision only
when an immutable Plan, bound Job and matching frozen spec prove publication;
otherwise it fails closed. The Server now has an internal explicit-proposal
publication path: it requires a durable failed evaluation, a Plan fingerprint
bound to the concrete Job spec, authorized credentials and templates, and
commits the new Job, Stage/Task, Plan and binding together. It is not exposed as
an automatic or public proposal API. Automatic Re-plan remains disabled and RPG
remains PARTIAL.

This contract does not introduce policy DSL, broad RBAC, Planner implementation,
or arbitrary Workflow Engine. RPG CLOSED requires actual publication, cancellation,
usage enforcement and Control bridge integration plus negative/recovery gates.

## Initial implementation boundary

Schema v16 persists decisions, one-use permissions and nullable Attempt usage.
`POST /v1/jobs/{job}/goal/decisions` uses the existing Control write lease and
requires an explicitly configured `goal_actor_kind: human`, stable
`goal_actor_id`, `goals:approve` and `jobs:read`; budget and constraint decisions
also require `goals:budget` and `goals:constraints` respectively. Goal GET includes
a bounded recent-decision projection. ABORT/REJECT atomically put the Job into
its established STOPPING/cleanup path; they do not manufacture cleanup proof.

No current Runtime adapter advertises enforceable token/cost hard limits. Setting
a finite usage budget therefore fences future assignments with
`GOAL_RUNTIME_BUDGET_UNSUPPORTED`; existing active Attempts still follow their
already-frozen execution contracts. This is an opt-in accounting/governance
foundation, not a runtime spend-control release. Unreported terminal Attempt usage
is recorded as incomplete with null amounts. The Guard blocks unknown/exhausted
accounting. The one-use permission transaction and internal Server publication
path are implemented, but a governed proposal API and enforceable Runtime caps
remain pending.
Evidence/constraint decisions are provenance only; frozen Jobs remain
immutable. These limits keep RPG PARTIAL.
