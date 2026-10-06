# WS-B cache implementation evidence

Status: PR #93 foundation merged to main; concurrent-hit improvement under validation. 2026-10-06.
Contract: [workspace-cache-contract.md](workspace-cache-contract.md).

## Implemented

- Root-scoped, cancellation-aware file lock across independent provider instances.
- Single validation under the prepare/materialize lock; content tampering fails closed.
- Darwin clonefile / Linux reflink with independent-copy fallback; no writable hardlinks.
- Atomic deletion tombstones, restart cleanup, LRU retention / byte budget,
  per-pass deletion limit, and separate Attempt lifecycle ownership.
- Optional 0–2 warm slots, digest validation before ownership transfer, independent
  writable copies, warm-slot cleanup with template GC.
- Durable workspace.prepared event: fingerprint, hit/miss, prepare/materialize
  duration, strategy; no local paths or credentials.
- Dedicated cache/benchmark CI job included in package dependencies.
- Shared validation/materialization lock for existing immutable hits without warm
  slots; exclusive lock retained for misses, warm-slot transfer and GC.

## Local evidence

macOS arm64, Go 1.26.0, deterministic fixtures, no real model calls:

- workspace/worker/config race tests PASS.
- Existing prepared-workspace contract and recovery gates PASS.
- go vet ./... PASS.
- Provider benchmark: 128 tracked dependency files, 2 MiB payload; 10 cold samples,
  10 cached preparations with 9 hits (90%), 9 warm latency samples.
- Observed cold P50/P95: 80.388 / 83.400 ms.
- Observed cached P50/P95: 43.239 / 45.533 ms.

These are one-machine provider results, not ten complete Jobs, production SLOs,
or signed mobile / real Runtime evidence. Cached P50 remains above the original
40%-of-cold aspirational target; P95 is below its 60% target in this sample.
The gate records measurements without masking unsuccessful correctness tests.

## Remaining acceptance

- Linux and full integration CI; dependency-heavy and concurrent throughput tuning.
- Warm-slot replenishment currently adds synchronous preparation work; its full
  Job impact must be measured before recommending nonzero warm slots.
- Misses, warm-slot transfers and GC still serialize. Concurrent hits now run
  under a shared root lock, preserving GC exclusion and digest validation.
- Readiness signal / scheduler observation is WS-C, separate from this patch.

## Complete Job fixture acceptance

Ten consecutive Jobs after single/MapReduce warmup: PASS; reuse 100%,
Job P50 1016.976 ms / P95 1066.261 ms.
One real Server process and two Worker processes on one Darwin arm64 host,
no real model calls. This is not the independent-host production baseline.

## Concurrent-hit follow-up (local, 2026-10-06)

The benchmark now prepares ten simultaneous, distinct cached Attempts using ten
provider instances; all ten must hit and produce valid independent workspaces.
On the same Darwin arm64 host, a pre-change sample took 443.888 ms wall time
(concurrent P95 443.755 ms). Two post-change samples took 161.831 and 156.738 ms
wall time (P95 161.612 and 155.394 ms). These small fixture samples show reduced
lock queueing, not a production throughput guarantee. The latest sequential
sample was cold P50 80.595 ms and cached P50 40.612 ms (50.4% of cold), still
above the 40% target. Linux, dependency-heavy real work and independent-host
Job acceptance remain open.
