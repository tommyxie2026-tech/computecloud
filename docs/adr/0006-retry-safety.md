# ADR-006：Retry Safety

- 日期：2026-09-26
- 状态：Accepted
- 版本：v0.3.1
- 依赖：ADR-005 Stage / Multi-Attempt / Generation Fencing
- 关联 Issue：[#12](https://github.com/tommyxie2026-tech/computecloud/issues/12)

## Context

v0.3.0 已建立 multi-Attempt history、current generation、Worker epoch fencing 和 Artifact generation gating，但仍然没有自动 Retry。

没有 Retry 时，短暂的 Runtime / Worker 故障会直接终止 Job；如果无条件 Retry，又可能对有外部副作用、执行状态不确定或未完成 cleanup 的任务造成重复执行。

因此 v0.3.1 的目标不是“尽量重试”，而是建立一个保守的 Retry Safety 边界。

## Decision

### 1. Retry 必须由 JobSpec 显式声明可重放

Execution 增加：

~~~json
{
  "replay_safe": true
}
~~~

当 `max_attempts_per_task > 1` 时，所有可能被执行的 Execution 都必须显式设置 `replay_safe=true`。

缺失或为 false 时拒绝提交，而不是由 Server 推测任务是否幂等。

### 2. Retry budget 有硬上限

v0.3.1 支持：

~~~text
max_attempts_per_task = 1..3
~~~

Attempt generation 单调递增，并继续沿用 v0.3.0 fencing。

### 3. Retry error taxonomy 由 Server 固定

v0.3.1 仅允许以下失败进入自动 Retry：

~~~text
RUNTIME_FAILED
WORKER_RESTARTED
STORAGE_UNAVAILABLE
ARTIFACT_ERROR
~~~

验证失败、协议错误、输入错误、权限错误、deadline、用户取消、执行状态不确定等不进入自动 Retry。

调用方不能通过 JobSpec 自定义“哪些错误可重试”，避免把安全策略下放到任务提交方。

### 4. Cleanup confirmed 是必要条件

Retry 只从正常 CompleteAttempt 的 cleanup-confirmed 路径触发。

~~~text
failure
  ↓
cleanup_confirmed?
  ├─ no  -> RECONCILING / terminal policy
  └─ yes
       ↓
replay_safe?
       ↓
retryable error?
       ↓
budget + deadline?
       ↓
schedule next generation
~~~

执行状态不确定时不得自动创建新 Attempt。

### 5. Deadline 不因 Retry 重置

Task / Job 原始 deadline 保持不变。Retry 使用剩余时间预算。

### 6. Backoff 必须持久化

Server schema v5 增加 `tasks.retry_after`。Scheduler 只调度 `retry_after <= now` 的 QUEUED Task。

当前退避：

~~~text
generation 1 -> 500ms
generation 2 -> 1s
generation 3 -> 2s
...
cap -> 30s
~~~

该退避是实现策略，不作为长期公共协议保证。

### 7. Retry 可观察

至少产生：

- `task.retry_scheduled`
- `job.task_retry_scheduled`
- `task.retry_exhausted`
- `job.task_retry_exhausted`

历史 Attempt 不删除。

## Consequences

收益：

- 短暂 Runtime / Worker 类故障可恢复；
- Retry 建立在 v0.3.0 fencing 上，不破坏 stale-generation 不变量；
- 不需要新增外部队列或 Workflow Engine；
- Retry history 可审计。

限制：

- 目前只支持 Job-managed Task；
- replay safety 由调用方显式声明；
- 当前最多 3 次 Attempt；
- 当前 taxonomy 是保守 allowlist；
- 不实现 Workspace resume；
- 不对 execution-uncertain 状态自动 Retry。

## Non-goals

- 通用 Retry DSL；
- 用户自定义错误分类；
- exactly-once 外部副作用；
- 任意 DAG Retry；
- Workspace checkpoint / resume；
- Runtime API v2；
- HA。

## Invariants

v0.3.1 必须保持：

~~~text
retry only when replay_safe
retry only after cleanup confirmed
deadline never resets
generation monotonically increases
stale generation cannot publish valid writes/results
uncertain execution never auto-retries
~~~