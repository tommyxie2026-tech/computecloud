# Worker Deployment Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a verifiable hardening profile for Worker and Attempt containers, with explicit resource and egress contracts and no claim that static config proves target-host isolation.

**Architecture:** Extend the existing deployment and security validation path rather than adding an orchestration subsystem. Separate Worker service-container checks from the existing Agent Runtime controller checks; keep strict Attempt OCI controls in the existing controller and make host firewall acceptance an explicitly separate target-host step.

**Tech Stack:** Go config/runtime already in the repository, Python 3 validator/fixtures, Docker inspect JSON, Chinese deployment docs.

**Spec:** `docs/superpowers/specs/worker-deployment-hardening-design.md`

## Global Constraints

- Keep public task execution in `trusted_only` unless the full Attempt credential boundary is enabled and verified.
- Do not mount a personal full Codex/Claude home, SSH agent, Docker socket, or unrelated host path into a Worker task boundary.
- Require non-root, read-only root filesystem, drop-all capabilities, no-new-privileges, finite CPU/memory/PID limits, and bounded logs in deployment examples.
- Strict Attempt containers must use an operator-created named egress network; built-in Docker networks remain rejected.
- Static validation must not claim that host firewall, OCI escape resistance, or target-host credential isolation has been proven.
- Never emit credential values, Authorization headers, prompts, raw model responses, or full personal paths in evidence.

## Review Focus

- Missing, zero, negative, or malformed CPU/memory/PID/log limits must fail validation.
- Root UID, privileged mode, added capabilities, writable personal-home mounts, Docker socket or host networking must fail validation.
- A custom named Docker network in inspect output is not proof that host firewall egress is restricted; acceptance output must keep the claims separate.
- Linux bind ownership and macOS Docker Desktop path translation must not be conflated; examples need explicit host-specific notes.
- Missing Worker ID, image digest, platform, or config digest must not produce a target-host pass record.

---

### Task 1: Add a Worker service-container inspect validator

**Files:**
- Create: `scripts/security/validate_worker_container.py`
- Create: `scripts/security/validate_worker_container_test.py`
- Reference: `scripts/security/validate_worker_runtime_config.py` for the separate Runtime controller contract

**Interfaces:**
- Produce `validate(inspect: dict, expected: dict) -> list[str]` for one Worker service container.
- CLI accepts `--docker`, `--container`, and a protected expected-policy JSON file; success prints a JSON result with `passed` and `errors`, never mount source values or environment contents.
- Require non-root `Config.User`; `HostConfig.ReadonlyRootfs`; `HostConfig.CapDrop` containing `ALL`; `HostConfig.SecurityOpt` containing `no-new-privileges`; finite positive `HostConfig.Memory`, `NanoCpus`, and `PidsLimit`; and no privileged mode, added capabilities, published ports, Docker socket or host network.
- Evaluate mounts against explicit policy: reject personal home and unapproved host mounts; allow only named service data/runtime paths with their required read-only state.

- [x] **Step 1: Write failing fixtures** for a valid Worker inspect, root user, writable personal Codex mount, Docker socket, missing limits, unlimited PID, privileged/capability additions, published port, and host network.
- [x] **Step 2: Run** `python3 -m unittest scripts.security.validate_worker_container_test`; confirm insecure fixtures fail for the expected reasons.
- [x] **Step 3: Implement** the validator with bounded/sanitized error labels and strict type/range checks.
- [x] **Step 4: Run** the focused suite and `python3 -m py_compile scripts/security/validate_worker_container.py`.
- [x] **Step 5: Commit** `feat: validate hardened worker container policy`.

### Task 2: Add hardened deployment examples and policy checks

**Files:**
- Create: `examples/deployment/worker-hardening-policy.json`
- Create: `examples/deployment/worker-hardening.compose.yaml`
- Create: `examples/deployment/server-hardening.compose.yaml`
- Modify: `docs/deployment/production-v0.4.8.md`
- Modify: `docs/deployment/v0.4.8-ubuntu-nginx-public-worker.md`
- Test: `scripts/security/validate_worker_container_test.py`

