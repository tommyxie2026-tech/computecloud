# ADR-007：Artifact Lifecycle、Immutable References 与可恢复删除

- 状态：Accepted
- 日期：2026-09-26
- 目标版本：v0.3.2
- Issue：[#15](https://github.com/tommyxie2026-tech/computecloud/issues/15)

## 1. Context

v0.3.0 已建立 Artifact 的最低可靠性边界：Artifact 归属于 Attempt generation，并以 `STAGED / ACCEPTED / ORPHANED` 区分候选结果、正式结果和失败代产物。

v0.3.1 引入 Retry 后，一个 Task 会长期保留多个历史 Attempt。仅依赖“当前 Task 指向哪个 Attempt”不足以回答三个问题：哪个 Artifact 已经成为 Task、Reduce 或 Job 的稳定输入/输出；哪些 Artifact 可以安全清理；Server 在数据库决定删除与文件实际删除之间崩溃时如何恢复。

因此 v0.3.2 把 Artifact 生命周期纳入 Reliability Kernel，而不是提前建设通用存储平台。

## 2. Decision

### 2.1 状态机

~~~text
STAGED
 ├──> ACCEPTED
 └──> ORPHANED
          │
          └──> DELETING
                    │
                    └──> DELETED
~~~

允许的状态迁移由 SQLite trigger 强制约束。`STAGED` 表示已上传但未正式接受；`ACCEPTED` 是唯一正式结果；`ORPHANED` 是失败或未选中产物；`DELETING` 表示已取得删除所有权；`DELETED` 保留 tombstone。

### 2.2 Artifact Reference

引入 `artifact_refs`，表示不可变的逻辑引用：`task_result`、`reduce_input`、`job_result`。

规则：只有 `ACCEPTED` Artifact 能创建 reference；reference 创建后不可 UPDATE/DELETE；被 reference 的 `ACCEPTED` Artifact 不允许离开 ACCEPTED；Task 正式结果、Reduce 输入和 Job 最终结果必须由显式 reference 固化；旧 generation 即使文件仍在，也不能通过 reference 进入新执行链路。

Reference 是生命周期所有权依据，不把 JSON manifest/result 当作唯一 GC 根。

### 2.3 Publication semantics

v0.3.2 不增加独立的 `PUBLISHED` 状态。一个 Artifact 可以同时成为 task result、reduce input 和 job result，因此“被谁使用”属于引用关系，而不是 Artifact 自身的单一状态。使用 `ACCEPTED + immutable refs` 表达发布关系。

### 2.4 GC eligibility

当前只自动处理 `ORPHANED`、已超过固定安全窗口、且不存在任何 `artifact_refs` 的对象。默认 orphan safety window 为 24h，它是实现级安全缓冲，不是用户 retention SLA。

被引用的 ACCEPTED Artifact 不进入本版本自动 GC。完整历史 retention、用户 TTL、tombstone retention 与全局 GC policy 留到后续 Scale & Resilience。

### 2.5 Crash-recoverable deletion

~~~text
DB claim: ORPHANED -> DELETING
        ↓
delete file
        ↓
DELETING -> DELETED
        ↓
keep tombstone
~~~

如果 Server 崩溃，DELETING 会在后续 reconciliation 再执行；文件已经不存在视为删除成功；只有不存在 reference 才能进入 DELETED；DELETED 不会重新成为有效结果。

### 2.6 Filesystem orphan cleanup

Server 允许清理超过 1h 的 `.upload-*` 临时文件，以及超过 24h、文件名符合 Artifact ID 且 DB 中不存在 metadata 的孤儿文件。它不会按目录遍历结果重新发现或恢复 Artifact，也不会删除仍有 DB metadata 的普通文件。

## 3. Invariants

~~~text
only ACCEPTED artifacts are externally visible
only ACCEPTED artifacts can be referenced
artifact refs are immutable
referenced ACCEPTED artifacts are not GC-eligible
stale generations cannot publish or pin artifacts
failed/retried generations cannot become Reduce or Job results
deletion is crash-recoverable
DELETED keeps a tombstone
artifact ID cannot be silently rebound to different bytes
~~~

并继续继承 v0.3.0 / v0.3.1 的 generation fencing 与 Retry Safety。

## 4. Rejected alternatives

仅解析 Job Result / Manifest 判断引用：拒绝，JSON 不适合作为数据库 GC 的唯一 referential root。

完成时直接删除失败 Artifact：拒绝，Server crash 会产生 DB/file 双边状态不一致，也失去诊断安全窗口。

引入对象存储或 Storage Service：拒绝，本版本解决 Agent Job 生命周期正确性，而不是跨后端存储平台。

同时实现全量 Retention/TTL：拒绝，全局历史清理涉及 Job/Event/Workspace/Attempt 一致性，应后续统一治理。

## 5. Consequences

正向：Retry 后旧 generation 与正式结果彻底解耦；Reduce / Job 结果具有显式 provenance；GC 不依赖隐式 JSON；文件删除可重放；为 v0.3.3 Workspace Lifecycle 提供 ownership/reconciliation 模式。

代价：schema 升级为 v6；增加 lifecycle reconciliation；tombstone 和 reference 会持续增长，后续需要统一 retention policy。

## 6. Non-goals

Workspace Lifecycle、用户自定义 TTL、全局历史 GC、shared/object storage backend、跨区域复制、独立 GC service、HA 均不属于 v0.3.2。