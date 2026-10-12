# Public Ingress and Resource Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Protect the public HTTPS/gRPC entry points from credential guessing, resource exhaustion, and abusive authenticated requests while preserving public access for legitimate clients.

**Architecture:** Apply bounded per-credential request limits in the Server after authentication, enforce submit/model/worker concurrency and queue budgets at the resource-owning services, and put externally hosted DDoS/WAF controls in the deployment layer. The Server remains authoritative; forwarded IP headers are not used as identity.

**Tech Stack:** Go, `net/http`, gRPC interceptors, SQLite-backed existing job/gateway state, YAML config, Docker deployment documentation.

**Spec:** `docs/superpowers/specs/2026-10-12-public-access-security-design.md`

## Global Constraints

- Keep the product as an Agent-aware Distributed Job Execution Platform / Agent Job Executor.
- Preserve public Internet reachability for 7443 and 7444; security controls must authenticate, authorize, meter, and bound access rather than depend on hiding ports.
- Do not trust `X-Forwarded-For` unless the request arrived from an explicitly trusted proxy; rate limits are keyed by authenticated credential/identity.
- Reject overload explicitly; never create unbounded queues, goroutines, request bodies, or retained limiter state.
- Do not log Authorization values, prompt contents, model keys, or raw upstream responses.

## Review Focus

- Many API keys belonging to one user must not share an unexpectedly restrictive rate bucket; add a test with two independent keys.
- Spoofed forwarding headers must not evade identity limits; add a test varying `X-Forwarded-For` on one credential.
- Server restart or limiter-map churn must not permit unbounded memory growth; add a bounded-state/eviction test.
- Streaming API calls must be limited at admission without being cut off by an unrelated short request deadline; add a streaming regression test.
- Saturated project, identity, model-route, or Worker capacity must reject new work without changing idempotent replay semantics; add submit/replay tests.

---

### Task 1: Add bounded authenticated request limiting

**Files:**
- Create: `internal/server/rate_limit.go`
- Create: `internal/server/rate_limit_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/http.go`
- Modify: `internal/config/config.go`
- Create: `internal/config/public_rate_limit_test.go`

**Interfaces:**
- Produce `type RequestLimit struct { RatePerMinute int; Burst int; MaxEntries int; IdleTimeoutSeconds int }` in config and a server-owned limiter keyed by SHA-256 of the presented credential plus a bounded route class.
- Produce `func (s *Server) allowRequest(key, class string, now time.Time) (allowed bool, retryAfterSeconds int)` with bounded entry count and deterministic eviction of expired buckets.
- HTTP rate limiting runs only after bearer authentication and before route handlers. It returns HTTP 429 and a `Retry-After` value calculated from the remaining token-bucket refill time.
- Managed `cc_live_` key lookup also has a separate bounded pre-auth token bucket keyed by the direct socket peer IP, before any SQLite resolver lookup. It ignores forwarding headers and protects the DB path when callers rotate unknown key values; the existing post-auth per-credential limiter remains in force.

- [x] **Step 1: Write failing tests** for `TestHTTPHandlerRateLimitPerCredential`, `TestHTTPHandlerRateLimitIgnoresForwardedFor`, `TestHTTPHandlerRateLimitEvictsIdleCredential`, and `TestRequestLimiterBoundsEntries`.
- [x] **Step 2: Run** `go test ./internal/server -run 'TestHTTPHandlerRateLimit|TestRequestLimiter' -count=1`; confirm the tests fail because the limiter is absent.
- [x] **Step 3: Implement** a token-bucket limiter with a fixed maximum entry count, cleanup on admission, config validation, and middleware placement after `s.auth.Bearer`.
- [x] **Step 4: Run** the same targeted tests and `go test ./internal/config ./internal/server`.
- [x] **Step 5: Commit** as `feat: limit authenticated public API request rates`.

### Task 1a: Bound managed API-key lookups before authentication

- [x] Add a regression test proving rotated unknown `cc_live_` tokens from one direct IP cannot each trigger a resolver lookup, and that spoofed `X-Forwarded-For` does not split the bucket.
- [x] Apply the separate bounded limiter before bearer resolution on the standard HTTPS API, model gateway, and Agent conversation HTTP entry points; preserve the post-auth per-credential limiter.
- [x] Focused regression and race tests passed locally; PR head `57e75ab` CI run [38219776567](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38219776567) passed 30/30 jobs, including Linux/macOS full verify.

### Task 1b: Apply managed API-key lookup limits to gRPC

- [x] Run the same bounded direct-peer-IP limiter before managed-key resolution in unary and streaming gRPC interceptors; share the HTTP source budget and ignore forwarding metadata.
- [x] Add a regression test that rotates unknown managed keys from one peer and proves the resolver is called only once before `ResourceExhausted`.
- [ ] Run CI verification for this gRPC extension.

