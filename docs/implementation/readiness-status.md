# WS-C readiness implementation

Status: implemented locally; remote CI/merge pending (2026-10-06).
Contract: [readiness-contract.md](readiness-contract.md).

- Backward-compatible optional Renew/WorkerStatus ExecutionSignal.
- Worker bounded recent preparation and successful Environment activation samples,
  Runtime names, repository/template hints, free slots and observed/measured times.
- Server validates size, range, clock skew and both observation/receipt expiry;
  invalid hints do not block lease renewal. Session replacement starts unknown.
- Scheduler records bounded candidate explanations and selected Worker in the
  same transaction as Attempt creation. No scoring, filter, fairness or ownership change.
- Rejected candidates get no affinity credit. Cross-project candidate identities
  are redacted; evidence does not expose unrelated repository/template lists.
- Independent contract/explainability/integration CI gates required by packaging.

Validation: full Server/Worker/readiness race regression PASS; dedicated contract,
fairness/explainability PASS; real local Server + two fixture Worker processes +
ten complete Jobs PASS, including fresh signal delivery and scheduler evidence.
No real model calls. No SQLite migration. Environment/readiness remains advisory.

Remaining: remote Linux/full CI and real deployment observation. Score/affinity
optimization belongs to v0.5 and is not enabled by these hints.
