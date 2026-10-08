# Runtime Budget Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce cumulative Goal cost admission through Claude HTTP Runtime's native estimated-USD stop boundary while keeping unsupported token/cost dimensions fail-closed.

**Architecture:** The Server computes a frozen remaining budget from the durable Goal ledger, admits only a matching Runtime capability, and permits one in-flight Attempt for a finite-budget Goal. The Worker carries the budget into the self-hosted HTTP Adapter, which injects Claude's native `--max-budget-usd`; terminal metrics are settled into `goal_usage` in the same Server transaction that releases the Attempt.

**Tech Stack:** Go 1.27, protobuf/gRPC, SQLite transactions, Claude Code print-mode stream JSON, self-hosted Unix HTTP Runtime, Docker/OCI, Python CI report scripts.

**Spec:** `docs/superpowers/specs/2026-10-08-runtime-budget-enforcement-design.md`

## Global Constraints

- One `cost_unit` is one micro-USD of Claude's client-side estimated API spend.
- The only initial capability is `runtime:budget_claude_estimated_usd_v1`, advertised by a compatible `claude_http` Runtime.
- Claude may exceed the supplied estimate by one completed API call; never describe the feature as an actual invoice or mathematically non-exceeding cap.
- Any finite token budget, Codex cost budget, Claude token budget, unknown cost semantics, or incomplete prior usage remains fail-closed.
- A finite-budget Goal has at most one in-flight Attempt; unbudgeted concurrency remains unchanged.
- Keep self-hosted HTTP + Docker/OCI execution and the Agent Job Executor product boundary. Do not add pricing, an LLM Gateway, model serving, or a workflow engine.
- Use integer/decimal-string conversion only. Never pass budget or observed cost through binary floating point.
- Existing unbudgeted Assignments and older Workers remain wire-compatible.

## Review Focus

- A Claude result can report `error_max_budget_usd` with observed cost above the dispatched remainder; `TestClaudeBudgetResultAllowsOneCallOvershoot` must retain that value and map the stable reason, while `TestBudgetExhaustedIsTerminal` must prove it is not retried.
- Decimal costs can use exponent notation or more than six fractional digits; `TestParseUSDToMicrosRoundsUpWithoutFloat` must round upward and reject negative, non-finite, and overflowing inputs.
- An older HTTP controller can lack the cost capability while still running unbudgeted work; `TestConfiguredHTTPRuntimeCapabilitiesFailClosedByVersion` must omit only budget eligibility.
- A crash or missing final result can leave partial metrics; `TestBudgetedCompletionWithUnknownUsageBlocksNextAttempt` must persist incomplete usage and prevent another Assignment.
- Completion and scheduling can race; `TestFiniteBudgetGoalAllowsOnlyOneInflightAttempt` must prove that the second Attempt cannot claim the same remaining allowance before the first usage settlement commits.

## File Map

- `api/agent/v1/runtime.proto`, `api/agent/v1/runtime.pb.go`: optional Assignment budget envelope.
- `internal/adapter/budget.go`: exact USD-micro formatting/parsing and HTTP Runtime budget validation.
- `internal/adapter/adapter.go`, `internal/adapter/execution.go`, `internal/adapter/http_runtime.go`: Outcome fields, configured capability discovery, budget propagation, and Claude result parsing.
- `internal/agenthttp/server.go`: version-scoped HTTP health capabilities and unchanged container command boundary.
- `internal/telemetry/metrics.go`: cost and completeness fields in persisted Attempt metrics.
- `internal/governance/budget.go`, `internal/governance/governance.go`: overflow-safe remaining-budget calculation.
- `internal/server/goal_runtime_budget.go`: Goal admission, provider capability requirement, and single-in-flight check.
- `internal/server/server.go`, `internal/server/goal_publication.go`: budget-aware Worker selection, Assignment creation, and publication admission.
- `internal/server/goal_governance.go`, `internal/server/results.go`: atomic terminal usage settlement and read projection.
- `internal/worker/worker.go`, `internal/worker/execute.go`: configured capability advertisement, Adapter budget input, metrics, and terminal reason.
- `scripts/ci_runtime_budget.py`, `Makefile`, `.github/workflows/ci.yml`: credential-free CI acceptance and evidence.
- `README.md` and implementation documents named in Task 7: supported/unsupported capability and release status.

