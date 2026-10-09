# Prepared Workspace Core 实施记录

- Workstream：WS-A
- 日期：2026-09-29
- 状态：DONE（v0.4.5 Core 发布范围）；完整发布 CI PASS
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
- read-only template files；目录保留 owner 清理权限，摘要验证拒绝污染；
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
- template file read-only mode / cleanup-compatible directory permissions；
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

Dedicated CI Gate              DONE (v0.4.5 release CI PASS)
Performance target             WS-B
Template GC                    WS-B
~~~

WS-A 的 correctness contract 完成后，WS-B 可以在不修改 public contract 的前提下优化 materialization/cache。

## v0.4.7 representative performance Gate

本地 `ci_workspace_cache.py --benchmark` 继续用于实现回归，不关闭真实性能目标。
稳定版要求在三个固定 commit 的代表性 Linux 仓库上分别采集至少 10 个冷样本和
10 个热样本，记录完整 Job 与 preparation 时间、负载和唯一 Attempt ID。
[`v0.4.7 performance acceptance`](../validation/v0.4.7-performance-results.md)
从原始样本重算 P50/P95，并要求每个仓库 warm P50 / cold P50 不高于 `0.40`。

发布证据：[main CI 36575512360](https://github.com/tommyxie2026-tech/computecloud/actions/runs/36575512360)，`prepared-workspace-contract` / `prepared-workspace-recovery` 以及完整发布矩阵均通过。模板清理权限与 Runtime identity fixture 的旧阻塞已修复，不能继续作为当前 BLOCKED 原因。

当前存储是文件系统 manifest，不存在 `prepared_workspaces` 表；Worker v6 用于 control ledger，不是本 WS migration。Worker 级模板复用证明 correctness，不替代 WS-B 的 Job 级命中率与冷/热耗时 benchmark。下一轮见[实施台账](v0.4.5-roadmap-reconciliation.md)。
