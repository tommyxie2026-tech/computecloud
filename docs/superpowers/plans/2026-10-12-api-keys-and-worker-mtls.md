# API Key Lifecycle and Worker mTLS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace indefinitely configured user secrets with revocable, expiring API keys and require an independently revocable mTLS identity for each Worker without breaking user gRPC clients.

**Architecture:** Add a Server-owned SQLite API key registry and management endpoints guarded by a bootstrap administrator scope. Keep static identities during a documented migration window. On gRPC, TLS verifies an optional Worker client certificate; the application interceptor requires a matching certificate only when the presented application identity is a Worker, so existing user gRPC clients remain usable.

**Tech Stack:** Go, gRPC TLS credentials, `crypto/rand`, SQLite schema migrations, HTTP API, existing `config.Identity` and `rpcutil.Auth`.

**Spec:** `docs/superpowers/specs/2026-10-12-public-access-security-design.md`

## Global Constraints

- A user API key and a Worker credential are distinct identity classes and cannot authenticate as one another.
- Store only a high-entropy key digest and non-sensitive prefix; return the plaintext once at creation.
- Key creation, listing, rotation, expiry, and revocation must be auditable; list/revoke responses never include plaintext or digest.
- Existing file-backed identities remain available during a bounded migration window; do not silently disable them or turn off mTLS enforcement without explicit configuration.
- Worker certificates identify exactly one configured Worker via URI SAN `spiffe://computecloud/workers/{worker_id}`; Common Name alone is not an identity binding.
- 7443 also supports user gRPC clients; TLS must allow user connections without a client certificate while requiring verified certificates for Worker principals.

## Review Focus

- API key management endpoints must never return the stored digest or reveal whether an unknown key prefix exists; test both cases.
- Revocation must take effect on the next request without Server restart; test with an already-connected HTTP and gRPC client.
- Expired API key and revoked Worker certificate must fail closed after clock boundary/rotation; test exact boundary behavior.
- Worker principal without a certificate, wrong URI SAN, wrong Worker ID, untrusted CA, and expired cert must fail; a user gRPC principal without a certificate must continue to work.
- Migration from schema v18 and rollback backup must retain existing tasks and recoverable key state; test migration on a copied v18 fixture and integrity checks.

---

### Task 1: Add the API key schema and repository

**Files:**
- Modify: `internal/store/db.go`
- Modify: `internal/store/migrations.go`
- Create: `internal/store/api_keys.go`
- Create: `internal/store/api_keys_test.go`
- Modify: `internal/store/release_upgrade_test.go`

**Interfaces:**
- Add Server schema v19 table `api_keys` with primary key ID, digest, prefix, owner, JSON project/scope/credential lists, label, created/expires/revoked timestamps; unique index on digest and lookup indexes on owner/status.
- Produce `CreateAPIKey(ctx, record)`, `LookupAPIKeyByDigest(ctx, digest, now)`, `ListAPIKeys(ctx, owner)`, and `RevokeAPIKey(ctx, owner, id, now)` on the store.
- `CreateAPIKey` rejects malformed scope/project lists and invalid expiration; `LookupAPIKeyByDigest` treats revoked and expired keys identically as not found.

- [x] **Step 1: Write failing tests** named `TestServerV19APIMigration`, `TestAPIKeyCreateAndLookup`, `TestAPIKeyExpiredAndRevokedNotFound`, `TestAPIKeyRevokeRequiresOwner`, and `TestAPIKeyListOmitsDigest`.
- [x] **Step 2: Run** `go test ./internal/store -run 'Test(ServerV19|APIKey)' -count=1`; initial compile failed because the v19 repository was absent.
- [x] **Step 3: Implement** the v19 migration, increment `ServerSchema` target from 18 to 19, and implement owner-filtered repository methods using transactions.
- [x] **Step 4: Run** `go test ./internal/store` and verify SQLite `integrity_check` and `foreign_key_check` in the migration test.
- [ ] **Step 5: Commit** as `feat: persist revocable API key metadata`.

### Task 2: Authenticate managed API keys and add management endpoints

**Files:**
- Modify: `internal/rpcutil/rpc.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/http.go`
- Create: `internal/server/api_keys.go`
- Create: `internal/server/api_keys_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/jobs.go`

**Interfaces:**
- Extend `rpcutil.Auth` with a store-backed resolver while retaining config-file identity lookup; dynamic API key lookup is by digest and uses the current time on every authentication.
- Add `api_keys:manage` scope to the validated scope registry. Only configured non-Worker bootstrap identities with this scope can create/list/revoke keys.
- Expose `POST /v1/auth/keys`, `GET /v1/auth/keys`, and `DELETE /v1/auth/keys/{id}`. Create accepts label, projects, scopes, credentials, and expiry; response shows plaintext once plus ID/prefix/expiry. List and revoke never return plaintext.
- Generate 32 random bytes using `crypto/rand`; encode with a versioned `cc_live_` prefix; hash the full key before persistence.

