# Runtime token/cost budget enforcement design

Status: design and written specification approved; implementation plan pending
review.

## Purpose

Add enforceable Runtime budget boundaries to Goal execution without changing the
product into an LLM Gateway, Model Serving Platform, Workflow Engine, AI
Execution OS, or GPU Cloud. The platform remains an Agent-aware Distributed Job
Execution Platform / Agent Job Executor using self-hosted HTTP Runtime services
and Docker/OCI execution.

The first delivery supports the one budget dimension that the selected Runtime
can enforce natively: estimated API cost for Claude Code print mode. Finite
dimensions without a matching native Runtime capability remain fail-closed.

## Agreed semantics

- One `cost_unit` is one micro-USD of the Runtime client's estimated API spend.
- Claude cost enforcement means invoking Claude Code print mode with its native
  `--max-budget-usd` boundary. The platform guarantees exact propagation of the
  configured remaining estimate and honors Claude's budget-stop result.
- The estimate can differ from the provider invoice. This feature does not claim
  an actual-billing ceiling.
- Claude evaluates the boundary after an API call completes, so its final
  estimated cost can exceed the supplied boundary by at most one API call. The
  feature is an enforceable stop boundary, not a mathematically strict
  non-exceeding spend cap.
- Codex total token and cost ceilings, and Claude total token ceilings, are not
  supported in this delivery. A Goal requesting any unsupported finite dimension
  is rejected before assignment with `GOAL_RUNTIME_BUDGET_UNSUPPORTED`.
- Observing usage and then cancelling a process is not a hard limit and is not
  used to advertise support.
- A finite-budget Goal can have only one in-flight Attempt. This prevents two
  Attempts from receiving the same remaining cumulative allowance.

Claude documents `--max-budget-usd` as a print-mode maximum checked against its
client-side cost estimate and notes that the estimate can differ from the bill:
<https://code.claude.com/docs/en/cli-reference>.
Its official Agent SDK example also states that checking occurs after each API
call and may exceed the boundary by one call:
<https://github.com/anthropics/claude-agent-sdk-python/blob/main/examples/max_budget_usd.py>.

## Capability model

Budget capabilities are Runtime- and dimension-specific. The initial capability
is:

```text
runtime:budget_claude_estimated_usd_v1
```

A generic `runtime:budget` capability is deliberately insufficient. The Server
must match the Task's resolved Runtime provider and every finite policy dimension
against the Worker capabilities. A Worker that can enforce Claude estimated cost
must not thereby qualify a Codex task or a token-limited task.

The HTTP Runtime advertises the capability only when its configured Claude
executable supports the required print-mode behavior. The pinned project Runtime
version satisfies the minimum behavior, while startup/version validation prevents
an older or unknown executable from claiming it. Workers and Runtime controllers
that omit the capability continue to execute unbudgeted work but cannot receive a
finite cost-budget Assignment.

Future capabilities may add native token or cost semantics under different names.
They must not reuse this capability unless their unit, boundary, and result
semantics are identical.

## Assignment contract

`agent.v1.Assignment` gains an optional `runtime_budget` message. Its optional
integer fields represent the remaining cumulative allowance at assignment time:

```text
RuntimeBudget
  remaining_token_units: optional int64
  remaining_cost_units: optional int64
  cost_semantics: enum/string identifying usd_micros_client_estimate
```

The exact generated representation should follow the repository's protobuf
compatibility conventions. Absence means unlimited for that dimension. Zero or a
negative remaining value is never dispatched. Unknown semantics fail closed.

The budget is a frozen Attempt input. A subsequent Goal budget increase affects
only future Attempts. Existing governance supports explicit increases, not silent
reductions of an already-frozen execution contract.

The budget is not accepted from an untrusted Worker or result payload. It is
derived by the Server from the durable Goal policy and usage ledger.

## Server scheduling and transaction boundary

Before assigning a Task whose Goal has a finite token or cost policy, the Server
performs the following in one scheduling transaction:

1. Read the current Goal budget policy and all durable usage records.
2. Reject any missing or incomplete terminal Attempt usage required by the
   policy. Unknown consumption is never treated as zero.
3. Sum completed usage with overflow-safe integer arithmetic.
4. Return `GOAL_USAGE_EXHAUSTED` when consumed usage is greater than or equal to
   the configured maximum.
5. Resolve the Task's Runtime provider and require a matching capability for
   every finite dimension. Otherwise return
   `GOAL_RUNTIME_BUDGET_UNSUPPORTED`.
6. Confirm that the Goal has no other in-flight Attempt.
7. Freeze `maximum - consumed` into the Assignment and claim the Goal's single
   in-flight position atomically with the normal Attempt assignment transition.

The single-in-flight rule is scoped to finite token/cost Goals. Unbudgeted Goals
retain existing concurrency. This delivery does not introduce a reservation
allocator or divide a Goal budget among concurrent Attempts.

The Server releases the single-in-flight position only after usage has been
durably recorded. Usage recording and the terminal Attempt transition must occur
in one transaction, or the Attempt must remain non-terminal until recording
succeeds. This prevents another Attempt from observing a terminal predecessor
without its consumption.

## Worker and HTTP Runtime flow

The Worker treats `runtime_budget` as immutable Assignment data and passes it to
the selected Adapter. It must not reinterpret units or substitute a soft
watchdog.

