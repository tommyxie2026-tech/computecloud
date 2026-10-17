# v0.5.0 Agent-aware Scheduler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Reconcile Scheduler status with v0.4.9, prove the existing opt-in readiness scorer cannot weaken hard eligibility, and prepare a controlled two-Worker evaluation without duplicating PR #141.

**Architecture:** Reuse the current Server-only eligibility filters, `readiness-v1` score, bounded scheduler evidence, and `prepared_workspace_score_enabled` switch. The switch stays false by default. First document the actual contract and gaps, then add only a targeted regression if the current integration path lacks proof, and finish with an operator runbook. Any missing first-class wire/schema contract becomes a separately reviewed design, not an implicit expansion of this plan.

**Tech Stack:** Go, Protocol Buffers as already defined, SQLite as the existing execution fact store, Make/Go CI targets, Markdown.

**Spec:** `docs/superpowers/specs/2026-10-17-agent-aware-scheduler-v05-design.md`

## Global Constraints

- Product remains Agent-aware Distributed Job Execution Platform / Agent Job Executor.
- `prepared_workspace_score_enabled` remains false by default and for old/unset configuration.
- Hard capability, project, credential, isolation, template, concurrency, and capacity checks run before scoring.
- Preserve `readiness-v1`, its existing factors/weights, signal freshness contract, and tie rotation unless a separately reviewed spec changes them.
- Unknown, stale, malformed, or cross-project readiness data cannot improve a candidate's score or leak in evidence.
- Do not add SQLite schema, public wire fields, or incompatible configuration in this plan.
- CI/fixture evidence does not close the independent v0.4.9 real Provider, dual-host, public-edge, OCI/egress, Relay, cache, or provider-billing acceptance items.

## Review Focus

- An ineligible high-score Worker must never beat an eligible Worker; pin this at Server assignment integration level.
- Default-off and legacy config behavior must retain least-active selection plus rotation; retain and run existing config/fairness tests.
- Candidate order changes must not change score ordering; retain and run existing deterministic scorer tests.
- Scheduler evidence must remain bounded and redact unrelated repository/project data; retain and run readiness/explainability gates.
- Real rollout measurements must be separated from CI simulation and have operator-agreed thresholds before the run; encode this in the pilot runbook.

---

### Task 1: Reconcile current Scheduler and release status

**Files:**
- Modify: `README.md`
- Modify: `docs/implementation/long-term-roadmap.md`
- Modify: `docs/implementation/parallel-development-plan.md`
- Modify: `docs/implementation/readiness-status.md`
- Modify: `docs/superpowers/specs/2026-10-11-agent-aware-scheduler-scoring-design.md`
- Modify: `docs/superpowers/plans/2026-10-11-agent-aware-scheduler-scoring.md`
- Modify: `docs/implementation/p0-p2-execution.md`

**Interfaces:**
- Consumes: v0.4.9 release status, PR #141 merge state, current `internal/server/worker_scoring.go`, and current readiness CI targets.
- Produces: one consistent status: readiness observer/scorer implemented and merged; opt-in remains default-off; real deployment efficacy remains unverified.

- [x] **Step 1: Record the authoritative facts.** Confirm that the release baseline is v0.4.9, PR #141 is merged, `readiness-v1` exists on main, and the config default is false. Do not copy old status claims forward.
- [x] **Step 2: Update the current-state summaries.** Correct the long-term roadmap stable baseline and next-stage link; update readiness and parallel-plan summaries to distinguish merged code/CI from real deployment evidence.
- [x] **Step 3: Close obsolete design/plan status text.** Mark the existing scoring design and checked implementation plan as implemented/merged, preserving historical decisions and limitations.
- [x] **Step 4: Link this plan from README and the P0–P2 ledger.** Keep v0.4.9 environment acceptance visible as an independent pending track.
- [x] **Step 5: Check for contradictory current claims.** Run `rg -n 'v0\.4\.8|implemented locally|remote CI/merge pending|待用户审阅' README.md docs/implementation/long-term-roadmap.md docs/implementation/parallel-development-plan.md docs/implementation/readiness-status.md docs/superpowers/specs/2026-10-11-agent-aware-scheduler-scoring-design.md docs/superpowers/plans/2026-10-11-agent-aware-scheduler-scoring.md`; any remaining match must be clearly historical.

### Task 2: Add assignment-level hard-filter regression for scored candidates

**Files:**
- Modify: `internal/server/fair_scheduling_v35_test.go`
- Test target: `internal/server`

**Interfaces:**
- Consumes: `offlineJobServer`, `fitsWithCapabilities`, `scoreWorker`, and the assignment evidence emitted by `Server.assign`.
- Produces: a regression asserting that an ineligible Worker with a fresh maximum-readiness signal is excluded before scoring and cannot receive the Attempt.

- [x] **Step 1: Write `TestAssignReadinessScoringCannotOverrideCapabilityFilter`.** Enable scoring, create one eligible Worker and one same-project Worker with an incompatible Runtime/model capability, give the ineligible Worker fresh matching readiness signals and the eligible Worker no positive readiness signal, and submit one Job.
- [x] **Step 2: Assert persisted outcome and evidence in the test.** The eligible Worker receives the Attempt; the rejected Worker has a mismatch reason and no score; the selected Worker evidence identifies the score version/reason without exposing repository identity.
- [x] **Step 3: Run `go test ./internal/server -run '^TestAssignReadinessScoringCannotOverrideCapabilityFilter$' -count=1`.** Expected: PASS with the current filter-before-score path. If it fails, fix only the demonstrated bypass without changing any hard-filter or score contract.
- [x] **Step 4: Run the neighboring scorer regressions.** Run `go test ./internal/server -run 'Test(AssignUsesReadinessScoreOnlyWhenEnabled|AssignReadinessScoringCannotOverrideCapabilityFilter|ReadinessCannotOverrideHardFilter|ChooseHighestScoredWorker)' -count=1`.