**Interfaces:**
- Compose examples are operator-reviewed overrides; Server retains UID 65532 and loopback-only 17443/17444 publication, while Worker uses a dedicated service UID/GID and no published port. Both set `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, finite CPU/memory/PID limits, bounded log rotation, and only documented mounts.
- Resource values are labeled as examples to tune from host capacity; policy file defines which values must be finite without embedding secrets.
- Documentation distinguishes trusted direct-host CLI credentials from strict Attempt Gateway credentials and states that direct persistent credential paths remain trusted-single-tenant only.

- [x] **Step 1: Add policy fixtures** and failing deployment-policy tests for omitted hardening fields, public Server bindings, personal Worker home mounts, and secret-shaped policy values.
- [x] **Step 2: Run** the focused Python test and verify it fails on the missing Server example.
- [x] **Step 3: Add** Server and Worker overrides/policy with dedicated runtime-home paths; retain loopback-only Server ports, and do not include real usernames, credentials, or actual API keys.
- [x] **Step 4: Document** Linux and macOS Docker mount/UID differences, resource tuning, upgrade/rollback cautions, and trusted-only limitations.
- [x] **Step 5: Run** the validator fixture suite, example check, and `git diff --check`.
- [ ] **Step 6: Commit** `docs: add hardened worker deployment profile`.

### Task 3: Separate static checks from host egress and isolation evidence

**Files:**
- Modify: `scripts/security/worker_egress_acceptance.py`
- Modify: `scripts/security/worker_egress_acceptance_test.py`
- Modify: `scripts/security/validate_worker_runtime_config.py`
- Modify: `docs/implementation/agent-http-runtime.md`
- Modify: `docs/validation/public-access-security-acceptance.md`

**Interfaces:**
- Existing egress probes remain explicit-target-only and non-scanning.
- Extend the existing evidence contract (which already requires Worker ID, platform, image digests, and `target_host`) with a policy/config digest and separate fields for static config checks and target-host egress checks; do not add a second competing Worker identity schema.
- Static config success alone can never set target-host egress or credential-isolation success.
- Evidence output contains no secret values, prompts, Authorization headers, or personal-home paths.

- [x] **Step 1: Add failing validator tests** for missing policy/config digest and attempts to claim host egress success using static-only evidence; retain existing Worker ID/platform/image digest requirements.
- [x] **Step 2: Run** focused validator tests; new assertions failed before implementation.
- [x] **Step 3: Implement** separate evidence fields and output redaction while preserving explicit allow/deny probes.
- [x] **Step 4: Update** docs to make target-host evidence a release gate and explain what CI fixtures do not prove.
- [x] **Step 5: Run** all relevant security Python tests, `python3 -m py_compile` for changed scripts, and `git diff --check`.
- [ ] **Step 6: Commit** `test: distinguish worker config and host isolation evidence`.

### Task 4: Verify the complete deployment-hardening contract

**Files:**
- Verify: Tasks 1–3 outputs

- [x] Run all relevant Worker/container/evidence Python tests (23 tests passed).
- [x] Validate both Compose examples with Ruby YAML and `docker compose config`; inspect-validator fixtures pass.
- [x] Run `git diff --check`; unrelated pre-existing files were not staged or rewritten.
- [x] Record target-host checks as pending until separately executed on each Worker; do not mark this plan’s host-isolation acceptance complete from CI.

---

## Completion Gate

The local deployment package is complete when all static config tests pass and examples are clear. Production hardening is not complete until each actual Worker has separate target-host evidence for its exact image/config digest, resource limits, credential-access negatives, allowed model/SCM connectivity, and denied egress destinations. Existing dual-Worker task records confirm scheduling and execution across two hosts only; they do not satisfy these host-isolation checks. This plan does not authorize changing live production containers.
