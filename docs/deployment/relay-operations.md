# Experimental Relay operations

This runbook operates the explicit `direct_then_relay` transport of the
Agent-aware Distributed Job Execution Platform / Agent Job Executor. Relay is
an opaque inner-TLS byte forwarder. It does not own Job state, inspect prompts
or Artifacts, cache execution data, or replace Server recovery.

## Enablement and rollback

Keep `direct` as the default. Enable `direct_then_relay` only for named Server
and Worker identities after the outer TLS CA/SAN, issuer HTTPS origin, private
token files and Relay address have been reviewed. Token and signing-key files
must be regular files with mode `0600`; all configured paths are absolute.

Rollback by draining new submissions, switching Server and Workers back to
`direct`, and restarting them with the last known direct configuration. Existing
Relay sockets may be closed after Worker state and command inboxes reconcile.
Never replay StartAttempt at the Relay layer and never lower a database schema
version during rollback.

## Credential rotation

1. Generate a new ticket signing key and distinct Server/Worker issuer tokens.
2. Write new files with mode `0600` outside data directories and record hashes,
   never their contents.
3. Drain Relay connections, restart the fixture with the new files, then restart
   Server/Workers with matching issuer tokens and CA configuration.
4. Confirm old tickets fail because the Relay epoch/key changed, new Workers
   reconnect, and no duplicate Attempt is created.
5. Remove old files only after direct rollback remains available.

## Metrics and alarms

`relay-fixture --metrics-interval=30s` emits fixed-cardinality structured
snapshots and one final snapshot at shutdown. Snapshots contain numbers only:

- `active_connections`, `accepted_pairs`
- `bytes_in`, `bytes_out`, `backpressure_total`
- `rejected_invalid`, `rejected_replay`, `rejected_capacity`
- `rejected_quota`, `rejected_timeout`, `rejected_not_ready`

Server/Worker direct-first connectors separately expose direct successes,
fallback attempts and failed connections. Metrics never contain identities,
tickets, tokens, addresses, prompts or Artifact payloads.

Alert when capacity/quota/timeout rejection counters increase continuously,
when fallback failures rise after direct failure, when active connections do
not return to zero after drain, or when backpressure rises while control RPC
latency breaches its operational objective. Treat invalid/replay increases as
a security investigation. Counters reset on Relay restart; correlate them with
the Relay boot epoch and process start time.

## Restart and fault handling

A Relay restart invalidates outstanding tickets and closes paired sockets.
Server and Worker must obtain a fresh one-use pair, complete inner TLS and
identity authentication, and reconcile durable command state. A disconnect is
not success, failure or cancellation. Preserve UNKNOWN/UNVERIFIABLE and cleanup
semantics until Server/Worker evidence resolves the Attempt.

For overload, reduce new pairing demand or return to `direct`. Do not raise
connection, Worker-pair or byte-rate bounds without recording memory, file
descriptor, RTT and control-latency evidence.

## Real NAT acceptance

Use two independently identified Linux Workers and a separately reachable
Relay endpoint. Record exact candidate SHA, Relay/Server/Worker binary hashes,
CA fingerprints, configuration mode and boot epoch without recording secrets.
Run and retain evidence for:

1. direct success without Relay;
2. forced direct failure followed by Relay fallback;
3. control/lease/cancel while a Bulk Artifact stream is active;
4. network loss and recovery with event-watermark continuity;
5. Relay restart and fresh ticket acquisition;
6. a long-running Job across reconnect;
7. zero duplicate Attempts, stale-generation writes and invalid Artifacts.

Loopback, one-host containers and GitHub Actions fixtures do not satisfy this
acceptance. Keep Relay experimental and opt-in until the sanitized real-network
report passes the v0.4.7 stable Gate. The fixed `RLY01`–`RLY05` fields and
fail-closed command are defined in
[`../validation/v0.4.7-performance-results.md`](../validation/v0.4.7-performance-results.md).
