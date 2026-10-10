# Native Conversation Job Clients Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Claude Code/Codex use Server-issued Key and a native conversation endpoint to execute durable Worker Jobs, over TLS or loopback HTTP inside a verified SSH tunnel.

**Architecture:** Default-off `/agent` protocol adapters share frozen execution profiles and a durable request-to-single-Job service. They reuse Job scheduling, Worker execution, artifacts, authorization and recovery. A dedicated optional loopback-only HTTP listener permits tunnel deployments without changing Worker TLS.

**Tech Stack:** Go 1.26 module, net/http, crypto/tls/rand, existing SQLite store and Job engine, Python CI orchestration and existing Runtime fixtures.

**Spec:** [design spec](../specs/2026-10-11-native-conversation-jobs-design.md), approved in conversation on 2026-10-11 including the transport addendum.

## Global constraints and sequencing

- Do all implementation in the existing isolated worktree, on a new `feat/native-conversation-jobs` branch from verified `origin/main`. Preserve other branches, deployment artifacts and running containers.
- Carry design and this plan into repository `docs/superpowers/specs/2026-10-11-native-conversation-jobs-design.md` and `docs/superpowers/plans/2026-10-11-native-conversation-jobs.md` after plan approval.
- Execute inline in this session. Protocol layers share the canonical request contract and durable state service, so splitting implementation across agents is unnecessary. Independent final review is still required before PR.
- No upstream API keys are needed for fixture development. Real CLI tests use already-authorized local credentials where available, without logging tokens or changing global client config. An unavailable client/credential must be recorded as an uncompleted acceptance item.
- Each task follows red test → minimal implementation → focused tests → commit. Full checks run once the complete change is ready.
- Do not mark Issue #137 closed if real CLI or independent-host acceptance remains incomplete. The PR must state exact completed and pending validation.

## File responsibilities

| Files | Responsibility |
|---|---|
| `internal/config/conversations.go`, `_test.go`; `config.go`, `jobs.go` | Profiles, limits, identity binding, scopes and validation |
| `internal/conversation/request.go`, `messages.go`, `responses.go`, associated tests | Canonical text transcript and protocol validation; no scheduling or external I/O |
| `internal/store/conversation_migration.go`, `_test.go`; `db.go`, `migrations.go` | Durable conversation request records and schema upgrade |
| `internal/server/conversation_service.go`, `_test.go` | Authorization, frozen profile, idempotent Job submission/recovery and results |
| `internal/server/conversation_http.go`, `conversation_stream.go`, associated tests | Native envelopes/SSE, discovery, lookup/cancel and error mapping |
| `internal/server/conversation_listener.go`, `_test.go`; `http.go`, `server.go` | Dedicated secure transport routing and lifecycle |
| `scripts/ci_conversation_jobs.py`; Makefile and CI workflow | Two-protocol real-process fixture evidence |
| `docs/contracts/native-conversation-jobs.md`, `docs/deployment/native-conversation-clients.md`, README, examples | Supported subset, lifecycle, client settings, Key/CA/tunnel operation |

## Task 1 — validated profiles and canonical protocol inputs

- [x] Add failing config tests: feature off by default; missing execution identity/project/repository/commit/model/credential/template; invalid alias; unknown profile; forbidden scope; zero/negative/unbounded limits; unsafe plaintext address.
- [x] Define `config.ConversationJobs`, `config.ConversationProfile` and `Identity.ConversationProfile`. Profiles contain a complete `job.Workspace`, `job.Execution`, `job.Limits`, fixed project/execution owner, public alias and bounded input/output/concurrency settings.
- [x] Require dedicated conversation scopes and an explicitly configured execution identity with existing Job permissions. Validate both identities' project/credential ownership and trusted template match; no supplied Key in inline config examples.
- [x] Define `conversation.Request` with protocol, ordered text transcript, model alias, response cap and optional idempotency/session identifier. Implement `DecodeMessages` and `DecodeResponses` without side effects.
- [x] Add failing decoder tests for both native text shapes, history ordering, instructions, duplicate JSON keys, byte caps, invalid token limits, malformed/unrecognized executable input and unsupported media/state.
- [x] Implement tool boundary: tolerate tool declarations as inert metadata only; reject forced client tool selection and tool result/function/custom-tool input. Do not pass client tool schemas or executable instructions into Worker tool policy.
- [x] Run `go test ./internal/config ./internal/conversation -count=1`; commit the validated profile/protocol contract and document exact field behavior.