### Task 3: Publish Scheduler hard-filter traceability and gap matrix

**Files:**
- Create: `docs/implementation/v0.5-scheduler-gap-matrix.md`
- References: `internal/server/server.go`, `internal/server/job_schedule.go`, `internal/server/worker_scoring.go`, `internal/server/readiness.go`, `api/agent/v1/runtime.proto`, `api/agent/v1/runtime.pb.go`

**Interfaces:**
- Consumes: v0.5 scheduler requirements in `docs/implementation/long-term-roadmap.md` and actual eligibility/score/evidence code.
- Produces: a table mapping each requirement to authoritative input, filter/score location, existing test/CI evidence, and status (`implemented`, `partial`, or `not represented`).

- [x] **Step 1: Trace current hard filters.** Include project authorization, Runtime/model/credential/repository/policy/verifier/capability matching, frozen Job template compatibility, strict public isolation/Gateway requirements, credential and project concurrency, Worker lease/readiness, and Worker slots.
- [x] **Step 2: Trace soft scoring and evidence.** Include freshness, four score factors, default-off config, tie ordering, candidate redaction, bounded candidate count, and persisted selection reason.
- [x] **Step 3: Compare first-class contract coverage.** Explicitly state whether Worker pool, network zone, isolation/trust class, minimum CPU/memory/disk, and account class are typed contracts, encoded only as generic capabilities, or absent. Do not label a generic capability as a validated authoritative signal unless code proves it.
- [x] **Step 4: Assign evidence-backed statuses.** Link tests or CI targets for implemented items; identify unsupported requirements as gaps without proposing an unreviewed wire/schema change.
- [x] **Step 5: Add a stop rule.** If a gap requires new public fields, schema, migration, or cross-component contract, record it as a follow-on design item and stop that change for separate user review.

### Task 4: Document controlled scoring pilot and rollback

**Files:**
- Create: `docs/validation/agent-aware-scheduler-pilot.md`
- Reference: `docs/validation/dual-host-preflight.md`

**Interfaces:**
- Consumes: the hard-filter matrix, v0.4.9 release artifact identifiers, and current config/evidence semantics.
- Produces: a reproducible operator procedure for comparing scoring-off baseline with an explicitly enabled, controlled two-Worker pilot.

- [x] **Step 1: Define preflight and candidate freeze.** Require a single source SHA, Server/Worker image digests, two independent Worker identities, matching Runtime versions, and scoring disabled for the baseline.
- [x] **Step 2: Define comparison method.** Require the same task mix, Worker capacity, credentials, and environment for off/on phases; freeze the sample size and pass thresholds before execution instead of inventing universal latency targets.
- [x] **Step 3: Define evidence fields.** Record scoring config, selected-Worker distribution, score/fallback reason, fairness/share, cold/warm preparation latency, end-to-end Job latency, failures/retries, scheduler overhead, and artifact completeness. Prohibit prompts, credentials, absolute private paths, and cross-project data.
- [x] **Step 4: Define rollback and interpretation.** Turning the flag off restores least-active plus rotation. Any sustained Worker monopolization, material regression against the pre-agreed threshold, missing evidence, or hard-filter anomaly keeps the flag off; one pilot cannot establish a general performance claim.
- [x] **Step 5: Keep other production gates separate.** State that the pilot does not prove Provider correctness, public ingress security, OCI/egress isolation, certificate lifecycle, Relay/NAT reliability, cache targets, or vendor billing limits.

### Task 5: Run automated verification and close the implementation record

**Files:**
- Modify: `docs/implementation/p0-p2-execution.md`
- Verification only: `internal/server`, `internal/config`, `internal/readiness`, and existing scheduler CI targets.

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: a verifiable record of code/CI completion and a clearly separate pending target-environment pilot.

- [x] **Step 1: Run targeted tests.** `go test ./internal/server ./internal/config ./internal/readiness -count=1`.
- [x] **Step 2: Run scheduler contract gates.** `make ci-scheduler-readiness-contract ci-scheduler-explainability ci-scheduler-readiness-flow`.
- [x] **Step 3: Run full verification.** `make test race vet`; `vet` must retain the existing vulnerability scan prerequisite.
- [x] **Step 4: Review documentation and diff.** Run `git diff --check`; confirm no current status says scoring is local/unmerged or that real deployment efficacy passed.
- [x] **Step 5: Record pending external pilot accurately.** Mark it `待目标环境验收`; CI/fixture results cannot close it or authorize enabling scoring by default.

## Out of this plan

- Running the real two-Worker pilot requires the operator's target environment and a pre-agreed workload/threshold; this plan prepares the runbook but does not fabricate or claim that evidence.
- Any first-class typed Worker-pool/network/resource contract discovered by Task 3 requires a separate design and review before implementation.
- Execution Usage, Mobile pairing/push, Private Worker Pool, cross-node Artifact, Relay productionization, and other independent tracks remain separate.
