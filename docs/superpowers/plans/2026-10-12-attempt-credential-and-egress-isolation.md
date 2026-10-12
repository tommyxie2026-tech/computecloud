# Attempt Credential and Egress Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure a public task cannot read persistent Worker/model credentials or use an unrestricted Worker network path, and allow public untrusted tasks only when the requested OCI isolation is actually active.

**Architecture:** Reuse the existing per-Attempt Model Gateway token and budget path as the narrow model credential boundary; extend it only for Agent runtime execution, not as a general model-serving product. Enforce container environment selection for public execution, keep Worker host credentials outside Attempt mounts, and require deployment-level egress filtering plus real-host negative tests.

**Tech Stack:** Go, existing protobuf `GatewayAccess`, Agent adapters, Worker runtime preparation, `internal/agenthttp` OCI controller, Docker/OCI, deployment docs and acceptance scripts.

**Spec:** `docs/superpowers/specs/2026-10-12-public-access-security-design.md`

## Global Constraints

- Do not expand computecloud into a general-purpose LLM Gateway or Model Serving Platform; any model proxy path is scoped to an active Attempt, its configured route, and its existing Runtime token/cost budget.
- Public untrusted jobs fail closed unless they use the `container` EnvironmentProvider; never fall back to host `process` execution.
- Attempt containers never receive Worker API tokens, Worker mTLS keys, personal CLI login state, Docker socket, Server TLS private keys, or credentials for other attempts.
- A model credential usable by an Attempt is bound to the active Attempt/lease, route, project, model allowlist, budget, and deadline; it expires/revokes with that Attempt.
- Security claims about filesystem/network isolation require target-host negative tests against the pinned image digest and OCI runtime.

## Review Focus

- Existing Codex Gateway envelopes are bound to attempt lease and model route; tests must prove a token cannot be reused after lease expiry, release, route change, or budget exhaustion.
- Claude or other runtimes that cannot use the Attempt-scoped credential boundary must be rejected in public-strict mode rather than silently using `credential_env` host secrets; add a capability test.
- Agent CLI config files may persist credentials under HOME even when environment variables are filtered; inspect and test actual created HOME/mounts for absence of secret files.
- Docker argument validation must reject workspace path traversal, unexpected bind mounts, privilege flags, host networking, and a request-controlled image; add negative tests.
- Egress tests must prove both model-route success and denial of metadata/host/private/unapproved public destinations on the target Worker; a unit test or CI fixture is not sufficient.

---