## Task 2 — durable idempotent request-to-Job mapping

- [x] Add migration tests using drained v16 Server databases, fresh databases and schema role/version guards. Introduce v17 conversation records with immutable request/profile hashes, authenticated identity key, protocol, request ID, stable Job idempotency key, Job ID and timestamps/state.
- [x] Define `beginConversationRequest(ctx, caller, request)`, `ensureConversationJob(ctx, record)` and `getConversationRequest(ctx, caller, id)` in Server; use parameterized SQL and stable IDs from existing crypto-random store helper.
- [x] Add failing tests for explicit idempotency replay/conflict, automatic request hash replay, concurrent duplicate requests, cross-identity access, profile-change conflicts and incomplete mapping recovery.
- [x] Perform immutable record insertion before Job creation; derive the existing SubmitJob idempotency key from the durable record. Recover pending records on retry/restart using the same frozen spec. Do not rely on a transaction spanning SubmitJob and a second database transaction.
- [x] Submit under the configured execution identity through an internal scoped delegation boundary. Retain caller identity on conversation records and verify profile binding before delegation; never expose a generic caller-controlled principal replacement.
- [x] Reuse existing single Job report/output contract and trusted templates. Associate trace/request/job/task/attempt IDs. If complete answer extraction requires an existing artifact/report, use its authorized structured summary/full Task result rather than bounded Job summary.
- [x] Run `go test ./internal/store ./internal/server -run 'TestConversation|Test.*Migration|TestJob' -count=1`; commit durable task mapping and recovery.

## Task 3 — shared observation, results and cancellation

- [x] Add failing tests with `newJobHarness` and real Runtime fixture for actual assignment/Worker execution, queue, failed Job, timeout, complete output larger than 4096 bytes and cleanup fencing.
- [x] Define `observeConversationJob(ctx, record)` and `conversationResult(ctx, record)` with bounded polling/notification and lifecycle tied to observer context. No unbounded detached watcher goroutines.
- [x] Add caller-authorized GET request lookup and POST request cancel under `/agent/v1/requests/{id}`. Derive stable cancel control ID; reuse existing CancelJob and wait for cleanup before successful terminal publication.
- [x] Test disconnect continues durable Job, observer timeout returns a recoverable identifier, explicit cancel persists, Job/Server restart recovers and another caller cannot observe/cancel it.
- [x] Keep canonical context stateless text replay; record this explicitly instead of pretending Worker native session resume. Return safe request/job headers and provenance separately from model self-description.
- [x] Enforce per-profile active-request/concurrency limit against durable state, preserving retries. Clean up admission state from Job terminal state; crash/restart cannot leak in-memory permits or overadmit.
- [x] Run focused Server tests including `-race`; commit task observation, isolation and lifecycle behavior.

## Task 4 — Claude/Codex native envelopes and SSE

- [x] Add table-driven failing tests for `/agent/v1/messages` and `/agent/v1/responses`: bearer auth, method/version validation, text ordinary response, protocol-format errors, unauthorized alias and bounded output.
- [x] Register the separate `/agent` handler only when enabled. Ensure existing `/v1/responses` remains a model route and `/v1/jobs`/MCP behavior is unchanged.
- [x] Implement correct protocol envelopes and SSE event ordering, text-only output, terminal error/truncation semantics and keepalive while Worker Job runs. No internal tool event becomes a local client tool call.
- [x] Preserve actual Worker usage in platform provenance. Implement conservative UTF-8-safe output truncation for protocol caps, with explicit estimated/actual usage distinction in the contract; never invent billed token counts or equate client output cap to Runtime hard budget.
- [x] Add alias-discovery responses filtered to caller profile. Return stable capability errors for token-count/compact/media/tool-loop capabilities not implemented; do not fabricate outputs merely to satisfy a client probe.
- [x] Test SSE cancellation/disconnect at each stage, malformed JSON, pre-submit errors, post-header terminal errors and no success before Worker cleanup.
- [x] Run `go test -race ./internal/server ./internal/conversation -run 'TestConversation|TestJob|TestMCP|TestGateway' -count=1`; commit both adapters and their contract.

## Task 5 — secure default transport and HTTP-over-tunnel mode

