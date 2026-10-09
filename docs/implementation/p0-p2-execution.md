# P0–P2 execution ledger

Initial baseline: main `8540e06` (2026-09-30). Current pre-PR baseline: `dd36008` (2026-10-08).
Product: Agent-aware Distributed Job Execution Platform / Agent Job Executor.

This ledger separates implementation, automated validation, merge/release, and
real-environment acceptance. A role assignment is not proof of delivery.

The scoped previews `v0.4.7-rc.1` and `v0.4.7-rc.2` were published as
prereleases. The latter freezes Goal governance and the bounded Runtime budget
slice; its release gates, artifacts and limitations are tracked in
[the release record](v0.4.7-rc.2-release-status.md).
The [certification simulation gate](../validation/certification-simulation.md)
collects repeatable CI evidence, but cannot close the real-environment baseline.
The [v0.4.x closeout plan](v0.4-closeout-plan.md) now tracks the remaining
ecosystem, Goal, Relay and real-acceptance slices against explicit release gates.

| Order | Scope | Status / acceptance |
| --- | --- | --- |
| P0 | Retry storage gate, deterministic process-stop fixture, schema documentation | COMPLETE: PR #92 merged; full matrix and main CI 37435035493 PASS |
| P1-A | Workspace cache, bounded GC, warm materialization, metrics and benchmark | PR #93, #109 and #111 merged. Bounded parallel file copies plus overlapped integrity validation give three local macOS fixture P50 ratios of 20.2%, 20.1% and 20.7%. Linux single-runner simulation Gate passed in full matrix 37564049429, including cached/cold P50 <=40%; representative Linux Worker/real Job acceptance remains pending |
| P1-B | Readiness advertisement, stale/unknown handling and explainability | PR #94 merged; full matrix 37436461912 PASS. Observation-only signals; scheduling scoring remains disabled |
| P1-C | Production Baseline | BLOCKED on external environment: two independent Linux Workers, pinned real Runtime/MCP versions, accounts/budgets and a 24h+ window; #1/#9 remain open |
| P1-D | Goal compatibility, persistence, execution/evaluation integration, RPG-4 and Control bridge | PR #95–100 and #124–125 merged; the current code/CI slice was published in prerelease v0.4.7-rc.2. Synthetic execution, atomic reservations, artifact evaluation, human decisions, Control audit projection, atomic Guard → Plan/Job publication, one-use permission consumption and cost-only `claude_http` enforcement are implemented. PR #125 exact-head CI 37805837985 and release CI 37858141897 passed, including real Docker/fake-Claude OCI coverage; real Provider and independent-host acceptance remain pending, so RPG remains PARTIAL |
| P2-A | Experimental relay | PR #102–104 merged; full matrices 37465996087 / 37468359266 / 37468567184 PASS. Explicit operator TLS rendezvous fixture, direct-first connector and dual Worker Job recovery Gate are implemented. Unattended ticket provisioning, default-config opt-in wiring, control/event/bulk priority, full operations and real NAT acceptance remain pending |
| P2-B | Control review UX | PR #101 merged; full matrix 37465407006 PASS. Authenticated bounded report/patch text preview with full hash and Attempt provenance; no patch execution |
| P2-C | Mobile follow-ups | Existing foundation preserved. Pairing/push implementation, platform signing and device acceptance remain pending; no signed native package is claimed |

## P0 compatibility correction

- v0.4.5: Server schema v12 / Worker v6 (historical evidence remains unchanged).
- v0.4.6 tag: Server schema v13 / Worker v6; Control write lease migration.
- Current main: Server schema v16 / Worker v6; v15 adds Goal execution records, v16 adds governance/usage.
- Never downgrade `PRAGMA user_version`. Drain execution and back up complete data
  directories before upgrade. Restore compatible pre-upgrade backups for rollback.
- The earlier main run 36688523493 failed retry-flow (expected 13, actual 14)
  and the macOS process-stop fixture (fixed startup delay raced Python signal
  setup). PR #92 corrected both and passed the full matrix; this is historical
  failure context, not the current main status.
- C1 Observe and UI-03b Submit/bounded Retry are merged and included in v0.4.6.
  Control artifact review and synthetic Goal execution are now merged. Complete Mobile Beta, RPG closure and production Relay remain separate.

## Execution rules

Keep changes in independently reviewable PRs. Public contracts and migrations
are separate from dependent feature changes. Preserve existing CI gates and
run the full main/release matrix before claiming a new stable baseline.
No release, production certification, signed mobile distribution, or completed
RPG closure may be inferred from fixture tests or a prepared deployment guide.

## Remaining work in priority order

1. v0.4 ecosystem: validate the implemented self-hosted HTTP Runtime, isolated
   Container Environment Provider and authenticated Trigger/Delivery adapter
   against fixed real versions. Keep each negative/restart Gate independently
   reviewable. Token limits, Codex cost limits and mixed-provider finite budgets
   remain fail-closed.
2. P2: complete Relay traffic priority, metrics and operations. Keep `direct`
   the default and validate real NAT/fault/long-task behavior separately.
3. Last: measure cache targets on representative Linux Worker workloads and
   complete the real Production Baseline with independent hosts and authorized
   real-runtime budgets. The local ten-Attempt concurrency Gate and CI
   provider-fixture P50 target do not substitute for real Job acceptance.

Mobile pairing/push, signing and device acceptance remain in the v0.5.x C3
Mobile Beta lane, separate from v0.4.x exit gates.

These are outstanding implementation/acceptance items, not completed merely by
merging the foundations. The scoped prerelease does not create a new stable
release or production certification.
