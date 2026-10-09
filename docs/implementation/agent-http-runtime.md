# Self-hosted Agent HTTP Runtime (ECO-02 implementation slice)

computecloud remains an Agent-aware Distributed Job Execution Platform / Agent
Job Executor. This service runs the configured Codex or Claude Code CLI for a
Worker Attempt; it is not an LLM gateway, model server, or general workflow
engine.

The pinned upstream release snapshot on 2026-10-07 is Codex CLI `0.160.1`
(`rust-v0.160.1`) and Claude Code `2.1.292`. The Worker and service must agree
on the exact version. Build and deployment records must pin the resulting OCI
image by digest. Updating either CLI requires a new compatibility run.

## Contract

The Worker connects to a self-hosted HTTP service over an authenticated Unix
socket. `endpoint` must be `unix:///absolute/path.sock`; TCP endpoints are
rejected. `token_file` is read for each call. The service compares the bearer
token and has no unauthenticated health response.

| Request | Meaning |
| --- | --- |
| `GET /v1/health?profile=codex_http` | Probe the Docker daemon and return `{profile,version,isolation:"container"}`; also accepts `claude_http`. Docker failure returns 503. |
| `PUT /v1/runs/{attempt_id}` | Create once by immutable Attempt ID and generation; the Worker persists the remote reference before this call. Repeating the same request returns the same ID; a changed request is a conflict. |
| `GET /v1/runs/{attempt_id}?after=N` | Return state, cleanup, exit code, final outcome, and sequenced events after cursor `N`. Events retain the existing Job/Attempt provenance when the Worker records them. |
| `DELETE /v1/runs/{attempt_id}` | Request stop with bounded `grace_ms`, then report cleanup evidence or `UNKNOWN`. |
| `GET /v1/environments/{attempt_id}` | Inspect the owned Docker container; unavailable Docker evidence returns `UNKNOWN`. |
| `DELETE /v1/environments/{attempt_id}` | Persist a no-replay tombstone, stop the run if active, and confirm Docker cleanup or return `UNKNOWN`. |

The service keeps a mode-0600 run marker that prevents execution replay after
restart. Each Attempt runs in a distinct Docker container named from its
Attempt ID. The service alone receives the bearer token and Docker socket;
the Agent container receives only its Attempt workspace and configured runtime
environment. On restart, the service can inspect or remove a residual
container, but it cannot reconstruct the final event stream or outcome. It
reports an unknown runtime state and only confirms cleanup after querying the
Docker daemon. Failure to reach the daemon remains `UNKNOWN` and must not
count as cleanup proof.

## OCI deployment shape

`Dockerfile.agent-http` installs the exact CLI versions and Docker client, and
runs the HTTP controller as non-root UID 65532. The same image is used for
short-lived per-Attempt containers, with its entrypoint overridden to Codex
or Claude. Build it with:

```sh
docker build -f Dockerfile.agent-http -t computecloud-agent-http:0.160.1-2.1.292 .
```

Push the image, record its `sha256` digest, and pass that immutable reference
as the controller's `--runtime-image`. Mount a private shared directory at the
same absolute path in the Worker and controller for the Unix socket and bearer
token. Mount the Worker workspace root at the **same absolute path** inside the
controller and persist `/var/lib/computecloud-agent/runs` across controller
restarts. The controller needs access to the Docker Engine socket; its UID must
be allowed to use that socket. This is privileged infrastructure access and
must stay with the controller, never the Agent container. The socket file and
token are not mounted into Agent containers. The shared socket directory must
be accessible to the Worker UID because the HTTP socket is mode 0600. Supply
model credentials at runtime, never in the image.

For a Linux Worker using `/srv/computecloud/workspaces`, the controller launch
has this shape (replace the image reference and prepare the mounted directories
as UID 65532 with private permissions):

```sh
docker run -d --name computecloud-agent-http \
  --user 65532:65532 \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --mount type=bind,src=/run/computecloud-agent,dst=/run/computecloud-agent \
  --mount type=bind,src=/srv/computecloud/workspaces,dst=/srv/computecloud/workspaces \
  --mount type=bind,src=/srv/computecloud/agent-state,dst=/var/lib/computecloud-agent/runs \
  --tmpfs /tmp:rw,nosuid,noexec,size=64m \
  IMAGE@sha256:REPLACE_WITH_64_HEX_DIGEST \
  --workspace-root /srv/computecloud/workspaces \
  --runtime-image IMAGE@sha256:REPLACE_WITH_64_HEX_DIGEST
```

