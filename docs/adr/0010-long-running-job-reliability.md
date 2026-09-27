# ADR-010：Long-running Job 可靠性、显式 Deadline 与事件压缩

- 状态：Accepted
- 日期：2026-09-27
- 目标版本：v0.3.4
- Issue：[#21](https://github.com/tommyxie2026-tech/computecloud/issues/21)
- 依赖：ADR-005、ADR-006、ADR-007、ADR-009

## 1. Context

v0.3.0–v0.3.3 已经建立 Stage、multi-Attempt、generation fencing、Retry Safety、Artifact Lifecycle 与 Workspace Lifecycle。长时间 Agent Job 还存在三个需要独立解决的问题：

1. Worker 持续存活与 Runtime 是否持续输出是两个不同概念，长时间无 stdout/stderr 不能被误判为 hung；
2. 已开始执行的 Job 若需要延长 deadline，Worker 不能继续依赖 Assignment 中已经冻结的本地 deadline；
3. 长任务可能产生大量流式事件，Server 不能无限保留全部 Task 事件，同时压缩后也不能让旧 cursor 静默产生事件缺口。

## 2. Decision

### 2.1 Lease 是执行活性边界

Worker 每秒通过 Renew 上报当前未完成 Attempt。Server 只有在 Attempt identity、generation、Worker epoch、lease token 与当前 Task ownership 全部匹配时才续租。

Server 在每次有效 Renew 时持久化：

- `last_renewed`
- `lease_until`

因此：

> **runtime output 不是 liveness。Renewed lease 才是 Worker/Attempt liveness。**

即使 Runtime 数分钟没有输出，只要 Worker 正常续租、Job 未到 deadline 且没有 cancel，Attempt 就保持有效。

### 2.2 Server 是 mutable deadline 的唯一事实源

Worker 不再用 Assignment 中的 `deadline_ms` 创建固定本地 context deadline。

原因是 Assignment 是一次性调度快照，而 v0.3.4 允许显式延长正在运行 Job 的 deadline。若 Worker 保留旧 deadline，本地进程会在 Server 已合法延长后被错误取消。

新的边界：

~~~text
Job/Task deadline
      ↓
Server controller
      ↓
lease valid / stop command
      ↓
Worker execution context
~~~

如果 Server 不可达，Worker 最终因 lease 到期自动取消，因此失联不会导致无限执行。

### 2.3 Deadline extension 必须显式、幂等、有界

新增：

~~~text
POST /v1/jobs/{job_id}/deadline
MCP tool: extend_job_deadline
~~~

请求包含：

- `operation_id`
- `new_deadline_ms`

规则：

- 需要 `jobs:extend` scope；
- deadline 只能单调增加；
- operation_id 完全相同的重放返回同一结果；
- 同 operation_id 不同内容返回 idempotency conflict；
- 每次延长不超过 `max_deadline_extend_seconds`；
- 最终 deadline 不超过 `created_at + max_total_runtime_seconds`；
- terminal / STOPPING / RECONCILING / 已存在 stop reason 的 Job 不允许延长；
- 同步更新所有未终态 child Task deadline；
- 不改变 Attempt generation、Retry 次数、Artifact/Workspace ownership。

默认：

~~~text
max_total_runtime_seconds   = 7 days
max_deadline_extend_seconds = 24 hours
~~~

初始 Job timeout 的现有输入边界保持不变；长时间运行通过显式延长获得。

### 2.4 Stop escalation 必须有执行证据

Linux trusted-process Worker 继续使用 process group：

~~~text
SIGTERM
   ↓ grace
SIGKILL
   ↓
cleanup proof
~~~

`process.Run` 返回 `TermSent / KillSent / Cleanup`，Worker 在发生 escalation 时产生：

~~~text
attempt.stop_escalation
~~~

事件，包含 runtime/verifier phase、TERM、KILL 和 cleanup_confirmed。

只有 cleanup proof 成立时，已有 Retry/Workspace/Artifact 语义才允许继续安全推进；cleanup unknown 仍保持 fail-closed。

### 2.5 Task worker events 有界保留

Server schema v7 为 Task 增加 `event_floor_seq`，并增加紧凑的 `event_dedup(attempt, worker_seq, hash)`。

Server 只保留最近 `max_task_events` 条 Task event payload，默认 2000。

删除旧 payload 后：

- Task `seq` 不回退；
- `event_floor_seq` 单调前移；
- worker event hash 仍保存在 `event_dedup`，所以断线重放仍能验证相同 worker_seq 的内容；
- `WatchEvents(after_seq < floor)` 返回明确的：
  `EVENT_CURSOR_COMPACTED floor=N`。

客户端必须重新读取 Task/Job snapshot，并从新的 cursor 继续，而不是把缺失的历史事件当作完整流。

v0.3.4 不压缩 `job_events`；Job control/idempotency 记录继续完整保留。

## 3. Schema v7

Server schema v7 增加：

~~~text
attempts.last_renewed
tasks.event_floor_seq
event_dedup(attempt, worker_seq, hash)
~~~

Migration 会把现有 Attempt 的 `last_renewed` 初始化为当前 `lease_until`，并从已有 worker events 回填 dedup hash。

## 4. Invariants

~~~text
no-output != hung
valid lease renewals sustain execution until explicit deadline/cancel
deadline never extends implicitly
deadline extension is monotonic, bounded and idempotent
Retry budget/generation are unchanged by deadline extension
cancel/timeout cannot publish success
cleanup proof remains mandatory
event payload compaction never weakens worker replay dedup
old compacted cursors fail explicitly
~~~

## 5. Non-goals

- 真实 24h Production Baseline；
- Runtime session resume/checkpoint；
- interactive input / approval；
- generic workflow timer；
- user-defined arbitrary retention policy；
- Job event retention/GC；
- HA。

真实 24h+、独立主机和真实 Codex/Claude 长任务仍由 Production Baseline #1 验收。
