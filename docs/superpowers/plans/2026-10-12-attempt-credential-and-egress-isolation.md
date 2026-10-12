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
- [x] **Step 4: Run** targeted tests, `go test -race ./internal/server ./internal/worker`, and `go test ./...` (Linux/macOS CI run [38210717417](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38210717417), 30/30 jobs passed).
- [x] **Step 5: Commit** as `feat: require isolated environments for public jobs` (`44ce52b`).

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

- [x] **Step 1: Add pinned CLI contract tests** in `scripts/ci/agent_cli_gateway_contract.js`. CI run [38234526922](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38234526922) passed with Codex 0.160.1 and Claude Code 2.1.292 in a `--network none` container; both hit only the local fixture using fixture-only credentials. The test observes Codex `POST /v1/responses` with Bearer auth and Claude `POST /v1/messages?beta=true` with Bearer auth. It proves CLI configuration behavior, not production upstream or Agent Runtime support.

#### 固定版 CLI 能力矩阵（2026-10-12）

| Runtime | 固定版 CLI 合约证据 | 当前严格模式状态 |
|---|---|---|
| Codex HTTP | 0.160.1 使用配置的 OpenAI Responses `base_url`，并从 `env_key` 发送 Attempt Bearer token；本地 mock CI 通过。 | 支持 Attempt Gateway。 |
| Claude HTTP | 2.1.292 使用 `ANTHROPIC_BASE_URL` 与 `ANTHROPIC_AUTH_TOKEN`，发送 Anthropic Messages API 请求（含 `?beta=true`）；本地 mock CI 通过。 | 仍 fail-closed：当前 Server Gateway 暴露 Responses API，没有 Anthropic Messages 路由/协议适配；CLI 能连接通用网关不等于当前产品路径已受支持。不得在 `isolated_only` 注入持久 Worker 凭据。 |
- [x] **Step 2: Add Gateway/strict-mode regression coverage.** The existing consolidated tests cover Attempt lease expiry and cancellation/revocation, route digest and model allowlist mismatch, token/cost budget exhaustion, and strict-mode rejection of Claude/direct credentials. Coverage lives in `TestGatewayWorkerInvocationAndSecretRedaction`, `TestPublicStrictModeRequiresAttemptGateway`, `TestWorkerFailsClosedBeforeWorkspaceForUnsafeCredentialMode`, and `TestClaudeHTTPRejectsAttemptGateway`; no separate duplicate test functions were needed.
- [x] **Step 3: Run focused regression tests.** `go test ./internal/server ./internal/adapter ./internal/worker ./internal/agenthttp -run 'Test(GatewayWorkerInvocationAndSecretRedaction|PublicStrictMode|CodexHTTPAdapterUsesAttemptGatewayWithoutDirectCredential|ClaudeHTTPRejectsAttemptGateway|WorkerFailsClosedBeforeWorkspaceForUnsafeCredentialMode|IsolatedRuntimeDoesNotInheritHostNetworkOverrides|RunLifecycleIdempotencyAndRestartFence|AttemptRestartReconcilesSecretFiles|PublicStrictOCI)' -count=1` passed for all four packages on 2026-10-12. This validates the current implementation, not actual pinned Codex/Claude CLI binaries.
- [x] **Step 4: Implement** Codex HTTP Attempt Gateway support using its existing Codex provider config and token, and fail closed for Claude HTTP. No protobuf change was needed.
- [x] **Step 5: Run** targeted/full Go tests and race suites for adapter/Worker and server/Worker; all passed.
- [x] **Step 6: Commit** as `09b97a0 feat: scope OCI Codex model access to attempts`.

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

- [x] **Step 1: Add OCI boundary regression coverage.** `TestRunLifecycleIdempotencyAndRestartFence` proves request-controlled privilege/network/bind/Docker-socket arguments remain after the image boundary and cannot become Docker options; credential file mode/location and stale-file restart are covered by the controller secret lifecycle tests.
- [x] **Follow-up regression:** `TestPublicStrictOCIRejectsBuiltInNetworks` first failed because strict mode accepted Docker `host` and `none`; strict validation now rejects all built-in networks, and focused plus full `internal/agenthttp` tests pass.
- [x] **Follow-up network identity check:** strict startup resolves the configured Docker network to its actual name, rejecting network-ID aliases that resolve to built-in `host`; `TestPublicStrictOCIRejectsNetworkIDResolvingToHost` covers the rejection and named-network acceptance.
- [x] **Follow-up egress review:** strict Attempt execution no longer inherits host proxy variables or TLS root overrides; trusted execution preserves prior proxy behavior. `TestIsolatedRuntimeDoesNotInheritHostNetworkOverrides` covers both modes.
- [x] **Step 2: Run** focused controller tests after implementing the file lifecycle; all passed.
- [x] **Step 3: Implement** operator-owned OCI network/seccomp/resource settings, canonical workspace bind validation, private state-dir credential files, and stale file cleanup at controller recovery. Host egress rules still require target configuration.
- [x] **Step 4: Run** full Go suite and Python egress tool tests; `go test -race ./internal/agenthttp` passed. Server/Worker race suite passed after P2 Task 1.
- [x] **Step 5: Commit** as `17f0a5e feat: harden per-attempt OCI runtime boundaries`.