The controller requires the Docker socket, which grants host-level container
control; only trusted operators should deploy or configure it. The child
Agent container has no Docker socket or HTTP token mount. Do not use this
example as production certification before ECO-03 and VAL-01 evidence passes.

The controller launches each Agent container with a read-only root filesystem,
no Linux capabilities, `no-new-privileges`, a PID limit, private writable
tmpfs, and a bind mount of only that Attempt's workspace. It uses Docker's
bridge network for model access; outbound network policy is still an operator
deployment decision. `HOME`, `PATH`, `TMPDIR`, and Docker control variables
from the Worker are replaced or stripped before the Agent starts.
The Worker runtime configuration is:

```yaml
worker:
  runtimes:
    codex_http:
      endpoint: unix:///run/computecloud-agent/runtime.sock
      token_file: /run/computecloud-agent/token
      version: 0.160.1
      models: [YOUR_CODEX_MODEL]
      credentials: [YOUR_CREDENTIAL_REF]
    claude_http:
      endpoint: unix:///run/computecloud-agent/runtime.sock
      token_file: /run/computecloud-agent/token
      version: 2.1.292
      models: [YOUR_CLAUDE_MODEL]
      credentials: [YOUR_CREDENTIAL_REF]
```

The HTTP Runtime advertises `environment:container` only when the Worker has
registered the Container EnvironmentProvider and the authenticated service
health response identifies its container isolation contract. A Job must still
name `environment:container` and the Worker policy must explicitly allow
`container`; legacy Jobs continue to use `process`. The EnvironmentProvider
persists the controller socket and token-file reference before preparation,
queries the controller on recovery, and requires a confirmed Docker cleanup
response before reporting success. Releasing an Environment writes a durable
tombstone before checking Docker, which prevents a late or replayed run from
starting even when Docker is unavailable. If the controller or Docker daemon
cannot prove cleanup, the Worker records `UNKNOWN` and fails closed.

## Claude estimated-cost boundary

Authenticated health for `claude_http` 2.1.217 or later advertises
`budget_claude_estimated_usd_v1`. When a Goal has only `max_cost_units`, the
Server dispatches the remaining allowance as integer micro-USD and the
controller converts it exactly to `--max-budget-usd`. Decimal parsing rejects
negative, non-finite and overflowing values and rounds a positive sub-micro-USD
remainder upward. The Worker persists the final `total_cost_usd` estimate and
`error_max_budget_usd` as `RUNTIME_BUDGET_EXHAUSTED`; the Server settles that
usage before releasing the Attempt. A single API call can take the reported
total above the dispatched remainder. This boundary controls the Claude client
estimate and does not claim exact provider invoice enforcement.

Finite-budget Goals permit one in-flight Attempt. Missing or incomplete usage
blocks the next Attempt. Claude token caps and all Codex token/cost caps remain
unsupported. `make ci-runtime-budget` validates this contract with fixture
responses and Docker command construction. Its GitHub CI job additionally runs
a credential-free fake Claude executable in OCI for normal completion, native
budget termination, container failure, stream interruption and missing-final
handling. It makes real Docker calls but no real model calls. Fixed-version
real-provider validation remains part of VAL-01. The stable v0.4.7 Runtime and
Goal budget evidence format, fixed cases, sanitization rules, and fail-closed
command are defined in
[`../validation/v0.4.7-runtime-acceptance.md`](../validation/v0.4.7-runtime-acceptance.md).
The report validator is repository-ready, but its Gate remains blocked until an
authorized real-provider run supplies the report.

This is an implementation and fixture-test result, **not isolation
certification**. Docker-backed file/network escape, crash/restart and
actual-host negative tests remain ECO-03 acceptance work. The final independent
host Runtime and upgrade/restore acceptance remains VAL-01.

The Go tests cover authenticated contract handling, version and workspace
fences, idempotent creation, Docker command construction, event cursor,
Stop/Inspect, and restart fail-closed behavior. They use fixtures; they do not
constitute real Codex/Claude or Docker isolation certification.