---

### Task 1: Add the wire envelope and internal metric types

**Files:**
- Modify: `api/agent/v1/runtime.proto`
- Regenerate: `api/agent/v1/runtime.pb.go`
- Modify: `internal/adapter/adapter.go`
- Modify: `internal/adapter/execution.go`
- Modify: `internal/telemetry/metrics.go`
- Create: `internal/adapter/runtime_budget_test.go`

**Interfaces:**
- Produces: `pb.RuntimeBudget` with optional `RemainingTokenUnits`, optional `RemainingCostUnits`, and `CostSemantics`.
- Produces: `adapter.PrepareRequest.Budget *pb.RuntimeBudget` and copied `PreparedExecution.Budget *pb.RuntimeBudget`.
- Produces: `adapter.Outcome.CostUnits *int64`, `CostComplete bool`, and `BudgetReached bool`.
- Produces: matching `telemetry.Attempt.CostUnits *int64`, `CostComplete bool`, and `BudgetReached bool` JSON fields.

- [ ] **Step 1: Write `TestRuntimeBudgetWirePresenceAndCopy`**

  Assert protobuf round trips distinguish an absent limit from `1`, preserve `usd_micros_client_estimate`, and `prepareCLI` copies rather than aliases the budget.

- [ ] **Step 2: Run the focused test and verify it fails**

  Run: `go test ./internal/adapter -run TestRuntimeBudgetWirePresenceAndCopy -count=1`

  Expected: FAIL because `RuntimeBudget` and the budget fields do not exist.

- [ ] **Step 3: Add the minimal protocol and Go fields**

  Add `message RuntimeBudget` and field `RuntimeBudget runtime_budget = 10` to `Assignment`; use proto3 `optional int64` for both dimensions. Run `make generate`, then add the Adapter and telemetry fields listed in Interfaces.

- [ ] **Step 4: Run protocol and Adapter tests**

  Run: `go test ./api/agent/v1 ./internal/adapter ./internal/telemetry -count=1`

  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add api/agent/v1/runtime.proto api/agent/v1/runtime.pb.go internal/adapter/adapter.go internal/adapter/execution.go internal/adapter/runtime_budget_test.go internal/telemetry/metrics.go
  git commit -m "feat: add runtime budget assignment envelope"
  ```

### Task 2: Advertise Claude HTTP cost capability from the configured controller

**Files:**
- Create: `internal/adapter/configured_capabilities.go`
- Modify: `internal/adapter/http_runtime.go`
- Modify: `internal/agenthttp/server.go`
- Modify: `internal/worker/worker.go`
- Modify: `internal/adapter/http_runtime_test.go`
- Modify: `internal/agenthttp/server_test.go`
- Modify: `internal/worker/runtime_execution_test.go`

**Interfaces:**
- Produces: `adapter.ConfiguredCapabilities(ctx context.Context, p Provider, r config.Runtime) (CapabilitySet, error)`.
- Produces: HTTP health JSON field `capabilities []string` containing unqualified Runtime capability values.
- Produces: advertised Worker capability `runtime:budget_claude_estimated_usd_v1` only for compatible `claude_http` configuration.

- [ ] **Step 1: Write configured-capability failure tests**

  Add `TestConfiguredHTTPRuntimeCapabilitiesFailClosedByVersion` and assertions that Codex never advertises the cost capability, Claude `2.1.217+` does, an older Claude omits it without failing the unbudgeted health probe, and unknown controller capabilities are rejected.

- [ ] **Step 2: Run the focused tests and verify they fail**

  Run: `go test ./internal/adapter ./internal/agenthttp ./internal/worker -run 'ConfiguredHTTPRuntimeCapabilities|HTTPRuntimeHealthCapabilities' -count=1`

  Expected: FAIL because health and Worker advertisement are static.

- [ ] **Step 3: Implement configured capability discovery**

  Implement the optional configured-capability interface in `configured_capabilities.go`; have `httpRuntimeProvider` read and validate health capabilities, and have `Worker.probe` advertise that resolved set. Keep tools, environments, and legacy capabilities from the existing static descriptor.

- [ ] **Step 4: Add the controller version gate**

  Add a small integer semantic-version comparator for Claude's `major.minor.patch` configured version. Health returns `budget_claude_estimated_usd_v1` only for `claude_http` at `2.1.217` or later. Invalid versions omit the capability rather than granting it.

- [ ] **Step 5: Run Runtime capability tests**

  Run: `go test ./internal/adapter ./internal/agenthttp ./internal/worker -run 'Capabilities|HTTPRuntime|RuntimeExecution' -count=1`

  Expected: PASS with old-controller unbudgeted compatibility intact.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/adapter/configured_capabilities.go internal/adapter/http_runtime.go internal/agenthttp/server.go internal/worker/worker.go internal/adapter/http_runtime_test.go internal/agenthttp/server_test.go internal/worker/runtime_execution_test.go
  git commit -m "feat: advertise configured Claude cost limits"
  ```

