# Claude Attempt Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow supported Claude HTTP Runtime Attempts to use a bounded Anthropic Messages gateway with a short-lived Attempt token, without exposing the long-lived Anthropic key to Worker or task processes.

**Architecture:** Extend the existing model gateway with a separate, explicit Anthropic protocol path and backend; reuse Attempt identity, frozen route digest, lease, accounting, audit and redaction primitives. Add Claude capability to strict execution only after end-to-end local contract tests pass; never fall back to persistent Worker credentials.

**Tech Stack:** Go `net/http`, existing SQLite-backed gateway accounting, config/YAML, Anthropic Messages JSON/SSE, fixed-version Claude CLI contract fixture, Go tests and Python CI contract tests.

**Spec:** `docs/superpowers/specs/2026-10-15-claude-attempt-gateway-design.md`

## Global Constraints

- Keep the feature opt-in and specific to Anthropic Messages; do not build a generic LLM Gateway or Model Serving Platform.
- Long-lived Anthropic keys remain server-side and are never sent to Worker, Attempt environment files, task mounts, DB request blobs, logs or error bodies.
- Attempt tokens are short-lived, one-Attempt scoped, hash-validated against the active lease and frozen route digest, and revocable on cancellation/expiry.
- Strict mode must fail closed for missing capability, unsupported CLI/protocol, unknown route, invalid budget, unknown settlement, or unsafe credential mode.
- Upstream TLS verification, bounded request/response sizes, bounded concurrency and explicit upstream/model allowlists are mandatory.
- Never log prompts, raw Messages bodies, raw model responses, Authorization, Anthropic keys or Attempt tokens.
- App-level usage/cost budgets are estimates/accounting controls, not supplier invoice hard caps.

## Review Focus

- Duplicate/invalid authorization headers, invalid Anthropic version headers, unsupported query strings/endpoints and malformed JSON must be rejected before upstream contact.
- Streaming disconnect, partial SSE, upstream error, cancellation and retry must not double-spend or silently treat unknown usage as zero.
- Route digest, model, Attempt generation, lease expiry/revocation and project mismatch must all reject before upstream contact.
- Supplier headers/body must not override upstream host, credentials, proxy, TLS trust or redirect behavior.
- All errors and logs must remain bounded and secret/prompt-free even when the upstream returns malformed or oversized content.

---

### Task 1: Model an explicit Anthropic Gateway route and strict validation

**Files:**
- Modify: `internal/config/jobs.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/gateway_cli_test.go`
- Modify: config gateway tests (create a focused test file if needed)

**Interfaces:**
- Add a route backend discriminator for the Anthropic Messages upstream while preserving existing `http` and `codex_cli` behavior.
- Anthropic route has fixed HTTPS `base_url`, `api_key_file`, explicit `allowed_models`, positive max inflight, positive per-Attempt token/cost budgets and configured input/output prices when strict mode is enabled.
- Add route digest coverage for backend, endpoint, key identity reference (never key value), model allowlist, protocol version and limits.
- Reject URL credentials/query/fragment, non-HTTPS upstream except explicitly approved loopback tests, incomplete budgets and duplicate/conflicting route configuration.

- [x] **Step 1: Add failing config tests** for valid Anthropic route; HTTP downgrade; URL query/userinfo; missing key file; empty model allowlist; absent/partial budgets; and route digest changes when protocol/model/limit changes.
- [x] **Step 2: Run** focused config tests; expected validation failures were confirmed before implementation.
- [x] **Step 3: Implement** typed route validation and digest fields without altering existing route defaults.
- [x] **Step 4: Run** focused config tests and `go test ./internal/config`.
- [x] **Step 5: Commit** `ce2d434 feat: configure Anthropic Attempt Gateway routes`.

### Task 2: Add Anthropic Messages request admission and non-streaming proxy

**Files:**
- Create: `internal/server/gateway_anthropic.go`
- Create: `internal/server/gateway_anthropic_test.go`
- Modify: `internal/server/gateway.go`
- Modify: `internal/server/gateway_validation.go`
- Modify: `internal/server/server.go` only if route mounting requires it

