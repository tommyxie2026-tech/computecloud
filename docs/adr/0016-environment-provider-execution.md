# ADR-016：EnvironmentProvider Execution Lifecycle

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.4.4
- Issue：[#33](https://github.com/tommyxie2026-tech/computecloud/issues/33)
- 依赖：[ADR-015 EnvironmentCapability](0015-environment-capability.md)、[ADR-013 Runtime Execution](0013-runtime-execution.md)、[ADR-009 Workspace Lifecycle](0009-workspace-lifecycle.md)

## Context

v0.4.3 已把 Environment 从 Runtime Provider 的附属 capability 字符串拆成独立 Registry / Descriptor / Policy contract，但 Worker 仍只真正执行 `process + workspace`。如果后续直接在 Worker 中增加 `if environment == container` / `vm` / `sandbox` 分支，会重新把执行载体与 Worker 核心耦合，并破坏 Runtime API v2 已建立的 transport-neutral 边界。

因此 v0.4.4 引入 EnvironmentProvider execution lifecycle，使 Environment 的准备、激活、恢复与释放由 Provider 拥有；Worker 只负责生命周期编排、持久化 fencing 与 cleanup proof。

## Decision

### 1. EnvironmentProvider contract

~~~text
Descriptor
Prepare
Activate
Inspect
Release
~~~

`Prepare` 返回 transport-neutral `EnvironmentRef`、Runtime 使用的 CWD 与 Env。Provider 必须在真正产生不可逆外部执行状态前通过 callback 持久化 `EnvironmentRef`。

### 2. EnvironmentRef

~~~text
provider
id
~~~

`EnvironmentRef` 必须能在 Worker restart 后独立用于 Inspect / Release，不要求是本地 PID、container ID 或 VM ID 的固定格式。

### 3. Lifecycle

~~~text
PREPARED
   ↓
ACTIVE
   ↓
RELEASED

unknown observation -> UNKNOWN
~~~

Cleanup 独立表示：

~~~text
PENDING
CONFIRMED
UNKNOWN
~~~

Environment ACTIVE 必须在 Runtime Prepare/Start 前持久化。Runtime execution 只接收 Environment Provider 返回的 CWD / Env。

### 4. Attempt cleanup composition

Attempt cleanup proof 不是 Runtime cleanup 的同义词。v0.4.4 定义：

~~~text
Attempt cleanup confirmed
=
Runtime cleanup confirmed
AND
Environment cleanup confirmed
~~~

任一侧 UNKNOWN 都必须 fail closed：不能发布 success，Workspace 进入既有 QUARANTINED 路径，Retry Safety 不获得 cleanup proof。

### 5. Worker persistence

Worker schema v5 在 runs 保存：

~~~text
environment_provider
environment_ref
environment_state
environment_cleanup
~~~

EnvironmentRef 在 Runtime Start 前 durable。Worker restart 使用 Provider Inspect / Release；不能把 UNKNOWN Environment 当作已清理，也不能静默复用旧 Environment。

### 6. Builtin process Environment

`process` 也实现同一 Provider contract。它不拥有 Runtime 子进程本身，Runtime cleanup 仍由 Runtime Provider 证明；process Environment 只表示当前 Workspace/host execution context，因此 Release 可独立确认。

这保持现有 Codex/Claude 行为兼容，同时验证 Worker 核心不依赖 Environment 名称。

### 7. Capability advertisement

v0.4.3 的 descriptor registry 继续保留，但 Worker 广告从“registered descriptor ∩ Runtime-compatible”收紧为：

~~~text
executable Environment Provider
∩
Runtime-compatible Environment
~~~

仅有 Descriptor、没有 execution Provider 的 Environment 不得被调度。

## Invariants

~~~text
Environment execution is Provider-owned
EnvironmentRef is durable before Runtime Start
Runtime uses Provider-prepared CWD/Env
Attempt cleanup requires Runtime + Environment cleanup proof
UNKNOWN Environment fails closed
Worker restart never silently reuses unknown Environment
descriptor-only Environment is not advertised
Scheduler contains no Environment implementation-name branches
Job/Stage/Task/Attempt semantics are unchanged
~~~

## Rejected alternatives

把 container/VM 直接写进 Worker switch：拒绝，会复制 Runtime 早期 profile-name 耦合。

把 Environment 合并进 Runtime Provider：拒绝，同一 Runtime 应能运行在多个 Environment，二者需要独立 capability/policy 与生命周期。

Environment cleanup 只看 Runtime cleanup：拒绝，外部 sandbox/container 可能在 Runtime 已退出后仍继续存在。

在 v0.4.4 同时实现生产 container/VM/external sandbox：拒绝，本版本先稳定 contract 和 failure semantics。

## Non-goals

- 生产 container/VM Provider；
- 商业 Sandbox API 绑定；
- NetworkPolicy enforcement；
- filesystem snapshot/checkpoint；
- Prepared/warm Workspace；
- Environment 跨 Worker migration；
- v0.5 Agent-aware score；
- RBAC / HA。