### Task 2: Enforce identity/project workload budgets

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/jobs.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/jobs.go`
- Modify: `internal/server/gateway.go`
- Modify: `internal/server/gateway_stream.go`
- Modify: matching tests in `internal/server` and `internal/config`

**Interfaces:**
- Add an optional `Limits` block to user identity configuration with explicit maximum active/queued jobs and model requests; omitted limits resolve to documented secure defaults.
- All admission checks use the authenticated principal and project, and return typed resource-exhausted errors before inserting a Job or starting an upstream request.
- Existing global project limits, per-route model limits, stable idempotency keys, and retry behavior remain in force.

- [x] **Step 1: Write failing tests** named `TestSubmitJobEnforcesIdentityActiveAndQueueLimits`, `TestSubmitJobLimitPreservesIdempotentReplay`, and `TestModelGatewayEnforcesPerIdentityInflightLimit`.
- [x] **Step 2: Run** `go test ./internal/server -run 'TestSubmitJobEnforcesIdentity|TestSubmitJobLimitPreserves|TestModelGatewayEnforcesPerIdentity' -count=1`; sandbox loopback was denied, then the authorized local integration run passed after implementation.
- [x] **Step 3: Implement** validated config limits and admission checks at Job submission and model-gateway request creation; do not rely on the Gateway alone.
- [x] **Step 4: Run** `go test ./internal/config ./internal/server` and `go test ./...`.
- [x] **Step 5: Commit** as `feat: enforce per-identity execution budgets` (`0fcb5c5`).

### Task 3: Bound Worker gRPC admission and expose safe overload signals

**Files:**
- Modify: `internal/rpcutil/rpc.go`
- Modify: `internal/server/server.go`
- Modify: `internal/config/config.go`
- Modify: `internal/rpcutil/rpc_test.go`
- Modify: `internal/server/transport_test.go`

**Interfaces:**
- Add configurable maximum concurrent authenticated Worker sessions and RPC stream admissions; limits are applied after TLS and application authentication.
- Reject excess Worker registration/session replacement and stream admission with `codes.ResourceExhausted`; do not terminate healthy existing sessions merely because new capacity is full.

- [x] **Step 1: Write failing tests** named `TestWorkerSessionAdmissionLimit` and `TestWorkerStreamAdmissionLimit`.
- [x] **Step 2: Run** `go test ./internal/server ./internal/rpcutil -run 'TestWorker(Session|Stream)AdmissionLimit' -count=1` (both passed; loopback permission was granted for the integration package).
- [x] **Step 3: Implement** bounded admission counters with release on every stream/session exit path and safe counter cleanup.
- [x] **Step 4: Run** the targeted tests, targeted race checks, and `go test ./...`.
- [x] **Step 5: Commit** as `feat: bound public worker RPC admission`.

### Task 4: Document and verify the public edge deployment contract

**Files:**
- Modify: `docs/deployment/container-images.md`
- Modify: `docs/deployment/production-v0.4.7.md`
- Create: `scripts/check_public_edge_config.py`
- Create: `scripts/check_public_edge_config_test.py`

- [x] **Step 1: Write failing checks** for examples that publish only 7443/7444, expose no Docker socket/metrics/database ports, set TLS, and place 7444 behind the documented HTTP Gateway while 7443 uses a TCP/TLS pass-through path.
- [x] **Step 2: Run** `python3 -m unittest scripts.check_public_edge_config_test` (5 checks pass, including mutated insecure fixtures).
- [x] **Step 3: Update** deployment guidance for source protection, origin/backend bypass prevention, trusted-proxy boundaries, DDoS/WAF duties, 429 handling, health-check exposure, and safe logs.
- [x] **Step 4: Run** `python3 -m unittest scripts.check_public_edge_config_test`, `go test ./...`, and `git diff --check`.
- [x] **Step 5: Commit** as `docs: define hardened public edge deployment`.

### Task 5: Verify adversarial resource-control behavior

**Files:**
- Create: `scripts/security/public_ingress_smoke.py`
- Create: `scripts/security/public_ingress_smoke_test.py`
- Modify: `docs/deployment/production-v0.4.7.md`

- [x] **Step 1: Write tests** for unauthenticated rejection, per-key 429 behavior, oversized request rejection, saturated queue rejection, and absence of secrets in returned errors/log fixtures.
- [x] **Step 2: Run** `python3 -m unittest scripts.security.public_ingress_smoke_test` (mocked endpoint cases pass).
- [x] **Step 3: Implement** a safe smoke tool that accepts an explicitly supplied test endpoint and test key from protected files/environment, never prints credentials, and never targets arbitrary networks by default.
- [x] **Step 4: Run** unit fixtures, `go test ./...`, targeted `go test -race ./internal/server ./internal/rpcutil`, and `git diff --check`.
- [x] **Step 5: Commit** as `test: verify public ingress security controls`.

---

## Completion Gate

Do not call this plan complete until all five tasks pass, public deployment docs show the required edge topology, and a target deployment records an external test showing the expected public ports only, denied source-origin bypass, rate-limit response, and no exposed operations ports. CI alone proves code behavior, not cloud security group or WAF state.