**Interfaces:**
- Produce an isolated handler for `POST /v1/messages`; no wildcard or arbitrary upstream paths.
- Accept only one valid Bearer Attempt token for the strict path; reject duplicate auth, unsupported endpoints and unsafe content/headers. Permit `?beta=true` only if required by the pinned CLI contract; reject every other query value.
- Validate Attempt lease/generation/revocation, frozen route digest, project, credential reference and allowed model before contacting the upstream.
- Rewrite upstream auth to the server-held Anthropic key; construct the fixed upstream URL from config, disable redirects, verify TLS, apply request/header/body/time limits and strip hop-by-hop/sensitive headers.
- Parse Anthropic request fields sufficiently to enforce model, stream, max_tokens, and bounded body policy; reject unsupported fields/endpoints rather than silently routing them.
- Reuse accounting persistence where compatible; add schema only if a test proves current `gateway_requests` fields cannot represent the endpoint/usage lifecycle.

- [x] **Step 1: Write failing `httptest` cases** for valid non-streaming flow, authentication/query rejection, model/size failures, redirect and no-contact denials.
- [x] **Step 2: Run** focused Anthropic Gateway tests; expected failures were confirmed before implementation.
- [x] **Step 3: Implement** bounded proxy and server-side secret replacement.
- [x] **Step 4: Run** focused Anthropic Gateway tests and config tests. Full Server package remains a separate slow suite and was interrupted after no output; see validation notes.
- [x] **Step 5: Commit** (combined with Task 3 as `ccff51a feat: add bounded Anthropic Attempt Gateway`).

### Task 3: Add streaming, usage settlement, token/cost budgets and cancellation

**Files:**
- Create/modify: `internal/server/gateway_anthropic_stream.go`
- Modify: `internal/server/gateway_budget.go`
- Modify: `internal/server/gateway_stream.go` only for genuinely shared primitives
- Modify: `internal/server/gateway_budget_test.go`
- Modify: `internal/server/gateway_anthropic_test.go`

**Interfaces:**
- Support the fixed CLI contract’s SSE event set with bounded line/event sizes, idle timeout and response duration; preserve only protocol-required fields.
- Enforce input/output token and estimated cost budgets before upstream dispatch using conservative input reservation and maximum output clamping.
- Settle reported Anthropic usage exactly once; upstream errors, client disconnect, malformed SSE or missing final usage transition the request to an explicit incomplete/unknown state and fail closed for subsequent calls when budget cannot be proven.
- Propagate request cancellation to upstream, prevent unbounded goroutines/buffers, and avoid transparent retries after any upstream bytes or uncertain billing.

- [x] **Step 1: Add failing tests** for SSE settlement, budget clamp, partial/missing usage, malformed and oversized events, and cancellation.
- [x] **Step 2: Run** focused tests; expected failures were confirmed before implementation.
- [x] **Step 3: Implement** bounded streaming/accounting states and deterministic failure semantics.
- [x] **Step 4: Run** targeted tests plus `go test -race ./internal/server -run 'AnthropicGateway|GatewayBudget' -count=1`.
- [x] **Step 5: Commit** (combined with Task 2 as `ccff51a feat: add bounded Anthropic Attempt Gateway`).

### Task 4: Bind Claude HTTP Attempt credentials and strict capability

**Files:**
- Modify: `internal/server/gateway.go` (Attempt route binding/token scope)
- Modify: `internal/adapter/http_runtime.go`
- Modify: `internal/adapter/runtime_v2_test.go`
- Modify: `internal/adapter/http_runtime_test.go`
- Modify: `internal/worker/public_execution.go`
- Modify: `internal/worker/execute.go`
- Modify: corresponding `internal/server/public_execution.go` and tests

**Interfaces:**
- Claude HTTP strict capability is advertised only when the Worker runtime version and gateway config support this exact Anthropic contract.
- Assignment contains only Gateway base URL/route and Attempt token; `ANTHROPIC_BASE_URL` points to the fixed Gateway route and `ANTHROPIC_AUTH_TOKEN` carries only the Attempt token.
- No direct `ANTHROPIC_API_KEY`, `credential_env`, personal `~/.claude`, or fallback path is allowed in `isolated_only`.
- Missing/unsupported profile or credential mode is rejected by scheduler and rechecked by Worker before workspace creation.