- [x] Add failing tests: dedicated plaintext listener rejects nonliteral loopback/all-interface addresses; disabled listener creates no socket; wrong CA fails; spoofed forwarded headers do not enable plaintext/security bypass; Worker TLS remains unchanged.
- [x] Add optional `conversation_jobs.loopback_listen` independent of global TLS settings. Bind before serving, validate actual listener address, serve only the `/agent` routes with explicit header/body/idle/observer timeouts.
- [x] Use TLS 1.3 for the new encrypted conversation path/listener while preserving existing transport behavior. Validate certificates instead of permitting insecure verification. Confirm no application support for 0-RTT task requests.
- [x] Own listener startup/shutdown within Server lifecycle; close failed binds and child listeners, wait for observer shutdown, and avoid goroutine leaks under restart tests.
- [x] Write SSH deployment examples with localhost-only forwarding, verified host keys, fixed forwarding target and separate tunnel identity. VPN remains an operator-managed option, not a new in-app subsystem.
- [x] Add Go benchmark subtests for input sizes 1/64/256 KiB, concurrency and TLS cold/reused connection. Report measured metrics separately from Worker/model latency; retain the design target as a target until measured.
- [x] Run focused transport and race tests; commit secure listener behavior and deployment examples.

## Task 6 — CI, real clients and compatibility evidence

- [x] Create `scripts/ci_conversation_jobs.py` using existing process helpers and protocol Runtime fixtures; launch Server and Workers, submit both native protocols, and save sanitized request/job/task/worker/event/result evidence.
- [x] Include normal and SSE success, denied Key/profile, duplicate request, explicit cancel, Worker failure and Server restart. Verify jobs/attempts actually exist and Worker cleanup is confirmed.
- [x] Register a Makefile target and bounded CI job/artifact upload following existing conventions; do not invent production certificates or secrets in CI.
- [ ] Create independent Claude/Codex settings under temporary test directories, never modify global client configuration. Record CLI version and actual startup/request behavior; extend supported inert fields only when captures justify it.
- [ ] Run available real CLI clients through the new Server path, with the model question and a remote repository task. Prove execution in Worker workspace and no local duplicated tools; capture only sanitized evidence. Mark absent clients/account access/incomplete live-host tests pending.
- [x] Run `make test`, `make vet`, `make race`, `make smoke`, `make ci-flow` and the new conversation CI target. Investigate failures; do not silently weaken existing checks.
- [x] Update README, contract and deployment docs with exact supported capabilities, Key rotation boundary, SSH/CA setup, cancellation limitations and evidence status.


**Pending acceptance:** Real Claude/Codex CLI settings and credential-backed remote request tests were not run; this environment uses CI fixtures only. Independent two-host acceptance remains separate and Issue #137 stays open.

## Task 7 — review and PR

- [ ] Self-review diff for authorization delegation, secrets, persistence race windows, schema rollback boundaries, SSE completion, unsupported protocol fields, tool execution ownership and transport listeners.
- [ ] Obtain independent code review using available review tooling; apply relevant feedback and rerun checks affected by changes.
- [ ] Commit final fixes and validation documents; push `feat/native-conversation-jobs`; create a PR referencing `Refs #137` with behavior, scope, verification and outstanding real-host acceptance.
- [ ] Attach the created PR to this task with the Codex artifact tool. Inspect GitHub CI, repair feature-related failures, and report PR URL plus confirmed/pending results.

## Review Focus and coverage

1. Job creation survives mapping crash/retry: Task 2 fault/restart tests.
2. Conversation-only Key cannot elevate project/credential scope: Tasks 1–3 identity and delegation tests.
3. Native client tool declarations do not cause duplicate local execution: Tasks 1, 4 and real CLI tests in Task 6.
4. Cap/usage/truncation semantics do not falsely claim a Runtime budget: Tasks 1 and 4 with protocol and usage assertions.
5. HTTP tunnel mode does not disable Worker TLS or expose bearer tokens on network interfaces: Task 5 address/CA/lifecycle tests and operator-controlled capture evidence.

## Execution handoff

Recommend **Native / inline execution** because all tasks depend on the same frozen profile and durable request contract. Review this plan before coding. This plan produces a reviewable PR; remaining real physical-host acceptance stays explicitly tracked instead of automatically closing Issue #137.