- [ ] **Step 1: Write failing tests** named `TestAPIKeyManagementRequiresManageScope`, `TestCreateAPIKeyReturnsPlaintextOnce`, `TestAPIKeyAuthenticatesWithoutRestart`, `TestRevokedAPIKeyRejectedImmediately`, and `TestAPIKeyCannotEscalateProjectsOrScopes`.
- [ ] **Step 2: Run** `go test ./internal/server ./internal/rpcutil -run 'Test(APIKey|CreateAPIKey)' -count=1`; confirm failure.
- [ ] **Step 3: Implement** the dynamic resolver and management handlers. Validate requested scopes against an explicit allowlist; a manager may grant only projects/scopes it already holds unless it is a designated bootstrap administrator.
- [ ] **Step 4: Run** `go test ./internal/server ./internal/rpcutil ./internal/config` and `go test ./...`.
- [ ] **Step 5: Commit** as `feat: manage expiring user API keys`.

### Task 3: Add optional verified Worker client certificates

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/rpcutil/rpc.go`
- Modify: `internal/rpcutil/rpc_test.go`
- Modify: `internal/worker/worker.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/transport_test.go`

**Interfaces:**
- Add Server TLS fields `client_ca_file` and `worker_mtls_mode` (`disabled`, `optional`, `required`); production examples set `required`, migration config can use `optional`.
- Worker/client TLS uses existing `cert_file` and `key_file` to load a client certificate when configured.
- Configure gRPC TLS as `VerifyClientCertIfGiven` when Worker mTLS is enabled; certificate chains are verified against the configured client CA.
- In `rpcutil.Auth.Context`, if the resolved principal is a Worker and mTLS mode is enabled, require a TLS peer certificate whose URI SAN exactly matches `spiffe://computecloud/workers/{WorkerID}`. Reject client certificates on user identities unless explicitly configured for user mTLS in a later design.

- [ ] **Step 1: Write failing tests** named `TestWorkerRequiresVerifiedClientCertificate`, `TestWorkerCertificateMustMatchIdentity`, `TestUserGRPCCanConnectWithoutClientCertificate`, `TestWorkerCertificateRejectsUnknownCA`, and `TestWorkerCertificateRejectsExpiredCertificate`.
- [ ] **Step 2: Run** `go test ./internal/rpcutil ./internal/server -run 'Test(WorkerRequires|WorkerCertificate|UserGRPCCan)' -count=1`; confirm failure.
- [ ] **Step 3: Implement** TLS CA loading, optional client certificate requests, client cert loading in `rpcutil.Dial`, and URI SAN binding in the auth context.
- [ ] **Step 4: Run** `go test ./internal/rpcutil ./internal/server ./internal/worker`, `go test -race ./internal/rpcutil ./internal/server`, and `go test ./...`.
- [ ] **Step 5: Commit** as `feat: authenticate workers with per-node mTLS`.

### Task 4: Add issuance, rotation, rollback, and migration procedures

**Files:**
- Modify: `cmd/computecloud/main.go`
- Create: `cmd/computecloud/keys.go`
- Modify: `docs/deployment/container-images.md`
- Modify: `docs/deployment/production-v0.4.7.md`
- Modify: `examples/server.yaml`
- Modify: `examples/worker.yaml`
- Create: `scripts/security/verify_worker_mtls.py`

- [ ] **Step 1: Write failing command/config tests** for key bootstrap, required mTLS validation, missing cert/key pairs, and malformed/unknown mode.
- [ ] **Step 2: Run** `go test ./cmd/computecloud ./internal/config -run 'Test(Key|MTLS)' -count=1`; confirm expected tests fail.
- [ ] **Step 3: Implement** an offline bootstrap command that creates the first manager key without printing secrets to logs, and document online manager API key lifecycle thereafter. Document a Worker-by-Worker mTLS rollout with overlap, rollback, and final `required` mode.
- [ ] **Step 4: Add** a test-only certificate verification utility that reports pass/fail and Worker ID but never prints private key or token material.
- [ ] **Step 5: Run** `go test ./...`, `go test -race ./internal/server ./internal/rpcutil`, script tests, and `git diff --check`.
- [ ] **Step 6: Commit** as `docs: add API key and worker certificate lifecycle`.

---

## Completion Gate

Do not mark complete until a v18 database upgrades to v19 without task loss, managed key create/use/revoke works without restart, each Worker certificate can be independently revoked, user gRPC remains compatible, and a target-host rollout/rollback has been exercised with real A/B certificates. CI proves protocol implementation, not production CA issuance or secret-store policy.
