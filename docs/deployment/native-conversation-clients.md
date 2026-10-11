# Native Claude and Codex clients

This experimental interface submits a normal single Agent Job through a native text conversation endpoint. Keep the feature disabled until an operator configures a profile, protected API Key file, and tested Worker runtime. Never place upstream model credentials in the client API Key file.

## Fixed Server profile

The following is a structural example. Replace every value in angle brackets, ensure the execution owner is already authorized for the project and credential, and use a digest generated from the Server's trusted Job template configuration.

```yaml
server:
  jobs:
    enabled: true
    # Existing trusted Job templates remain required.
  conversation_jobs:
    enabled: true
    max_request_bytes: 262144
    # Optional. Plain HTTP listener; must be a literal loopback address.
    loopback_listen: 127.0.0.1:8081
    profiles:
      - id: remote-codex
        public_model: remote-codex
        execution_owner: job-runner
        project_id: engineering
        workspace:
          repository_ref: application
          base_commit: <40-or-64-hex-commit>
        execution:
          engine: codex
          runtime_profile: codex_exec
          model: <worker-allowed-model>
          credential_ref: <server-side-credential-reference>
          policy_ref: <trusted-policy>
          acceptance_profile: <trusted-acceptance-profile>
        limits:
          timeout_seconds: 1800
          max_attempts_per_task: 1
        max_input_bytes: 240000
        max_output_tokens: 16384
        max_output_bytes: 1048576
        max_active: 4
  users:
    # The execution identity has Job scopes, a fixed project, and credential access.
    - owner: job-runner
      token_file: <restricted-path>
      projects: [engineering]
      credentials: [<server-side-credential-reference>]
      scopes: [jobs:submit, jobs:read, jobs:cancel]
    # Client-facing identity has no general jobs or model gateway permissions.
    - owner: claude-codex-client
      token_file: <restricted-path>
      projects: [engineering]
      conversation_profile: remote-codex
      scopes: [conversations:submit, conversations:read, conversations:cancel]
```

The profile must match an existing configured Job template and fixed execution identity. The conversation-facing Key is a high-entropy random token (at least 256 bits), stored in a file readable only by the Server service account. The configuration loader does not mint keys. Provision the token through the existing secure operator process; do not copy it into this document, shell history, a URL, or logs.

Client-declared `max_tokens` / `max_output_tokens` values above the profile limit are clamped to the Server profile limit. Claude Code may send a default of 32000 even when the profile is lower; the fixed profile remains authoritative. This value is not enforced as a Runtime token/cost limit. `max_output_bytes` is the enforced response size bound; truncation is UTF-8 safe and reported. For actual token/cost limits, configure the existing Worker Runtime enforcement independently.

## SSH loopback tunnel

On the Server host, keep the listener on `127.0.0.1:8081`. From the client host, open a local forward after verifying the Server's SSH host key through a trusted channel:

```sh
ssh -N -L 18080:127.0.0.1:8081 <server-user>@<server-host>
```

Keep `StrictHostKeyChecking` enabled and install the expected host key in the client's `known_hosts`. Do not bind the local forward to `0.0.0.0`. The conversation bearer Key travels inside the SSH-encrypted connection; the application listener accepts only loopback peers. Run SSH as a supervised process and close the tunnel when it is no longer needed.

Claude-compatible clients that support an Anthropic base URL can use `http://127.0.0.1:18080/agent` and the conversation Key as their Anthropic API Key. The client appends `/v1/messages`.

Codex-compatible clients that support the OpenAI Responses base URL can use `http://127.0.0.1:18080/agent/v1` and the same conversation Key as their OpenAI API Key. The client appends `/responses`.

For a direct encrypted connection, use `https://<server-name>/agent` (Claude) or `https://<server-name>/agent/v1` (Codex), configure the trusted private CA where needed, and retain normal hostname verification. The Server conversation route rejects TLS below 1.3 even when another existing HTTP route accepts TLS 1.2.

## Lifecycle and validation

Responses include `X-ComputeCloud-Request-ID` and `X-ComputeCloud-Job-ID`. Use `GET /agent/v1/requests/{request_id}` to inspect status, measured Job usage and result. `POST /agent/v1/requests/{request_id}/cancel` requests cancellation and returns after the Job cancellation is accepted; the Job becomes terminal only after Worker cleanup.

`Idempotency-Key` makes client retries explicit. Without it, the canonical request digest is the replay key; submitting the exact same request again will return its previous Job. Supply a new key when a deliberate repeat execution is intended. A network disconnect, including an interrupted SSE stream, stops observation but leaves the durable Job running.

Only text Messages and Responses inputs are supported. Tool schemas are ignored; forced tool selection, tool result/function call inputs, media, previous-response state and unsupported fields are rejected. Claude Code compatibility metadata (`context_management`, `output_config`, and `thinking`) is accepted and ignored: the Server profile remains authoritative for execution model, strategy, and limits. Text `system` messages in the Messages array are retained as conversation context. No real Claude/Codex account is needed for fixture CI. Live CLI validation against separately configured client installations and two independent hosts remains an operator acceptance step.