### Task 3: Enforce and parse the native Claude estimated-cost boundary

**Files:**
- Create: `internal/adapter/budget.go`
- Create: `internal/adapter/budget_test.go`
- Modify: `internal/adapter/http_runtime.go`
- Modify: `internal/adapter/adapter.go`
- Modify: `internal/agenthttp/server_test.go`

**Interfaces:**
- Produces: `formatUSDmicros(units int64) (string, error)`.
- Produces: `parseUSDmicros(raw json.RawMessage) (int64, error)` with upward rounding below one micro-USD.
- Produces: `applyHTTPRuntimeBudget(profile string, args []string, budget *pb.RuntimeBudget) ([]string, error)`.
- Consumes: `pb.RuntimeBudget` and Outcome fields from Task 1.

- [ ] **Step 1: Write exact decimal and unsupported-provider tests**

  Add table tests for `1 -> 0.000001`, `1250000 -> 1.250000`, exponent input, seven fractional digits rounded upward, maximum governance value, negative/non-finite/overflow rejection, and Codex/token/unknown-semantics rejection before start.

- [ ] **Step 2: Write Claude result tests**

  Add `TestClaudeBudgetResultAllowsOneCallOvershoot`: parse a result with subtype `error_max_budget_usd`, valid usage, and `total_cost_usd` above the assigned remainder; assert `BudgetReached`, `RUNTIME_BUDGET_EXHAUSTED`, complete token/cost telemetry, and the observed cost without clamping.

- [ ] **Step 3: Run the tests and verify they fail**

  Run: `go test ./internal/adapter -run 'USD|Budget' -count=1`

  Expected: FAIL because conversion, injection, and cost parsing are absent.

- [ ] **Step 4: Implement decimal conversion and Claude parsing**

  Parse JSON numeric text directly with integer decimal arithmetic. Append `--max-budget-usd` only in `claude_http` preparation. Recognize exact subtype `error_max_budget_usd`; do not infer budget termination from free-form error text.

- [ ] **Step 5: Verify the HTTP container command contract**

  Extend `TestRunLifecycleIdempotencyAndRestartFence` or add `TestClaudeBudgetDockerCommand` so the fake process observes the exact flag/value after the OCI entrypoint and emits a fixture budget result.

- [ ] **Step 6: Run Adapter and controller tests**

  Run: `go test ./internal/adapter ./internal/agenthttp -run 'USD|Budget|HTTPRuntime|RunLifecycle' -count=1`

  Expected: PASS.

- [ ] **Step 7: Commit**

  ```bash
  git add internal/adapter/budget.go internal/adapter/budget_test.go internal/adapter/http_runtime.go internal/adapter/adapter.go internal/agenthttp/server_test.go
  git commit -m "feat: enforce Claude estimated cost boundary"
  ```

### Task 4: Calculate cumulative remaining Goal budget safely

**Files:**
- Create: `internal/governance/budget.go`
- Create: `internal/governance/budget_test.go`
- Modify: `internal/governance/governance.go`

**Interfaces:**
- Produces: `governance.UsageBudget{MaxTokens, MaxCostUnits, RemainingTokens, RemainingCostUnits *int64}`.
- Produces: `governance.RemainingUsageTx(ctx context.Context, q store.Query, goalID string) (UsageBudget, error)`.
- Preserves: `CheckUsageTx` error strings `GOAL_USAGE_UNKNOWN` and `GOAL_USAGE_EXHAUSTED` by delegating to the new calculation.

