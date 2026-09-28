# ADR-019：Prepared Workspace Core 与不可变模板

- 状态：Accepted
- 日期：2026-09-29
- 目标版本：v0.4.x
- 依赖：[ADR-009 Workspace Lifecycle](0009-workspace-lifecycle.md)、[ADR-016 EnvironmentProvider Execution](0016-environment-provider-execution.md)

## Context

当前 Worker 为每个 Attempt 从 repository baseline 冷准备独立 Workspace。ADR-009 已保证 Attempt ownership、generation isolation、restart recovery 与可恢复 GC，但重复 Job 仍会重复 clone 相同 repository/base commit。

Prepared Workspace 的目标是在不改变 Attempt ownership 的前提下，引入可复用的只读 baseline：

~~~text
Immutable Prepared Template
          ↓
Materialize
          ↓
Attempt-owned Writable Workspace
~~~

它不是共享可写 Workspace，也不是通用 IDE / Dev Environment。

## Decision

### 1. WorkspaceTemplate

Prepared template 使用结构化身份：

~~~text
template_id
repository_ref
base_commit
dependency_fingerprint
environment_fingerprint
runtime_fingerprint
tool_fingerprint
version
~~~

完整结构经过 canonical JSON + SHA-256 得到 immutable fingerprint。

### 2. PreparedWorkspaceRef

~~~text
template_id
provider
immutable_ref
prepared_at
~~~

immutable_ref 在 local provider 中是 template fingerprint，不使用可变路径作为逻辑身份。

### 3. Provider Contract

~~~text
Describe
PrepareTemplate
InspectTemplate
MaterializeAttempt
ReleaseTemplate
~~~

第一版实现 local filesystem provider。

### 4. Immutability

Local provider：

1. 从 authorized local repository clone 固定 base commit；
2. 写入 template manifest；
3. 计算完整 tree digest；
4. 去除 template tree 的写权限；
5. materialize 前重新校验 manifest + digest。

发现篡改时 fail closed，不允许 materialize。

### 5. Attempt Isolation

Template 永远不作为 Runtime CWD。

每个 Attempt 必须：

~~~text
Prepared Template
      ↓
MaterializeAttempt
      ↓
workspaces/<attempt-id>
      ↓
READY
      ↓
IN_USE
~~~

因此两个 generation 即使使用同一 template，也不会共享 writable path。

### 6. Worker Integration

现有 Job/Proto 不增加 Prepared Workspace 字段。

Worker 从已有不可变 execution facts 推导 template identity：

~~~text
repository_ref
base_commit
runtime_profile / model
environment requirement
tool requirements
~~~

因此 legacy Job 自动兼容。

### 7. Persistence Boundary

本阶段不增加 Server schema，也不增加 Worker schema。

Template manifest 是 local cache evidence；Attempt Workspace 的 durable truth 仍由 ADR-009 的 workspaces 表维护。

Template GC、cache index、warm pool、metrics 属于 WS-B，不进入本 ADR。

## Invariants

~~~text
prepared template is never Runtime writable CWD
template fingerprint binds repository baseline and execution fingerprints
materialization always creates a distinct Attempt path
two generations never share writable Workspace
template corruption fails closed
Worker restart can Inspect template from durable manifest
Attempt lifecycle remains ADR-009 authoritative
Prepared Workspace does not become a second execution truth
~~~

## Non-goals

- warm Workspace pool；
- reflink/worktree optimization；
- template GC；
- shared/object-store template provider；
- dependency manager integration；
- scheduler scoring；
- new Job/Proto fields；
- Server-side Workspace service。

## Consequences

正面：

- 相同 baseline 可以复用 prepared template；
- Workspace ownership 不变；
- 为 WS-B cache/warm path 和 WS-C readiness signal 提供稳定 contract；
- 不引入新服务或数据库。

代价：

- 第一版 materialization 使用普通文件复制，性能优化留给 WS-B；
- template tree digest inspection 有 I/O 成本；
- template cache 暂无自动 GC。
