# ADR-009：Workspace Lifecycle、Attempt Ownership 与可恢复 GC

- 状态：Accepted
- 日期：2026-09-27
- 目标版本：v0.3.3
- Issue：[#18](https://github.com/tommyxie2026-tech/computecloud/issues/18)

## 1. Context

v0.3.2 已把 Artifact 纳入 Reliability Kernel，但 Worker 的 Workspace 仍只是 `workspaces/<attempt-id>` 目录。目录存在性本身不能表达 ownership、generation、进程清理状态或是否可安全删除。

引入 Retry 后，同一个 Task 可以产生多个 Attempt generation。如果新旧 generation 共享或误复用同一个可写 Workspace，旧执行的残留进程、未提交文件或清理不确定性都可能污染新 generation。

因此 Workspace 必须成为 Worker 本地可靠性对象，但仍不升级为通用存储或远程开发环境产品。

## 2. Decision

### 2.1 Ownership

Workspace 与 `attempt_id + task_id + generation + repository_ref + base_commit + path` 一次性绑定；这些身份字段在 schema 中不可修改。

每个 Attempt 使用独立目录，因此同一 Task 的两个 generation 永不共享可写 Workspace。

### 2.2 Lifecycle

~~~text
PREPARING
   ↓
 READY
   ↓
 IN_USE
   ↓
 RETAINED
   ↓
 DELETING
   ↓
 DELETED

PREPARING / READY / IN_USE
   └── cleanup unknown ──> QUARANTINED
~~~

`QUARANTINED` 不自动回到正常生命周期，也不参与自动 GC。

### 2.3 Runtime spawn fence

Worker 必须先持久化 `READY`，在任何 Runtime/Verifier 进程 spawn 之前再持久化 `IN_USE`。这样 Worker restart 时：

- `PREPARING/READY + pid=0` 可作为“尚未启动执行进程”的 cleanup proof；
- `IN_USE + pid=0` 仍视为 spawn window 不确定，进入 QUARANTINED；
- 已记录 pid/start_id 时继续使用 process identity cleanup proof。

### 2.4 Retention and GC

正常或失败执行只有在 process cleanup confirmed 后进入 RETAINED，并设置 `retain_until`。

自动 GC 还要求对应 `runs.completed=1`，即 Server 已确认 completion。只有同时满足：

~~~text
state = RETAINED
retain_until expired
run completed = true
~~~

Workspace 才能进入 DELETING。

### 2.5 Crash-recoverable deletion

~~~text
RETAINED -> DELETING
              ↓
        remove directory
              ↓
          DELETED
~~~

DELETING 是持久状态；Worker restart 后可继续删除。DELETED 保留 tombstone。

### 2.6 Legacy workspace adoption

升级后 Worker 扫描已有 Workspace 目录时，只自动接管能与本地 `runs` 记录对应的目录。未知目录不自动删除、不自动认领。

已有 completion 且 cleanup confirmed 的 legacy Workspace 进入 RETAINED；无法证明 cleanup 的进入 QUARANTINED。

### 2.7 Workspace quota

新增 Worker 配置 `workspace_max_bytes`。值为 0 表示关闭。启用时：

- prepare 完成后检查一次；
- Runtime 执行期间周期检查；
- completion 前最终检查；
- quota 超限会取消执行，并阻止成功 completion。

Quota 是安全阈值，不是精确实时计费或文件系统配额。

### 2.8 Attempt-scoped Reduce inputs

`inputs/<attempt-id>` 与 writable Workspace 使用相同 Attempt ownership。只有在 process cleanup confirmed 后才立即清理；cleanup unknown / QUARANTINED 时不删除。若即时清理失败，后续 Workspace deletion reconciliation 再次尝试。

这样 Reduce 输入不会跨 generation 复用，也不会因为 Worker restart 的不确定执行状态被提前删除。

## 3. Invariants

~~~text
workspace ownership is immutable per Attempt generation
two generations never share a writable workspace
Runtime never spawns before IN_USE is persisted
running or cleanup-unknown workspace is never GC eligible
GC requires RETAINED + retention expired + run completed
QUARANTINED is never auto-GC
deletion is crash-recoverable and leaves a tombstone
quota failure cannot publish successful completion
unknown legacy directories are not auto-deleted
~~~

## 4. Rejected alternatives

复用 Task 级 Workspace：拒绝，会把 Retry generation 的写状态耦合在一起。

只依赖目录名判断 ownership：拒绝，无法表达 generation、baseline 和 cleanup proof。

Attempt 完成后立即删除：拒绝，不利于诊断，也无法保证 Server 已确认 completion。

Worker restart 时删除未知目录：拒绝，缺少 ownership/cleanup 证据。

直接引入 container/VM snapshot 或 shared workspace provider：拒绝，这属于后续 EnvironmentProvider / Prepared Workspace。

## 5. Consequences

正向：Retry generation 的可写状态隔离；Worker restart 的 cleanup 语义更明确；Workspace GC 可恢复；后续 Prepared Workspace 可以在不破坏 ownership 的前提下做只读模板/复制优化。

代价：Worker schema 升级到 v3；本地 metadata 与目录需要 reconciliation；启用 quota 时会增加周期目录扫描成本。

## 6. Non-goals

Prepared/warm Workspace、共享 Workspace Provider、容器/VM 文件系统隔离、Runtime checkpoint/resume、全局历史 retention policy、Server-side Workspace Storage Service 都不属于 v0.3.3。