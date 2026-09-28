# Prepared Workspace Core 实施记录

- Workstream：WS-A
- 日期：2026-09-29
- 状态：Implemented on main，CI pending
- ADR：[ADR-019 Prepared Workspace Core](../adr/0019-prepared-workspace-core.md)

## Current State

ADR-009 已提供 Attempt Workspace ownership / lifecycle / recovery；v0.4.4 已完成 EnvironmentProvider execution。

并行开发期间另一 Workstream 已把 Server schema 推进到 v12，因此 WS-A 不再申请 Server migration。

## Implemented

### Contract

新增：

~~~text
WorkspaceTemplate
PreparedWorkspaceRef
PreparedProvider
ProviderDescriptor
InspectResult
~~~

Provider contract：

~~~text
Describe
PrepareTemplate
InspectTemplate
MaterializeAttempt
ReleaseTemplate
~~~

### Local Provider

新增 local-prepared provider：

- authorized local repository；
- fixed base commit；
- canonical template SHA-256；
- manifest；
- tree content digest；
- read-only template tree；
- restart Inspect；
- corruption fail-closed；
- isolated writable materialization。

### Worker Integration

Worker prepare path 已从：

~~~text
repository -> clone directly into attempt
~~~

调整为：

~~~text
repository
   ↓
local prepared template
   ↓
digest verified
   ↓
materialize
   ↓
attempt-owned Workspace
~~~

没有新增 public Job/Proto 字段。

Template identity 从已有：

~~~text
repository_ref
base_commit
runtime profile/model
environment requirement
tool requirements
~~~

推导。

### Reliability

保持：

- one Attempt one writable path；
- retry generation isolation；
- Attempt Workspace DB lifecycle 不变；
- template corruption 不进入 Runtime；
- template 不成为第二事实源。

## Tests

新增：

- template fingerprint stability；
- local prepare / inspect；
- provider restart inspect；
- template filesystem read-only mode；
- two Attempt materialization isolation；
- Attempt mutation does not alter template；
- template tamper detection；
- invalid ref rejection；
- duplicate attempt path rejection；
- Worker-level template reuse without writable-state sharing。

并继续复用 ADR-009 generation isolation / recovery tests。

## Schema

~~~text
Server schema: v12 (parallel ACP workstream)
Worker schema: v6
WS-A schema change: none
~~~

## Explicitly Deferred to WS-B

- reflink / git-worktree optimization；
- template cache index；
- warm pool；
- template GC；
- cache metrics；
- benchmark tuning。

## DoD Status

~~~text
PW-1 Contract                  DONE
PW-2 Local Provider            DONE
PW-3 Attempt materialization   DONE
PW-4 Restart Inspect           DONE

Dedicated CI Gate              IN PROGRESS
Performance target             WS-B
Template GC                    WS-B
~~~

WS-A 的 correctness contract 完成后，WS-B 可以在不修改 public contract 的前提下优化 materialization/cache。
