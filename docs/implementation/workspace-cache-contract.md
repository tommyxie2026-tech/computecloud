# WS-B cache and measurement contract

Status: proposed implementation contract, 2026-10-06. Builds on ADR-019.
Owner: workspace / worker integration. No public Job or protobuf change, no migration.

## API and ownership

LocalPreparedProvider keeps its existing PreparedProvider interface. A new
PrepareAttempt operation groups lookup, immutable template validation and
materialization under one root-scoped filesystem lock. All mutations, inspection,
and GC use that lock, including independent provider instances. Cancellation while
waiting for a lock does not acquire or mutate the cache.

Writable Attempt files are independent copies or filesystem copy-on-write clones;
hard links are forbidden. Copy-on-write failure falls back to copying. Template
digests are still checked before materialization. A cache hit is counted only when
an existing, valid template is used successfully.

Cache metadata is a rebuildable optimization, never Attempt ownership authority.
The manifest retains immutable identity and digest. A separate bounded access
record tracks last use; missing records fall back to manifest preparation time.
Unknown/malformed records fail closed and are never interpreted as deletion proof.

## GC

GC has positive retention, byte budget and per-pass deletion limits. It runs
under the same lock, so it cannot remove a template during prepare/materialize.
Materialized Attempt trees no longer reference templates. GC does not operate on
Attempt workspaces and does not change lifecycle/cleanup proof.

Deletion is an atomic rename to a deterministic tombstone followed by removal.
Restart resumes tombstones. Unknown directories and symlink roots are rejected or
left untouched. Expiration and byte pressure select least-recently-used templates.
Errors are surfaced; failure to meet the byte budget is observable.

## Measurements and acceptance

Worker emits workspace.prepared with template fingerprint, cache hit,
prepare/materialize durations and materialization strategy (no local paths).
A benchmark reports cold/warm P50/P95, sample count, OS/arch, fixture size and cache
hit ratio; worker-level measurements remain distinct from ten end-to-end Job tests.
Performance ratios are reported, not represented as certified production SLOs.

CI covers concurrent independent providers and GC, restart tombstones, corruption,
unknown metadata, cancellation, writable isolation, and worker event delivery.
Optional warm pools and readiness advertisement remain independent subsequent
slices; this contract does not pretend to deliver them.
