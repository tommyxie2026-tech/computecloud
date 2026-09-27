# ADR-013：Transport-neutral Runtime Execution

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.4.1
- Issue：[#27](https://github.com/tommyxie2026-tech/computecloud/issues/27)
- 依赖：[ADR-012 Runtime API v2](0012-runtime-api-v2.md)

## 1. Context

v0.4.0 已把 Runtime profile 发现、Probe、Args、Parser、Capability 和 Gateway eligibility 收敛到 Provider Registry，但 Agent 的真实执行仍由 Worker 直接调用本地 `process.Run`。这意味着 Provider 虽然可注册，执行 transport 仍被假定为本地 CLI。

长期路线需要同时覆盖 local CLI、API-backed Agent、self-hosted Agent Server 和 managed cloud Agent。因此 Worker 必须只理解 Runtime execution contract，而不能继续把 PID/process group 当成所有 Runtime 的公共模型。

## 2. Decision

### 2.1 Provider owns execution transport

Runtime Provider 在 v0.4.1 增加以下执行职责：

~~~text
Version
Transport
Prepare
Start
Inspect
Stop
~~~

`Prepare` 负责把 TaskSpec、Policy、Gateway、基础环境和输入转换为 Provider 自己可执行的准备结果。`Start` 在 Worker 执行 goroutine 中运行 Runtime，并通过 started callback 先持久化 `ExecutionRef`，随后才允许 Worker 把本次执行视为 RUNNING。

`Inspect` 和 `Stop` 用于 Worker restart / recovery。它们必须由 Provider 解释其自己的 durable reference，Worker 不根据 profile 名推断 transport。

### 2.2 Durable ExecutionRef

统一引用：

~~~text
provider
transport
id
pid        optional
start_id   optional
~~~

`pid/start_id` 只服务 local CLI compatibility。remote/API-backed Runtime 可以只保存 provider/transport/id。

Worker schema v4 在 `runs` 中持久化：

~~~text
runtime_provider
runtime_transport
runtime_ref
runtime_state
runtime_cleanup
~~~

RuntimeRef 在执行被记录为 RUNNING 前必须落盘。若持久化 callback 失败，Provider 必须使当前 Start 失败并进行其 transport 所需的清理。

### 2.3 Runtime and cleanup states

Runtime state：

~~~text
STARTING
RUNNING
EXITED
UNKNOWN
~~~

Cleanup state：

~~~text
PENDING
CONFIRMED
UNKNOWN
~~~

`UNKNOWN` 永远 fail closed：不能据此发布 success、释放 Workspace 所有权或自动恢复执行。

### 2.4 Local CLI bridge

Codex/Claude 继续保持当前 CLI 行为，但本地 process group 的启动、JSONL parser、取消和 cleanup proof 由 Provider 的 local CLI bridge 封装。Worker Agent execution path 不再直接以 `runtime executable + process.Run` 启动 Runtime。

可信 Verifier 仍是 Worker 内部 operator command，它不是 Agent Runtime，因此继续使用独立的本地 `process.Run`。

### 2.5 Recovery

Worker restart 时：

~~~text
runtime_ref exists
  ↓
Provider.Inspect
  ├─ RUNNING -> Provider.Stop -> require cleanup proof
  ├─ EXITED + CONFIRMED -> safe completion
  └─ UNKNOWN -> CLEANUP_UNCONFIRMED
~~~

旧版本没有 runtime_ref 的 local run 保留 legacy process recovery；pre-spawn Workspace proof 仍可证明尚未启动。

Worker 不自动 resume remote Runtime。Session resume 属于后续 capability。

### 2.6 Capability

Built-in CLI Provider 广告：

~~~text
runtime:local_cli
~~~

API-backed fixture 广告：

~~~text
runtime:remote_api
~~~

Transport capability 只描述执行方式，不改变 Job/Stage/Task/Attempt 领域模型。

## 3. Invariants

~~~text
Worker core does not select Runtime transport by profile name
Provider owns Prepare/Start/Inspect/Stop
runtime_ref is durable before RUNNING ownership is accepted
remote Runtime does not require local PID
UNKNOWN cleanup fails closed
Worker restart never silently resumes unknown Runtime
existing generation fencing remains authoritative
Runtime transport cannot alter Artifact/Workspace ownership
~~~

## 4. Rejected alternatives

### 4.1 为每种 remote Agent 在 Worker 增加 if/switch

拒绝。这会重新引入 v0.4.0 已移除的 profile-specific core coupling。

### 4.2 把 PID 继续作为统一 Runtime identity

拒绝。API-backed、managed Agent 和远程 Agent Server 没有本地 PID。

### 4.3 Worker restart 自动重新调用 remote Start

拒绝。未知远端执行可能仍在运行，重新 Start 会制造双执行。必须先 Inspect/Stop 并取得 cleanup proof。

### 4.4 同时引入 Session resume / Approval

拒绝。本版本先稳定 execution transport；交互能力后续按 RuntimeCapability 引入。

## 5. Consequences

正向：

- Worker Agent execution 不再绑定本地 process transport；
- 第三方 Runtime 可以通过 durable ref 实现远端执行；
- restart/recovery 对 local/remote 使用相同 fail-closed 原则；
- Codex/Claude 保持现有 profile 和 wire compatibility。

代价：

- Worker schema 升级为 v4；
- Provider contract 增加执行生命周期方法；
- 远端 Provider 必须自己保证 Start/Inspect/Stop 的幂等与 cleanup 语义。

## 6. Non-goals

本 ADR 不实现具体商业 Agent API、Session resume、interactive approval、Tool execution framework、EnvironmentProvider、Prepared Workspace 或 Agent-aware scoring。