### Task 1: Make public execution policy reject process and unsafe credential modes

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/jobs.go`
- Modify: `internal/server/jobs.go`
- Modify: `internal/environment/execution.go`
- Modify: `internal/worker/execute.go`
- Modify: matching tests in `internal/config`, `internal/server`, and `internal/worker`

**Interfaces:**
- Add `server.public_execution_mode` with `trusted_only` and `isolated_only`; `isolated_only` requires each submitted spec to declare an allowed container Environment capability and each eligible Worker to advertise that capability.
- Add a validated runtime credential mode per profile: `attempt_gateway` or `dedicated_worker_account`. `isolated_only` rejects host `process` and rejects a profile configured only with a persistent personal HOME credential.
- The scheduler filters ineligible Workers before assignment; the Worker repeats the check before preparing a workspace/runtime and reports a stable `PUBLIC_ISOLATION_REQUIRED`/`CREDENTIAL_MODE_UNSAFE` error.

- [x] **Step 1: Write tests** for process rejection, Gateway requirement, Worker-side fail-closed validation, and trusted-mode compatibility. Filtering is verified by the scheduler's additional capability requirements.
- [x] **Step 2: Run** focused tests; the new tests passed after implementation.
- [x] **Step 3: Implement** strict-mode config validation, scheduler capability fencing, Worker-side pre-workspace checks, and `PUBLIC_ISOLATION_REQUIRED` errors.
- [ ] **Step 4: Run** targeted tests, `go test -race ./internal/server ./internal/worker`, and `go test ./...`.
- [ ] **Step 5: Commit** as `feat: require isolated environments for public jobs`.

### Task 2: Extend Attempt-scoped model credential proxy to supported runtimes

**Files:**
- Modify: `api/agent/v1/runtime.proto`
- Modify: generated `api/agent/v1/runtime.pb.go` and `runtime_grpc.pb.go` using the repository's protobuf generation target
- Modify: `internal/server/gateway.go`
- Modify: `internal/server/gateway_stream.go`
- Modify: `internal/adapter/execution.go`
- Modify: `internal/adapter/http_runtime.go`
- Modify: `internal/adapter/adapter.go`
- Modify: `internal/worker/execute.go`
- Modify: `internal/agenthttp/server.go`
- Modify: corresponding adapter, gateway, worker, and agenthttp tests

**Interfaces:**
- Preserve the existing Attempt-bound Gateway token; add explicit provider protocol metadata only if required by supported Codex/Claude CLI integration. The Server remains the holder of upstream model keys.
- Supported runtime adapters receive only a short-lived Attempt token and configured gateway URL; direct `CredentialEnv` injection is omitted when Gateway mode is selected.
- Gateway validates attempt ID/lease, active state, owner/project, route digest, allowed model, current budget, and deadline on every request; no standalone user key can invoke this path.
- Runtimes without a proven gateway-compatible authentication mode are rejected under `isolated_only` and may run only under an explicit trusted single-tenant policy.

- [ ] **Step 1: Add contract tests** for the real Codex and Claude CLI auth/base-URL behavior using pinned CLI versions in a credential-free fixture; capture no user credential or network call. Document the supported integration matrix before changing protocol fields.
- [ ] **Step 2: Add failing Go tests** named `TestAttemptGatewayTokenExpiresWithLease`, `TestAttemptGatewayRejectsReleasedAttempt`, `TestAttemptGatewayRejectsRouteAndModelMismatch`, `TestAttemptGatewayEnforcesBudget`, and `TestStrictModeRejectsRuntimeWithoutGatewayAuth`.
- [ ] **Step 3: Run** `go test ./internal/server ./internal/adapter ./internal/worker ./internal/agenthttp -run 'TestAttemptGateway|TestStrictModeRejectsRuntime' -count=1`; verify uncovered cases fail.
- [ ] **Step 4: Implement** only the runtime protocol path proven by Step 1; preserve existing Codex behavior and fail closed for unsupported providers. Regenerate protobuf files using the checked-in project target if the contract changes.
- [ ] **Step 5: Run** `go test ./internal/server ./internal/adapter ./internal/worker ./internal/agenthttp`, `go test -race ./internal/server ./internal/worker`, and `go test ./...`.
- [ ] **Step 6: Commit** as `feat: scope runtime model access to active attempts`.

### Task 3: Harden the OCI Attempt boundary and secret file lifecycle

**Files:**
- Modify: `internal/agenthttp/server.go`
- Modify: `internal/agenthttp/server_test.go`
- Modify: `cmd/computecloud-agent-http/main.go`
- Modify: `internal/worker/execute.go`
- Modify: `internal/worker/runtime_execution.go`
- Modify: `internal/adapter/execution.go`
- Modify: `internal/adapter/runtime_v2_test.go`

**Interfaces:**
- Add explicit OCI policy fields for seccomp profile, CPU/memory/PID limits, read-only root, no-new-privileges, non-root UID, tmpfs, and a named egress network; values are operator configuration and never task-controlled.
- Add flags/config validation in `cmd/computecloud-agent-http/main.go`; strict public mode requires a named operator-created egress network whose host firewall permits only the Server model endpoint and approved dependency endpoints.
- Create temporary env/config files with mode `0600` inside the private state directory, pass only the minimum Attempt token/settings, and remove them after runtime completion and controller recovery.
- Reject Docker parameters with host networking, privileged mode, added capabilities, devices, arbitrary binds, image overrides, or paths outside the canonical workspace.
- Keep Docker daemon access restricted to the trusted Agent Runtime controller; add startup validation that the configured executable/socket is not exposed through the public API.

- [ ] **Step 1: Write remaining failing tests** for privilege overrides, arbitrary binds, and Worker secret mounts. Credential-file mode/location, stale-file restart, and strict network/seccomp tests are covered.
- [x] **Step 2: Run** focused controller tests after implementing the file lifecycle; all passed.
- [x] **Step 3: Implement** operator-owned OCI network/seccomp/resource settings, canonical workspace bind validation, private state-dir credential files, and stale file cleanup at controller recovery. Host egress rules still require target configuration.
- [x] **Step 4: Run** full Go suite and Python egress tool tests; `go test -race ./internal/agenthttp` passed. Server/Worker race suite passed after P2 Task 1.
- [ ] **Step 5: Commit** as `feat: harden per-attempt OCI and credential boundaries`.

### Task 4: Add Worker egress controls and real-host negative acceptance

**Files:**
- Modify: `docs/deployment/container-images.md`
- Modify: `docs/deployment/production-v0.4.7.md`
- Modify: `docs/validation/v0.4.7-integration-acceptance.md`
- Create: `scripts/security/worker_egress_acceptance.py`
- Create: `scripts/security/worker_egress_acceptance_test.py`
- Create: `scripts/security/validate_worker_runtime_config.py`

- [ ] **Step 1: Write failing config tests** proving unsafe `process` environment, personal Codex HOME mounts, unrestricted host bridge, public Docker socket, and unpinned Runtime image are rejected by the public-production validator.
- [ ] **Step 2: Run** `python3 -m unittest scripts.security.worker_egress_acceptance_test` and confirm unsafe fixture rejection tests fail.
- [ ] **Step 3: Implement** a deployment contract requiring a dedicated Worker HOME, no personal profile mount, no Worker Docker socket, pinned Attempt image, enforced host/network egress policy, and explicit allowlisted model endpoint.
- [x] **Step 4: Implement** a target-host egress acceptance tool with harmless probes for the explicitly supplied allowed model endpoint, metadata, host gateway, private test destination, and unapproved public endpoint. It never scans arbitrary networks and stores labels/status only.
- [ ] **Step 5: Run** Python fixture tests, `go test ./...`, then execute target-host ENV01-ENV05 plus egress cases on the pinned candidate image and retain only sanitized hashes/results.
- [ ] **Step 6: Commit** as `test: gate public workers on egress and runtime isolation`.

### Task 5: Establish public tenant admission and operational rollback

**Files:**
- Modify: `docs/deployment/production-v0.4.7.md`
- Modify: `docs/implementation/v0.4.7-stable-tracker.md`
- Modify: `docs/validation/v0.4.7-integration-acceptance.md`

- [ ] **Step 1: Write acceptance checklist** that distinguishes CI fixture proof, target-host OCI proof, model credential isolation proof, and public-edge proof.
- [ ] **Step 2: Verify** the checklist rejects release evidence that has no candidate SHA, image digest, platform, Attempt ID, secret-scrubbed output, or negative network test.
- [ ] **Step 3: Document** staged rollout: trusted single-tenant first; mTLS and public strict mode enabled per Worker; canary with budgets; expand only after no-egress-bypass and credential-canary checks; rollback restores binaries/data while revoked keys/certs remain revoked.
- [ ] **Step 4: Run** repository documentation/validation scripts, `go test ./...`, `git diff --check`, and validate the acceptance report schema against a sanitized fixture.
- [ ] **Step 5: Commit** as `docs: gate public tenants on runtime security evidence`.

---

## Completion Gate

Do not mark complete until public-strict mode cannot schedule `process` execution, all public-supported runtimes use the Attempt-scoped credential route, Attempt containers cannot read persistent Worker credentials, outbound policy denies all non-allowlisted destinations on the target host, and ENV/egress acceptance records prove the exact candidate image digest and platform. A CI fixture is not sufficient evidence for target-host isolation.
