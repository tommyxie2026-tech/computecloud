# WS-C readiness implementation

Status: readiness observer, validation, bounded decision evidence, and deterministic
`readiness-v1` scoring are merged on main (PR #141); `prepared_workspace_score_enabled`
remains opt-in and defaults off. Automated contract/explainability/flow CI has passed
for the merged implementation. Real deployment efficacy and performance remain
unverified pending a controlled target-environment pilot.
Contract: [readiness-contract.md](readiness-contract.md).

- Backward-compatible optional Renew/WorkerStatus ExecutionSignal.
- Worker bounded recent preparation and successful Environment activation samples,
  Runtime names, repository/template hints, free slots and observed/measured times.
- Server validates size, range, clock skew and both observation/receipt expiry;
  invalid hints do not block lease renewal. Session replacement starts unknown.
- Scheduler records bounded candidate explanations and selected Worker in the
  same transaction as Attempt creation. Opt-in `readiness-v1` scoring is applied
  only after hard filters; the default-off path retains least-active selection
  and tie rotation. Exact score and factors are included in bounded evidence.
- Rejected candidates get no affinity credit. Cross-project candidate identities
  are redacted; evidence does not expose unrelated repository/template lists.
- Missing readiness is reported as `UNKNOWN`; received but expired/malformed
  signals are reported as `STALE_OR_INVALID` and receive no signal score.
- Independent contract/explainability/integration CI gates required by packaging.

Validation: merged code includes Server/Worker/readiness race regression, dedicated
contract, fairness/explainability gates, and a local Server + two fixture Worker
processes + ten complete Jobs simulation, including fresh signal delivery and
scheduler evidence. No real model calls. No SQLite migration. Environment/readiness
remains advisory.

Remaining: real deployment observation and real performance evidence. Aggregate
preparation P50 is a coarse hint and is not
evidence of a repository- or Runtime-specific speedup. Local M1 microbenchmark
at 64 candidates measured ~0.95 µs/off and ~20.8 µs/on; this is diagnostic only,
not a CI budget or production performance claim.