For Claude:

1. Validate that only the supported estimated-cost dimension is present.
2. Convert integer micro-USD to a decimal dollar string using integer/string
   operations. For example, `1250000` becomes `1.250000`. Floating-point
   conversion is forbidden.
3. Add `--max-budget-usd <amount>` to the existing print-mode command.
4. Preserve the existing self-hosted HTTP and Docker/OCI lifecycle.
5. Parse final token usage, `total_cost_usd`, and the budget-stop condition into
   the Adapter Outcome.

For observed cost, JSON numbers are retained as decimal values rather than first
converted through binary floating point. Values with precision below one
micro-USD are rounded upward so accounting cannot understate usage. Invalid,
negative, non-finite, or overflowing values make the report incomplete and fail
closed.

Codex receives no budget arguments in this delivery. A scheduling bug that sends
it a finite Runtime budget must be rejected by the Adapter before process start.

## Outcome and accounting

The Adapter Outcome gains:

- optional token totals using the existing telemetry structure;
- optional `cost_units` as an integer;
- usage completeness;
- a `budget_reached` signal or equivalent stable stop reason.

Claude results containing valid token and cost totals produce a complete durable
`goal_usage` record. The existing usage ID, Attempt binding, request hash, and
immutable replay rules continue to provide idempotency. A retry is a new Attempt
and receives only the remaining cumulative allowance.

When Claude reports its native budget boundary, the Server records all available
final usage before terminating the Attempt with
`RUNTIME_BUDGET_EXHAUSTED`. This is distinct from cancellation, timeout, and a
generic Runtime failure.

If the container crashes, the HTTP stream disconnects, the result is malformed,
or the final usage event is absent, the Server records incomplete usage. The
Attempt may terminate, but later finite-budget Assignment remains blocked until
the usage uncertainty is resolved through an explicit future reconciliation
mechanism. This design does not invent a zero value or an operator override.

## Read API and observability

The existing `runtime_hard_budget_supported` projection remains compatible. It is
true only when all finite dimensions for the resolved execution are supported.
An additive capability projection may expose the precise supported semantics so
clients can explain why a policy is blocked.

Metrics and structured events distinguish at least:

- unsupported Runtime/dimension;
- exhausted cumulative budget;
- blocked incomplete usage;
- blocked concurrent Attempt;
- Runtime-native budget termination;
- malformed or overflowing Runtime usage.

No metric or log includes credentials. The budget value is policy data and may be
logged with Goal/Attempt identifiers under the existing observability rules.

## Compatibility and rollout

All new protobuf fields are optional. Older Workers do not advertise the new
capability and therefore cannot receive finite-budget work. Unbudgeted
Codex/Claude execution retains its existing command and scheduling path.

The current `goal_budget_policy` and `goal_usage.cost_units` columns already hold
the required policy and ledger values, so a schema migration is not expected.
Implementation planning must re-check whether an index or explicit in-flight
query is needed for the scheduling transaction; any schema change must remain
forward-only and migration-tested.

Documentation updates cover the README, Goal governance contract, HTTP Runtime
contract, closeout plan, and capability matrix. They must describe this as a
Runtime estimated-cost boundary, not provider billing enforcement.

## Verification and acceptance

Focused Server tests cover:

- exact remaining-budget calculation and integer overflow;
- exhausted, incomplete, unsupported, and unknown-semantic rejection;
- provider-specific capability matching;
- single-in-flight exclusion for finite-budget Goals;
- atomic usage recording before the next Assignment;
- retry accumulation and idempotent result replay;
- unchanged unbudgeted concurrency.

Adapter tests cover:

- lossless micro-USD formatting, including sub-dollar and large values;
- exact Claude argument construction;
- valid cost/token result parsing and upward micro-USD rounding;
- native budget-stop recognition;
- malformed, negative, non-finite, and overflowing reports;
- rejection of finite budgets by unsupported providers.

Docker/OCI CI uses a fake Claude executable to verify normal completion, native
budget termination, container failure, stream interruption, and a missing final
usage event without requiring external credentials. Protocol generation checks,
focused Go tests, full repository tests, and documentation consistency checks are
release gates.

Real-provider acceptance remains a later validation activity. CI simulation
proves propagation, accounting, lifecycle, and fail-closed behavior; it does not
certify Anthropic billing accuracy.

## Non-goals

- Pricing models or conversion from tokens to cost inside computecloud.
- Actual invoice enforcement or reconciliation with provider billing.
- Soft post-usage cancellation advertised as a hard limit.
- Codex token/cost enforcement or Claude total-token enforcement without native
  support.
- Concurrent budget reservation or allocation among Attempts.
- An LLM Gateway, Model Serving Platform, general Workflow Engine, AI Execution
  OS, or GPU Cloud.
- Automatic Re-plan, broader Goal/RPG closure, or real-host release
  certification as part of this slice.

## Completion criteria

This slice is complete when a cost-only Claude Goal can be assigned exclusively
to a capable Runtime, receives the exact remaining estimated-cost boundary,
records complete usage before releasing the Goal, terminates with a stable budget
reason when the Runtime reaches its boundary, and passes the CI acceptance above.
Every unsupported or uncertain case must remain fail-closed, and no documentation
may claim an actual provider-billing ceiling.
