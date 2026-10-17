# Release certification simulation gate

The `certification-simulation` CI job runs on every pull request, main/tag push,
and explicit diagnostic dispatch. It produces `dist/certification-simulation/report.json`
and the original Go test event logs for the exact commit under test. It is a
repeatable **single-runner simulation**, not a Production Baseline certificate.

| Case | CI evidence | Boundary |
| --- | --- | --- |
| Upgrade and restore | v0.4.6 Server v13 fixture → current schema; untouched snapshot restore | No deployed Server or operational data |
| Offline backup | SQLite plus Artifact fixture restore | No stopped production Worker data directory |
| Two Worker Job/lifecycle | Two Worker processes and Job/MCP fixture on one runner | Not two independent Linux hosts or real Codex/Claude accounts |
| Relay restart | TLS Relay and Job recovery on loopback | No NAT, ISP outage or long task |
| Cache digest memory | 64-file digest retains a fixed golden hash and stays within 512 KiB scratch/metadata allocation | No skipped integrity checks; buffer reused within each traversal |
| Prepared Workspace | Ten cold, nine cached, ten simultaneous provider materializations; cached/cold P50 must be at most 40% | Provider fixture, not full Job or representative Worker repository |

## Fixed comparison and unchanged threshold

The job archives the exact baseline commit into a temporary directory on the same
runner, then runs three fixed baseline/candidate rounds in AB/BA/AB order. Both
trees use the candidate `cache_benchmark_test.go` measurement harness; baseline
production code is unchanged. PRs use the target branch SHA, main pushes use the
previous main SHA (new tags with an all-zero `before` use the candidate parent),
and diagnostic dispatch can supply `certification_baseline`.
Every report records resolved SHAs and this harness overlay.

Each round retains all ten cold, nine warm-hit and ten concurrent durations in
original order, plus its Go JSONL log and exit code. The gate recomputes P50 and
the warm/cold ratio from finite positive samples. **Every candidate round must
meet the existing <=0.40 threshold**; a failing baseline never waives a candidate
failure, and the script never retries until a pass appears. Missing/skipped tests,
missing rounds, malformed samples or either arm failing to execute fail the gate.
Baseline ratios are diagnostic evidence; production-host acceptance is separate.
Packaging depends on this gate, so certification failure blocks package/publish.

For a historical comparison, dispatch this branch's workflow with
`certification_baseline` set to the full commit SHA. `run_slow` need not be enabled
for that diagnostic job. Locally, use
`python3 scripts/ci_certification_simulation.py --baseline-ref COMMIT` with full
Git history and Python 3.12+.

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

The main run on `3615040` failed at ratio 0.445 and its unchanged-code rerun
failed at 0.479. In addition to closing the PR coverage gap, the cache digest
now reuses a per-traversal buffer. A 64-file regression first measured 2,158,464
allocated bytes in the original implementation and requires at most 512 KiB,
with the exact digest format preserved. Linux ratios remain subject to the
unchanged three-round <=0.40 gate; no performance gain is inferred from memory
allocation alone.
