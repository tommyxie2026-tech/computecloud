# P0–P2 execution ledger

Baseline: main `8540e06` (2026-09-30). Started 2026-10-06.
Product: Agent-aware Distributed Job Execution Platform / Agent Job Executor.

This ledger separates implementation, automated validation, merge/release, and
real-environment acceptance. A role assignment is not proof of delivery.

| Order | Scope | Status / acceptance |
| --- | --- | --- |
| P0 | Retry storage gate, deterministic process-stop fixture, schema documentation | COMPLETE: PR #92 merged; full matrix and main CI 37435035493 PASS |
| P1-A | Workspace cache, bounded GC, warm materialization, metrics and benchmark | PR #93 merged; full matrix 37434895415 PASS. Performance acceptance remains PARTIAL: local provider P50 43.239/80.388 ms exceeds the <=40% ratio target; real workload acceptance pending |
| P1-B | Readiness advertisement, stale/unknown handling and explainability | PR #94 merged; full matrix 37436461912 PASS. Observation-only signals; scheduling scoring remains disabled |
| P1-C | Production Baseline | BLOCKED on external environment: two independent Linux Workers, pinned real Runtime/MCP versions, accounts/budgets and a 24h+ window; #1/#9 remain open |
| P1-D | Goal compatibility, persistence, execution/evaluation integration, RPG-4 and Control bridge | PR #95–100 merged; full matrices 37442929805 / 37450161254 PASS. Synthetic execution, atomic reservations, artifact evaluation, human decisions and Control audit projection implemented. Atomic Guard → new Plan/Job publication, permission consumption and enforceable Runtime token/cost caps still pending; RPG remains PARTIAL |
| P2-A | Experimental relay | PR #102 merged (full matrix 37465996087 PASS); PR #103 adds explicit operator TLS rendezvous fixture. Direct-first connector and dual Worker Job recovery implemented on follow-up branch with local race evidence. Unattended ticket provisioning, default-config opt-in wiring, control/event/bulk priority, full operations and real NAT acceptance remain pending |
| P2-B | Control review UX | PR #101 merged; full matrix 37465407006 PASS. Authenticated bounded report/patch text preview with full hash and Attempt provenance; no patch execution |
| P2-C | Mobile follow-ups | Existing foundation preserved. Pairing/push implementation, platform signing and device acceptance remain pending; no signed native package is claimed |

## P0 compatibility correction

- v0.4.5: Server schema v12 / Worker v6 (historical evidence remains unchanged).
- v0.4.6 tag: Server schema v13 / Worker v6; Control write lease migration.
- Current main: Server schema v16 / Worker v6; v15 adds Goal execution records, v16 adds governance/usage.
- Never downgrade `PRAGMA user_version`. Drain execution and back up complete data
  directories before upgrade. Restore compatible pre-upgrade backups for rollback.
- Latest main run 36688523493 failed retry-flow (expected 13, actual 14) and the
  macOS process-stop fixture (fixed startup delay raced Python signal setup).
  The fixture now waits for the child to acknowledge installed signal handling.
- C1 Observe and UI-03b Submit/bounded Retry are merged and included in v0.4.6.
  Control artifact review and synthetic Goal execution are now merged. Complete Mobile Beta, RPG closure and production Relay remain separate.

## Execution rules

Keep changes in independently reviewable PRs. Public contracts and migrations
are separate from dependent feature changes. Preserve existing CI gates and
run the full main/release matrix before claiming a new stable baseline.
No release, production certification, signed mobile distribution, or completed
RPG closure may be inferred from fixture tests or a prepared deployment guide.

## Remaining work in priority order

1. P1: validate cache targets on representative workloads; complete real Production
   Baseline with independent hosts and authorized real-runtime budgets.
2. P1: implement atomic Guard/Plan/Job publication and one-use governance permission
   consumption. Obtain enforceable adapter token/cost ceilings before claiming
   bounded autonomous spend; current finite policies fence unsupported execution.
3. P2: complete ticket distribution and explicit Worker/Server Relay configuration,
   traffic priority/operations, then real NAT/fault/long-task acceptance.
4. P2: implement Mobile pairing/push with explicit device credential lifecycle;
   configure Apple/Android signing and validate on real devices.

These are outstanding implementation/acceptance items, not completed merely by
merging the foundations. No new stable release or production certification was
created in this execution pass.
