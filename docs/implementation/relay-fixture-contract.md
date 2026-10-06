# Experimental Relay fixture contract

RLY-2 builds a memory-only rendezvous and bounded byte-forwarding component on
RLY-1. It stores no execution records and never decodes inner gRPC/TLS traffic.
Each pair requires two individually signed tickets, one per role, with matching
pair/server/worker/connection-epoch identities, random nonce and at most 60s TTL.
Replay state is bounded and retained until ticket expiry. A signature authenticates
routing permission only; inner Server TLS and Worker bearer identity remain
mandatory and authoritative.

The fixture uses an explicit operator-provisioned signing key of at least 32 bytes.
It is not device enrollment, a production ticket distribution service, or OIDC.
Outer TLS protects rendezvous tickets. Registration is bounded; pair timeouts,
connection limits, idle timeouts and bounded forwarding buffers fail closed.
Cancellation or either stream failure closes both sockets; no bytes are persisted
or retried. Direct remains the production default. Automatic fallback, control/bulk
priority and real NAT/restart/long-task evidence remain separate RLY-3–5 gates.

## Operator fixture

`go run ./cmd/relay-fixture -listen 127.0.0.1:7445 -cert relay.pem -key relay.key
-ticket-key-file relay-signing.key` runs the explicit fixture. Generate the signing
key from at least 32 random bytes and restrict its file permissions to 0600. Startup
reports a public random `relay_epoch`; issue server/worker tickets with the same
pair, identities, connection epoch and current Relay epoch using `-mint-ticket`.
Each printed ticket is a short-lived secret and must be delivered out of band.
Never put tickets in URLs or share them in logs. Restart invalidates all old tickets.

The fixture binary is not included in stable release packaging and is not wired
into Worker defaults. `internal/relay.Connect` returns an opaque connection for the
RLY-1 connector/listener seam; callers must still use inner gRPC/TLS. Tests use an
actual outer TLS broker and a second end-to-end TLS handshake through it, plus
signature/expiry/replay/identity/quota/restart and cancellation checks. This is local
fixture evidence; real NAT and unattended credential distribution remain unverified.
