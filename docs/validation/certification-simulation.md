# Release certification simulation gate

The slow `certification-simulation` CI job produces `dist/certification-simulation/report.json`
and the original Go test event log for the exact commit under test. It is a
repeatable **single-runner simulation**, not a Production Baseline certificate.

| Case | CI evidence | Boundary |
| --- | --- | --- |
| Upgrade and restore | v0.4.6 Server v13 fixture → current schema; untouched snapshot restore | No deployed Server or operational data |
| Offline backup | SQLite plus Artifact fixture restore | No stopped production Worker data directory |
| Two Worker Job/lifecycle | Two Worker processes and Job/MCP fixture on one runner | Not two independent Linux hosts or real Codex/Claude accounts |
| Relay restart | TLS Relay and Job recovery on loopback | No NAT, ISP outage or long task |
| Prepared Workspace | Ten cold, nine cached, ten simultaneous provider materializations; cached/cold P50 must be at most 40% | Provider fixture, not full Job or representative Worker repository |

The correctness cases run with Go's race detector. The performance fixture runs
separately without it, so instrumentation does not penalize the in-process
cache path while the cold path spends its time in an external Git process. The
job fails if a required test is missing or fails, or if that fixture P50
target is missed. The report always records `real_hosts=0`,
`real_model_calls=0`, and `real_upgrade=false`; no status field may be used as
evidence for the unrun real environment cases in [the multi-node acceptance
plan](multi-node-poc.md).

A stable production certificate still requires #1: two independently identified
Linux Workers, pinned real Runtime/MCP versions and authorized accounts, real
single and cross-Worker Jobs, controlled process/network faults, a 24-hour-plus
task, artifact integrity and access checks, capacity measurements, and an
upgrade/restore/rollback rehearsal on a deployment clone. The release manager
must attach those records separately before marking the Production Baseline
complete. Goal automatic Re-plan and production Relay each retain their own
implementation and acceptance gates.
