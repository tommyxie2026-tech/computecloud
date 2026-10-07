# Goal execution and governance contract (GI-01)

Status: stages 1–3 implemented on the Goal integration branch, 2026-10-06. Builds on ADR-017/018; Agent Job Executor scope.

## Compatibility and compilation

A legacy Job maps to a synthetic Goal, immutable Plan revision and bounded Graph.
The Graph uses the existing single or map/barrier/reduce Stage controller. It does
not introduce arbitrary workflows, a second scheduler, or a Planner implementation.
Existing Job APIs, idempotency, frozen execution templates and authorization remain
in force. Goal metadata and Job creation commit together. Historical Jobs are
adopted using their durable state and actual Attempt history, without replay.

Plan revisions store their frozen Job provenance and deterministic graph shape.
One revision binds one Job; all Graph nodes map to that Job's existing Stages/Tasks.
Future external Planner integration proposes new revisions through an explicit
contract; it may not alter an existing Job, deadline, credential or budget.

## Atomic execution budget

Goal Attempt reservation and the existing Attempt insert share the same SQLite
transaction. A unique Attempt-to-Goal reservation prevents double consumption.
The current Plan revision / Graph generation and Goal state are checked before
assignment. Rollback of either operation rolls back the other. Existing release,
cleanup proof, retry and late-result fencing remain authoritative.

## Evaluation

A deterministic evaluator reads the bound terminal Job and accepted Artifact
references. Its versioned verdict and provenance commit with Goal transition.
Unknown, orphaned, stale-generation or absent successful-result evidence cannot
produce a success verdict. Evaluation replay returns the same durable verdict.

## Governance boundary

Runtime Approval remains separate from Goal Governance. Goal decisions require
an authorized human principal, expected Goal version/revision/generation, explicit
operation ID and reason. Duplicate same-request decisions return their receipt;
conflicting, stale, terminal and unauthorized decisions are rejected.
Token/cost accounting records source, completeness and idempotent usage identity;
unknown usage is never converted to zero. Budget/constraint increases require
explicit authorization. Planner/Evaluator do not carry approval authority.

Re-plan Guard approval, next immutable Plan/Graph and bound Job publication must
commit atomically or use a durable recoverable proposal protocol. The staged
internal Server publication path uses one transaction and rejects unsupported
finite Runtime token/cost budgets. Merely calling GuardReplan before SubmitJob
is insufficient and must not be advertised as closed.
RPG CLOSED requires GI-01–04 plus governance/negative/recovery end-to-end gates.

## Staged delivery

1. Contract and migration review.
2. Synthetic compatibility and atomic Attempt reservation.
3. Artifact-grounded evaluation and recovery.
4. Durable approval/usage and Control projection.
5. External proposal/Guard publication protocol and end-to-end closure.

Each stage has its own evidence. Incomplete stages leave RPG/Goal integration
PARTIAL and do not enable automatic autonomy.

## Stage 1–3 validation and limits

Server schema v15 adds immutable Plan/evaluation records, Job bindings and unique
Attempt reservations. Old Jobs are adopted in bounded batches without execution;
the authorized Job Goal projection also adopts on demand. Single and existing
map/reduce controller graphs execute through the same scheduler and release gates.
Explicit bounded manual retry reopens the same Plan without resetting counters;
explicit authorized deadline extension updates the synthetic Goal deadline.
Evaluations are keyed by terminal Job version so a legitimate manual retry can
produce a new result without overwriting the earlier verdict.

`make ci-goal-execution` covers real fixture Worker single/map-reduce completion,
idempotent submission/evaluation, transaction rollback, adoption across restart,
stale graph fences, missing artifact proof, manual retry and deadline regression.
The complete server/store/goal test suites pass locally. This is fixture evidence,
not real Runtime/MCP or production acceptance. Runtime token/cost enforcement
and a governed external re-plan proposal API remain unimplemented;
`automatic_replan_enabled` remains false and RPG integration remains PARTIAL.
