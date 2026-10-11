# WS-C readiness observation contract

Status: proposed implementation, 2026-10-06. No SQLite migration.

Renew gains optional ExecutionSignal; WorkerStatus gains the same read-only
projection. Old peers omit or ignore it. Signal fields are bounded lists of ready
Runtime/Environment names, template fingerprints and repository refs, free slots,
prepare P50, measurement time and observation time. All are hints, never authority.

Worker uses at most 32 recent successful preparations and a maximum 30-second
sample lifetime. Server receipt and worker observation must both be fresh within
30 seconds; future timestamps beyond five seconds are invalid. Unknown is absent,
not a fabricated zero-latency sample. Malformed telemetry is discarded without
preventing valid lease renewal. Reconnect/restart starts with unknown signals.

Scheduler preserves all existing capability, credential, security, concurrency
and fairness decisions. It records bounded candidate explanations at successful
dispatch in the same transaction as Attempt creation. Stale/unknown signals never
improve suitability and cannot override hard filters. Optional deterministic
scoring is controlled by `prepared_workspace_score_enabled`, which defaults to
`false`; when off, the existing least-active/tie-rotation decision is unchanged.
When enabled, formula version `readiness-v1` uses runtime/environment warmth,
repository affinity, recent aggregate preparation cost and live Server
load/slot headroom. The aggregate preparation metric is a coarse Worker-level
hint, not a repository/runtime-specific performance claim.
Dispatch explanations distinguish `UNKNOWN` (no signal received) from
`STALE_OR_INVALID` (a received signal failed freshness/shape validation).

Acceptance: compatibility roundtrip, stale/future/oversize rejection, received-time
expiry, reconnect reset, capability rejection despite affinity, existing fairness
regression and real fixture Worker signal delivery. No extra scheduler or truth store.
