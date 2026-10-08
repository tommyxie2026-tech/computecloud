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
otherwise it fails closed. The Server now has an explicit governed proposal
publication path: it requires a durable failed evaluation and accepts only the
evaluator's persisted `job_evaluation` failure fact as Re-plan evidence. Richer
Artifact claims remain unavailable until their facts can be verified. The path
also requires a Plan fingerprint bound to the concrete Job spec, authorized
credentials and templates, and commits the new Job, Stage/Task, Plan and binding
together. The HTTP caller needs `goals:propose`, `jobs:submit`, `jobs:read`,
`jobs:control`, and the current Job write lease. This scope grants no human
approval authority; a required one-use approval is still consumed in the same
transaction. Automatic Re-plan remains disabled and RPG remains PARTIAL.

`POST /v1/jobs/{job}/goal/replans` accepts bounded JSON with `proposal`,
`job_spec`, and optional `approval_operation_id`. Proposal fields use snake_case:
`id`, `goal_id`, `evaluation_id`, `expected_plan_revision`,
`expected_graph_generation`, `reason_code`, `evidence`, `proposed_plan`,
`strategy_delta`, and `progress`. The proposed Plan's `job_spec_hash` must
match the canonical hash of `job_spec`; failure evidence must match the
evaluator's persisted `job_evaluation` fact. First publication returns 202
with `job` and `decision`; identical replay returns 200 with the same Job.
A Guard decision that cannot publish returns 409 with `decision`. Malformed
JSON, stale evaluation, tampered evidence, and unauthorized credentials fail
closed; a conflicting replay returns 409. A human approval can release an
otherwise bounded strategy-loop,
repeated-failure or no-progress guard; it cannot override attempt, Re-plan,
wall-time or usage limits. Duplicate evidence and duplicate Plan fingerprints
are rejected for publication because their immutable indexes cannot record a
second copy. This endpoint does not choose a Plan or create a Workflow Engine.

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
accounting. The one-use permission transaction and governed proposal API are
implemented; enforceable Runtime caps and real end-to-end acceptance remain
pending.
Evidence/constraint decisions are provenance only; frozen Jobs remain
immutable. These limits keep RPG PARTIAL.
