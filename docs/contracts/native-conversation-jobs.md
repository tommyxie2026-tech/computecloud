# Native conversation jobs (experimental)

The opt-in `/agent/v1` API adapts a bounded, text-only subset of Claude Messages and OpenAI Responses into durable ComputeCloud Worker Jobs. It is an Agent Job Executor interface; it is not a model gateway or a local client tool runner. General Job, MCP, and `/v1/responses` APIs keep their existing routes and authorization.

Each API Key is bound to one configured profile. The profile fixes the execution owner, project, repository and base commit, runtime, model, credential reference, trusted Job template, timeout, and active-request/input/output limits. Conversation-scoped identities cannot receive general `jobs:*` permissions. Client tool declarations are ignored as metadata; forced tool choices, tool results, executable tool calls, media, and stateful response references are rejected. Worker policy alone governs tools.

The server stores a durable request-to-Job mapping and normalized prompt in SQLite before submitting the Job so it can recover a submission after a retry. Treat the Server data directory and its backups as sensitive conversation data; restrict OS access and backup access accordingly. `Idempotency-Key` replays return the same request; reusing a key with different input is a conflict. When omitted, the canonical request digest is the replay key. A disconnect ends observation, not execution. Explicit cancellation uses the existing Job cancellation and cleanup fence. Conversation history is replayed as text on every turn; the API does not provide native provider-session resume.

The default is disabled. HTTPS on the existing Server listener requires normal certificate verification; the conversation handler accepts only TLS 1.3. An optional plaintext listener must bind to a literal loopback IP and serves only `/agent`; operators may expose it through SSH local forwarding with verified host keys. This setting does not relax Worker TLS. No task-write 0-RTT is supported. `max_output_bytes` truncates at a valid UTF-8 boundary and signals truncation with `X-ComputeCloud-Output-Truncated` on JSON responses or an `computecloud.output_truncated` SSE event. The native `max_tokens`/`max_output_tokens` input is clamped to the profile ceiling and is not a hard Runtime token or cost budget. No token counts are fabricated in native response bodies; measured Job usage is available from the request lookup endpoint.

## Supported protocol subset

- Claude: `POST /agent/v1/messages`; text `system` and `messages` only.
- Codex: `POST /agent/v1/responses`; text instructions/input only.
- Text response and SSE response; SSE emits keepalives while the Job runs, then delivers the complete answer (it does not relay incremental model tokens). Alias discovery, request lookup, and explicit cancellation.
- Unsupported model probes or protocol features return explicit capability errors; the server does not fabricate token counts, model identity, or tool results.

The listener and profile configuration are intentionally not enabled by default. See the deployment guide for current operator settings and acceptance status.
