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
or retried. Direct remains the production default. Control/bulk priority and real
NAT/restart/long-task evidence remain separate RLY-4–5 gates.

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

## Experimental ticket issuer

The fixture can optionally listen on a second TLS port with `-issuer-listen`,
`-issuer-server-id`, `-issuer-server-token-file`, and
`-issuer-worker-tokens-file`. All credential files must be private regular files;
the Worker token file is a JSON map from Worker ID to a distinct 32-byte-or-longer
Bearer token. The Server uses `POST /v1/relay/pairs` with a `worker_id` JSON body
to obtain only its own signed ticket. An authenticated Worker uses
`POST /v1/relay/pairs/claim` to retrieve only its matching ticket, once. The
issuer retains unclaimed Worker tickets only in memory until expiry. It never
returns the other role's ticket, stores no Job state, requires TLS, and sends
`Cache-Control: no-store` on authenticated responses. Restart changes the Relay
epoch and invalidates outstanding pairs.

Server and Worker can now opt in with `transport.mode: direct_then_relay`.
The Server issues a pair for each configured Worker without an active session,
then offers the paired connection to its existing gRPC service. The Worker first
tries the configured direct `server_address` and, when the TCP connection fails
or times out, claims its own ticket and connects through the Broker. Each retry
begins with direct. Inner Server TLS and the ordinary Worker bearer remain
authoritative. A TCP connection that succeeds but fails inner TLS or identity
authentication does not trigger fallback. The issuer is still an experimental
fixture, not a production Relay service.

Example Server and Worker fragments (each has its own private issuer token):

~~~yaml
server:
  transport:
    mode: direct_then_relay
    relay_address: relay.example.com:7445
    issuer_url: https://relay.example.com:7446
    token_file: /etc/computecloud/relay-server.token
    ca_file: /etc/computecloud/relay-ca.pem
worker:
  server_address: server.example.com:7443
  transport:
    mode: direct_then_relay
    relay_address: relay.example.com:7445
    issuer_url: https://relay.example.com:7446
    token_file: /etc/computecloud/relay-worker.token
    ca_file: /etc/computecloud/relay-ca.pem
    direct_timeout_ms: 1000
~~~

The two Relay endpoints must have the same hostname. `server_name` can pin an
alternate certificate name for their outer TLS; the configured Server TLS CA
continues to verify the inner gRPC connection. The issuer must know each
Server/Worker token before either process starts. No Relay fields are accepted
in default `direct` mode.

## Direct-first connector

`rpcutil.NewDirectFirstConnector` composes explicit direct and alternate byte
connectors with a bounded direct timeout and one fallback attempt. Caller
cancellation never falls back. TLS and bearer authentication remain above the
connector, so certificate/identity errors never trigger a second transport path.
Counters expose only bounded transport outcomes, with no business payloads.
Every reconnect starts with direct again; no concurrent duplicate live streams
are created by this state machine. Production ticket provisioning and Worker
configuration are explicit; default `rpcutil.Dial` does not opt in.

## Local Job recovery evidence

The Server/dual Worker test exercises signed rendezvous, inner TLS and existing
Worker identity through the byte connector. It interrupts a RUNNING four-second
fixture Job by restarting the Relay, then verifies successful completion with
exactly one durable Attempt and a subsequent map/reduce Job. Worker default `New`
remains direct unless the explicit transport mode is set; `NewWithConnector`
still requires TLS. This validates local transport recovery; it does not supply
production NAT evidence or control/event/bulk traffic priority.

`Server.ServeWithTunnel` accepts a direct listener and a listener of already
paired opaque Relay connections on the same gRPC Server. Both routes therefore
share Worker authentication, session replacement, Job/Attempt state, commands,
and Artifact ownership. The caller still has to provision tickets and feed the
tunnel listener. The explicit configured path supplies it; default direct
config does not opt in automatically.