- [x] **Step 1: Add failing tests** for version/capability negotiation, secret override removal, HTTPS-only endpoint and protocol-specific scheduling.
- [x] **Step 2: Run** targeted tests; compile/assertion failures confirmed before implementation.
- [x] **Step 3: Implement** Attempt token injection and strict negotiation while preserving trusted-only Claude behavior.
- [x] **Step 4: Run** adapter/config/Agent HTTP package tests, focused Server strict-scheduling/Anthropic tests, and Worker tests excluding its process-recovery test. The excluded process-recovery test fails in this sandbox with `operation not permitted`; the full Server package suite was interrupted after running without output. Focused Server integration and race tests pass.
- [x] **Step 5: Commit** `d2e09fa feat: enable Claude Attempt credentials in strict execution`.

### Task 5: Pin real CLI contract and verify redaction/egress contract

**Files:**
- Modify: `scripts/ci/agent_cli_gateway_contract.js`
- Modify: matching contract test/fixture files discovered in the existing CI workflow
- Modify: `scripts/security/worker_egress_acceptance.py` and tests only as required by the Attempt route
- Modify: `docs/implementation/agent-http-runtime.md`
- Modify: `docs/deployment/codex-cli-gateway.md` or create a focused Claude Gateway deployment guide
- Modify: `docs/implementation/public-access-security-tracker.md`

**Interfaces:**
- Contract fixture records locked Claude CLI version, required endpoint/path, headers, request schema, stream events and usage fields; fixture uses no live supplier secret.
- Redaction tests inspect logs/errors/DB evidence for canary secrets and prompt strings.
- Egress acceptance proves only the explicit Gateway path is reachable from the Attempt network and direct supplier/arbitrary public destinations are denied on target hosts.
- Documentation clearly distinguishes CI mock success, real supplier contract, application budget and supplier billing hard limit.

- [x] **Step 1: Add failing contract fixtures** for CLI path/query, Bearer auth, protocol version, Beta Header and body-field allowlists, plus redaction canaries for responses and persisted evidence.
- [x] **Step 2: Run** the contract and redaction suites; CLI contract testing exposed an unhandled Beta Header, which was explicitly reviewed and added to the allowlist and forwarding behavior.
- [x] **Step 3: Add** the narrow Messages protocol contract and CI assertions without enabling count_tokens or arbitrary Anthropic endpoints.
- [ ] **Step 4: Run** targeted Go tests, full Go tests, `go test -race ./internal/server ./internal/adapter ./internal/worker ./internal/agenthttp`, Python security tests, contract scripts, and `git diff --check`.
- [x] **Step 5: Document** operational key placement/rotation and target-host checks using placeholders only.
- [x] **Step 6: Commit** the protocol contract and strict-gate tests (`fe823c9`, `1d99c9a`). Full-suite and fixed-image CI validation remain pending in Step 4.

### Task 6: End-to-end local strict-mode acceptance

**Files:**
- Verify: Tasks 1–5 outputs
- Evidence: local sanitized test report, no real secret values

- [x] Run a local strict Server scheduling + mocked Anthropic upstream integration with a synthetic pinned Worker capability, and run the CLI protocol fixture using temporary home/config directories. This is fixture-only, not a complete OCI Worker or pinned-version execution.
- [ ] Verify success, budget exhaustion, expired/revoked Attempt, model denial, cancellation, Worker loss, Server restart and malformed stream behavior end-to-end. Existing Gateway regression tests cover success, budget clamp/unknown usage, model denial, lease expiry, route digest, cancellation and malformed streams, but Worker loss/Server restart are not yet exercised through a real Claude CLI/OCI runtime.
- [x] Verify with fixture tests that provider-key/Attempt-token canaries do not appear in Gateway errors or persisted Gateway evidence, and that Claude direct API-key/environment overrides are removed. Target-container file and egress negatives remain pending.
- [ ] Record exact commit, image/runtime digest, platform and test types; label all mock checks as CI/local fixtures.
- [ ] Leave real supplier and production target-host validation pending; do not change online configs in this plan. Existing dual-Worker task records prove execution on both hosts for an already-deployed baseline only; they do not validate this Gateway change.

---

## Completion Gate

Do not enable Claude in `isolated_only` or mark public tenant support complete until the pinned CLI contract, budget/streaming tests, strict scheduler/Worker checks, target-host credential-access and egress negatives, exact image digest and real low-budget supplier verification pass. This plan does not authorize online rollout, key rotation, or disabling the current trusted-only Claude deployment.
