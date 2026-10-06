# P0–P2 execution ledger

Baseline: main `8540e06` (2026-09-30). Started 2026-10-06.
Product: Agent-aware Distributed Job Execution Platform / Agent Job Executor.

This ledger separates implementation, automated validation, merge/release, and
real-environment acceptance. A role assignment is not proof of delivery.

| Order | Scope | Status / acceptance |
| --- | --- | --- |
| P0 | Retry storage gate, deterministic process-stop fixture, schema documentation | Implementation ready; local and remote CI pending |
| P1-A | Workspace cache, bounded GC, warm materialization, metrics and benchmark | Pending; retain immutable templates and isolated Attempt ownership |
| P1-B | Readiness advertisement, stale/unknown handling and explainability | Pending; existing capability/security filters and scheduling order stay authoritative |
| P1-C | Production Baseline | External prerequisites requested: two independent Linux Workers, fixed real Runtime/MCP versions and account budgets, 24h+ window; #1/#9 remain open |
| P1-D | Goal compatibility, persistence, execution/evaluation integration, RPG-4 and Control bridge | Pending; contract → migration → implementation → end-to-end recovery/negative gates; no RPG-5 |
| P2-A | Experimental relay | Pending; RLY-1 through RLY-5 in dependency order, direct default, real NAT/fault evidence required |
| P2-B | Control review UX and Mobile follow-ups | Pending; pairing/push/signing require separate capability and deployment evidence |

## P0 compatibility correction

- v0.4.5: Server schema v12 / Worker v6 (historical evidence remains unchanged).
- v0.4.6 tag: Server schema v13 / Worker v6; Control write lease migration.
- Current main: Server schema v14 / Worker v6; tracing compatibility migration.
- Never downgrade `PRAGMA user_version`. Drain execution and back up complete data
  directories before upgrade. Restore compatible pre-upgrade backups for rollback.
- Latest main run 36688523493 failed retry-flow (expected 13, actual 14) and the
  macOS process-stop fixture (fixed startup delay raced Python signal setup).
  The fixture now waits for the child to acknowledge installed signal handling.
- C1 Observe and UI-03b Submit/bounded Retry are merged and included in v0.4.6.
  C2 review UX, complete Mobile Beta, Goal execution and Relay remain separate.

## Execution rules

Keep changes in independently reviewable PRs. Public contracts and migrations
are separate from dependent feature changes. Preserve existing CI gates and
run the full main/release matrix before claiming a new stable baseline.
No release, production certification, signed mobile distribution, or completed
RPG closure may be inferred from fixture tests or a prepared deployment guide.