- [ ] **Step 1: Write remaining-budget and overflow tests**

  Cover no policy, cost-only unused, accumulated retries, equality exhausted, consumed above maximum, incomplete/missing usage, nullable dimensions, and a multi-row sum that would overflow `int64`.

- [ ] **Step 2: Run the tests and verify they fail**

  Run: `go test ./internal/governance -run 'RemainingUsage|UsageOverflow' -count=1`

  Expected: FAIL because the API is missing.

- [ ] **Step 3: Implement row-by-row checked accumulation**

  Avoid SQLite `sum` overflow and binary floating point. Return nil fields for unlimited dimensions, reject unknown usage before calculating a remainder, and return exhausted rather than a zero/negative dispatch value.

- [ ] **Step 4: Make `CheckUsageTx` use the new helper**

  Preserve all existing callers and error behavior; add the new tests to the existing governance suite.

- [ ] **Step 5: Run governance tests**

  Run: `go test ./internal/governance ./internal/server -run 'GoalGovernance|RemainingUsage|UsageOverflow' -count=1`

  Expected: PASS.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/governance/budget.go internal/governance/budget_test.go internal/governance/governance.go
  git commit -m "feat: calculate remaining Goal usage budget"
  ```

### Task 5: Make scheduling and publication budget-aware

**Files:**
- Create: `internal/server/goal_runtime_budget.go`
- Create: `internal/server/runtime_budget_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/goal_publication.go`
- Modify: `internal/server/goal_publication_test.go`

**Interfaces:**
- Produces: `buildGoalRuntimeBudget(ctx context.Context, q store.Query, j *Job, t *pb.Task) (*pb.RuntimeBudget, []string, error)`; the string slice is the additional Worker capability requirement.
- Produces: transient blocker `GOAL_BUDGET_IN_FLIGHT` for the single-in-flight fence.
- Consumes: `governance.RemainingUsageTx` from Task 4 and the wire envelope from Task 1.

- [ ] **Step 1: Write scheduler admission tests**

  Add tests for cost-only `claude_http`, Codex cost, Claude token, mixed token/cost, unknown semantics, no capable Worker, exhausted usage, incomplete usage, and unchanged unbudgeted scheduling.

- [ ] **Step 2: Write the concurrency race test**

  `TestFiniteBudgetGoalAllowsOnlyOneInflightAttempt` queues two eligible Tasks for one finite-budget Goal and asserts only one Assignment is persisted; the other receives `GOAL_BUDGET_IN_FLIGHT` until settlement.

- [ ] **Step 3: Run the tests and verify they fail**

  Run: `go test ./internal/server -run 'RuntimeBudget|FiniteBudgetGoal' -count=1`

  Expected: FAIL because the current scheduler rejects every finite budget.

- [ ] **Step 4: Implement budget-aware candidate selection and Assignment freeze**

  Compute the budget before scanning peers, include the returned capability in the matching Runtime's required set, remove the blanket finite-budget rejection, and place the frozen budget on the selected Assignment. Query active Goal reservations joined to `attempts.released=0` inside the same transaction. Preserve distinct scheduler blockers `GOAL_RUNTIME_BUDGET_UNSUPPORTED`, `GOAL_USAGE_EXHAUSTED`, `GOAL_USAGE_UNKNOWN`, and `GOAL_BUDGET_IN_FLIGHT` in the existing scheduler observation/event path.

- [ ] **Step 5: Narrow the governed publication gate**

  Allow cost-only proposals whose frozen execution resolves to `claude_http`; preserve `GOAL_RUNTIME_BUDGET_UNSUPPORTED` for all other finite dimensions/providers. Publication does not depend on a currently online Worker.

- [ ] **Step 6: Run Server scheduling/publication tests**

  Run: `go test ./internal/server -run 'RuntimeBudget|FiniteBudgetGoal|GoalProposal|GoalPublication|Scheduler' -count=1`

  Expected: PASS.

- [ ] **Step 7: Commit**

  ```bash
  git add internal/server/goal_runtime_budget.go internal/server/runtime_budget_test.go internal/server/server.go internal/server/goal_publication.go internal/server/goal_publication_test.go
  git commit -m "feat: admit supported Goal runtime budgets"
  ```

### Task 6: Settle usage atomically before releasing the Attempt

**Files:**
- Modify: `internal/worker/execute.go`
- Modify: `internal/server/goal_governance.go`
- Modify: `internal/server/results.go`
- Modify: `internal/server/goal_governance_test.go`
- Modify: `internal/server/retry_v3_test.go`
- Modify: `internal/server/job_recovery_test.go`
- Modify: `internal/server/runtime_budget_test.go`

**Interfaces:**
- Produces: `recordBoundGoalAttemptUsage(ctx context.Context, q store.Query, taskID, attemptID string) error` that no-ops for non-Goal Tasks and records exactly one immutable usage row for Goal Attempts.
- Consumes: Adapter and telemetry cost/completeness fields from Tasks 1 and 3.
- Preserves: existing `runtime-<attempt_id>` usage IDs and `USAGE_CONFLICT` idempotency.

- [ ] **Step 1: Write atomic settlement and unknown-usage tests**

  Add `TestBudgetedCompletionSettlesUsageBeforeRelease` and `TestBudgetedCompletionWithUnknownUsageBlocksNextAttempt`. Assert complete Claude metrics store tokens/cost and release the gate in one transaction; missing final/cost stores an incomplete row and keeps future budget admission closed.

- [ ] **Step 2: Write `TestBudgetExhaustedIsTerminal`**

  Complete an Attempt with `RUNTIME_BUDGET_EXHAUSTED` under a replay-safe retry policy and assert no retry is scheduled, observed cost is retained even when it exceeds the dispatched remainder, and the Task ends with that stable code.

- [ ] **Step 3: Run focused tests and verify they fail**

  Run: `go test ./internal/server ./internal/worker -run 'BudgetedCompletion|BudgetExhausted|UnknownUsage' -count=1`

  Expected: FAIL because cost is not in metrics and usage settles only during terminal Goal evaluation.

- [ ] **Step 4: Persist cost metrics and stable terminal reason in the Worker**

  Copy `CostUnits`, `CostComplete`, and `BudgetReached` into `attempt.metrics`. When `BudgetReached` is true, complete with `RUNTIME_BUDGET_EXHAUSTED` after Runtime/environment cleanup and artifact handling; do not turn it into success.

- [ ] **Step 5: Settle Goal usage inside `CompleteAttempt`**

  Read the already-watermarked `attempt_metrics`, record complete usage only when native final, token complete, and cost complete are all true, otherwise record incomplete usage. Invoke settlement before `attempts.released=1` in the existing completion transaction. Keep terminal Goal evaluation as an idempotent backfill for historical rows.

- [ ] **Step 6: Run completion, recovery, and retry tests**

  Run: `go test ./internal/server ./internal/worker -run 'Goal|Budget|Retry|Recovery|Completion' -count=1`

  Expected: PASS, including lost completion ACK replay and Worker restart behavior.

- [ ] **Step 7: Commit**

  ```bash
  git add internal/worker/execute.go internal/server/goal_governance.go internal/server/results.go internal/server/goal_governance_test.go internal/server/retry_v3_test.go internal/server/job_recovery_test.go internal/server/runtime_budget_test.go
  git commit -m "feat: settle Goal runtime cost atomically"
  ```

### Task 7: Expose support accurately and add release evidence

**Files:**
- Modify: `internal/server/goal_governance.go`
- Modify: `internal/server/goal_governance_test.go`
- Create: `scripts/ci_runtime_budget.py`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Modify: `docs/implementation/goal-governance-contract.md`
- Modify: `docs/implementation/agent-http-runtime.md`
- Modify: `docs/implementation/v0.4-closeout-plan.md`
- Modify: `docs/implementation/parallel-development-plan.md`

**Interfaces:**
- Produces: compatible `runtime_hard_budget_supported` plus additive `runtime_budget_capabilities` in the Goal projection.
- Produces: `make ci-runtime-budget` and report schema `ci-runtime-budget.v1` with `real_model_calls: false`.

- [ ] **Step 1: Write projection tests**

  Assert the boolean is true only when every finite dimension in the bound execution is supported, and the additive list names only `runtime:budget_claude_estimated_usd_v1`. Unsupported and mixed policies remain false.

- [ ] **Step 2: Implement the projection without changing existing fields**

  Derive support from durable policy and bound Job Runtime profiles, not current Worker presence. Keep `usage_complete` and `automatic_replan_enabled` semantics unchanged.

- [ ] **Step 3: Add credential-free CI evidence**

  `ci_runtime_budget.py` runs the focused Adapter, controller, Worker, governance, and Server tests; checks the generated protocol is current; writes commands/logs plus PASS/FAIL coverage for propagation, capability fencing, decimal safety, single-in-flight, atomic settlement, crash/incomplete handling, and `real_model_calls: false`.

- [ ] **Step 4: Wire the Make target and CI artifact**

  Add `ci-runtime-budget` to `Makefile` and a dedicated Ubuntu workflow job that uploads `dist/ci-runtime-budget`. Keep the existing OCI image job as the pinned Claude/Codex packaging gate.

- [ ] **Step 5: Update product and implementation documentation**

  Mark Claude HTTP estimated-cost enforcement implemented after tests pass. Explicitly retain Codex cost/token and Claude token as unsupported; document one-API-call overshoot, fail-closed unknown usage, single-in-flight finite Goals, and later real-provider validation. Preserve the Agent Job Executor positioning.

- [ ] **Step 6: Run focused acceptance**

  Run: `make generate && git diff --exit-code api/agent/v1/runtime.pb.go && make ci-runtime-budget && make ci-runtime-adapter-contract && make ci-goal-governance`

  Expected: all targets PASS and both CI report files declare `real_model_calls: false` where applicable.

- [ ] **Step 7: Run repository verification**

  Run: `gofmt -w internal/adapter/adapter.go internal/adapter/execution.go internal/adapter/http_runtime.go internal/adapter/budget.go internal/adapter/configured_capabilities.go internal/adapter/runtime_budget_test.go internal/adapter/budget_test.go internal/adapter/http_runtime_test.go internal/agenthttp/server.go internal/agenthttp/server_test.go internal/telemetry/metrics.go internal/governance/budget.go internal/governance/budget_test.go internal/governance/governance.go internal/worker/worker.go internal/worker/execute.go internal/worker/runtime_execution_test.go internal/server/goal_runtime_budget.go internal/server/runtime_budget_test.go internal/server/server.go internal/server/goal_publication.go internal/server/goal_publication_test.go internal/server/goal_governance.go internal/server/goal_governance_test.go internal/server/results.go internal/server/retry_v3_test.go internal/server/job_recovery_test.go && go test ./... -count=1 && go vet ./... && git diff --check`

  Expected: all packages PASS, vet emits no findings, and diff check is clean.

- [ ] **Step 8: Commit**

  ```bash
  git add internal/server/goal_governance.go internal/server/goal_governance_test.go scripts/ci_runtime_budget.py Makefile .github/workflows/ci.yml README.md docs/implementation/goal-governance-contract.md docs/implementation/agent-http-runtime.md docs/implementation/v0.4-closeout-plan.md docs/implementation/parallel-development-plan.md
  git commit -m "docs: certify Claude runtime cost enforcement"
  ```

### Task 8: Final branch review and release-readiness check

**Files:**
- Review: all files changed since the design-spec commit.

**Interfaces:**
- Consumes: every task above.
- Produces: a reviewable branch whose claims match automated evidence.

- [ ] **Step 1: Review the branch against the specification**

  Run: `git diff 405a713..HEAD --check && git diff --stat 405a713..HEAD`

  Verify every completion criterion in the spec maps to code and a passing test; search for any new claim of actual billing enforcement or support for token/Codex caps.

- [ ] **Step 2: Run the final validation set once on the final tree**

  Run: `go test ./... -count=1 && go vet ./... && make ci-runtime-budget && make ci-runtime-adapter-contract && make ci-goal-governance`

  Expected: PASS with no generated-file or working-tree drift.

- [ ] **Step 3: Request independent code review**

  Review priorities: admission/settlement transaction ordering, decimal overflow, capability trust, protobuf compatibility, and unsupported-dimension fail-closed behavior.

- [ ] **Step 4: Apply valid findings and repeat only affected checks plus the final validation set**

  Commit each coherent correction with a specific `fix:` message. End with `git status --short --branch` showing only the expected commits and no local changes.
