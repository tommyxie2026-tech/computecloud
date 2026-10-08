# Goal governance contract

Status: implementation and CI contract complete for the bounded code slice;
real-provider and independent-host acceptance remain PARTIAL.

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
ceilings only claim a runtime boundary when the bound execution advertises and
implements it. The initial supported case is a cost-only Goal whose active Job
uses `claude_http` throughout. All token limits, mixed limits and Codex cost
limits remain fail-closed.

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

PR #125 CI 37803798658 validates capability fencing, exact micro-USD
conversion, single in-flight admission, atomic settlement, terminal budget
exhaustion and fail-closed incomplete usage. Its OCI cases use real Docker with
a credential-free fake Claude executable; `real_model_calls` remains false.

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

`claude_http` version 2.1.217 or later may advertise
`runtime:budget_claude_estimated_usd_v1`. For a cost-only policy, the Server
subtracts complete settled usage, freezes the remaining micro-USD amount and
`usd_micros_client_estimate` semantics into the Assignment, and permits only
one in-flight Attempt for that Goal. The Worker passes the amount to Claude's
`--max-budget-usd`; the final estimate can exceed the threshold by one API call.
This is an enforceable stop boundary over Claude's client-side estimate, not a
strict actual-billing cap. Completion records usage in the same transaction
before releasing the Attempt. Missing final/token/cost evidence is recorded as
incomplete and blocks future admission with `GOAL_USAGE_UNKNOWN`; exhausted
usage blocks with `GOAL_USAGE_EXHAUSTED`.

Codex cost/token limits and Claude token limits remain unsupported and receive
`GOAL_RUNTIME_BUDGET_UNSUPPORTED`. Existing active Attempts keep their frozen
contracts. Goal GET retains `runtime_hard_budget_supported` and adds
`runtime_budget_capabilities`; both are derived from durable policy and active
bound Job profiles rather than current Worker presence. Real-provider and
independent-host acceptance remain pending.
Evidence/constraint decisions are provenance only; frozen Jobs remain
immutable. These limits keep RPG PARTIAL.