### Task 4: Add Worker egress controls and real-host negative acceptance

**Files:**
- Modify: `docs/deployment/container-images.md`
- Modify: `docs/deployment/production-v0.4.7.md`
- Modify: `docs/validation/v0.4.7-integration-acceptance.md`
- Create: `scripts/security/worker_egress_acceptance.py`
- Create: `scripts/security/worker_egress_acceptance_test.py`
- Create: `scripts/security/validate_worker_runtime_config.py`

- [x] **Step 1: Add tests** for unsafe process/credential admission, personal HOME mount, default bridge, public TCP port, and unpinned controller image.
- [x] **Step 2: Run** `python3 -m unittest scripts.security.worker_egress_acceptance_test scripts.security.validate_worker_runtime_config_test`; all five fixtures passed.
- [x] **Step 3: Implement** a deployment contract requiring a dedicated Runtime HOME, no personal profile mount, pinned Runtime image, no public TCP port, named egress network, and explicit model endpoint. Host firewall policy remains operator-owned.
- [x] **Step 4: Implement** a target-host egress acceptance tool with harmless probes for the explicitly supplied allowed model endpoint, metadata, host gateway, private test destination, and unapproved public endpoint. It never scans arbitrary networks and stores labels/status only.
- [ ] **Step 5: Run** target-host ENV01-ENV05 plus egress cases on the pinned candidate image and retain only sanitized hashes/results. Python fixtures and full Go suite passed locally; target host is unavailable in this environment.
- [x] **Step 6: Commit** as `09b97a0` (egress tool and controller validator landed with Codex Gateway integration).

### Task 5: Establish public tenant admission and operational rollback

**Files:**
- Modify: `docs/deployment/production-v0.4.7.md`
- Modify: `docs/implementation/v0.4.7-stable-tracker.md`
- Modify: `docs/validation/v0.4.7-integration-acceptance.md`

- [x] **Step 1: Write acceptance checklist** distinguishing CI fixture proof, target-host OCI proof, Attempt credential isolation, and public-edge proof.
- [x] **Step 2: Verify** the validator rejects missing SHA/digests/platform/Attempt, fixture-only evidence, missing network negatives, and credential-shaped data.
- [x] **Step 3: Document** staged rollout: trusted single-tenant first; mTLS and public strict mode per Worker; low-quota canary; expand only after egress and revocation checks; rollback keeps revoked keys/certs revoked.
- [x] **Step 4: Run** repository security Python suites (21 tests), public-edge policy check, Python compile checks, and `git diff --check`. The full Go suite passed on this code revision.
- [x] **Step 5: Commit** as `4e1ae6e docs: gate public tenants on runtime security evidence`.

---

## Completion Gate

### 后续：strict 模式 Attempt 级 token 与估算费用上限

- [x] strict Gateway 路由必须设置正数 `max_attempt_tokens`，并纳入路由摘要和配置校验。
- [x] 按 Attempt 串行化模型请求、累计已结算 Gateway 用量；任何用量未结算时 fail closed。
- [x] 注入或收紧 `max_output_tokens`；拒绝不支持的 compact 请求和无法放入剩余额度的请求。
- [x] 增加路由输入/输出单价和每 Attempt 估算费用上限；逐次向上取整已完成请求的估算费用，并同时按 token 与费用余额限制输出。
- [x] 增加配置、预算计算与 Gateway 集成测试；本地聚焦测试通过。
- [x] 在 CI 运行完整 Go/race 与安全测试；最新 PR CI run [38234526922](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38234526922) 全部 30 个可见作业成功，包含固定版 CLI 合约及 Linux/macOS 全量验证。
- [x] 验证固定版本 Codex/Claude CLI 的 base URL 与认证头合同（仅本地无凭据 mock；不声称真实供应商协议/账单验收）。
- [ ] 在目标 Worker 验证 token 语义，并为不可信公网租户配置独立的供应商账单硬上限。

**边界：** token 与费用额度均按 Attempt 计算，重试会获得新额度。费用边界依据管理员配置的保守单价和供应商上报用量估算，不是供应商账单上限。请求体字节数用于保守估算输入 token；真实分词、单价及上游 `max_output_tokens` 行为仍须用锁定版本协议验收。不可信公网租户仍须有供应商侧支出硬上限。

Do not mark complete until public-strict mode cannot schedule `process` execution, all public-supported runtimes use the Attempt-scoped credential route, Attempt containers cannot read persistent Worker credentials, outbound policy denies all non-allowlisted destinations on the target host, and ENV/egress acceptance records prove the exact candidate image digest and platform. A CI fixture is not sufficient evidence for target-host isolation.
