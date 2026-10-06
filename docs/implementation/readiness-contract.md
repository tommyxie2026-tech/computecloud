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
improve suitability and cannot override hard filters. No scoring is enabled.

Acceptance: compatibility roundtrip, stale/future/oversize rejection, received-time
expiry, reconnect reset, capability rejection despite affinity, existing fairness
regression and real fixture Worker signal delivery. No extra scheduler or truth store.
