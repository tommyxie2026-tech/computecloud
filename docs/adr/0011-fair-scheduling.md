# ADR-011：Fair Scheduling、Priority Aging 与 Queue Backpressure

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.3.5
- Issue：[#23](https://github.com/tommyxie2026-tech/computecloud/issues/23)
- 依赖：[ADR-010：Long-running Job Reliability](0010-long-running-job-reliability.md)

## 1. Context

v0.2 已有 priority、Project 并发、Credential 并发、Job parallelism 和 Worker slot 约束；早期 Scheduler 还通过 Job/group rotating cursor 避免一个 Job 长期独占单个新空闲 slot。

随着 v0.3.0–v0.3.4 完成多 Attempt、Retry、Artifact/Workspace lifecycle 和长任务，排队时间会明显拉长。如果继续只按固定 priority 和单一 group cursor 扫描，会出现：

- 同一 Project 拥有大量 Job 时，可能长期压制其他 Project；
- 持续到来的高优先级任务可能让低优先级任务永久 starvation；
- 队列没有 admission bound，控制面可以被无限排队请求占满；
- Credential、Project、Worker slot 都可能阻塞，但如果都报告为同一个 CAPACITY_EXHAUSTED，运维不可解释。

v0.3.5 的目标不是提前实现 v0.5 的 Agent-aware Scheduler，而是完成 Reliability Kernel 所需要的最小公平排队和过载保护。

## 2. Decision

### 2.1 分层公平顺序

~~~text
effective priority
   ↓
Project round-robin
   ↓
Job/group round-robin
   ↓
oldest runnable Task
   ↓
assign hard constraints
~~~

候选组扫描有界，单次最多读取 512 个 runnable group。

assign() 继续是最终硬约束事实源：Runtime/capability、Job parallelism、Credential、Project concurrency、Worker health/slots 等都不能被公平排序绕过。

### 2.2 Priority aging

~~~text
effective_priority =
  min(10,
      base_priority +
      floor(queue_age / scheduler_aging_seconds))
~~~

默认 scheduler_aging_seconds = 300。

Aging 只改变候选顺序，不改变 Task 的持久 priority，也不改变 Job/Task deadline。

下列对象不会因为 aging 进入候选集合：retry_after > now、deadline <= now、非 QUEUED，以及之后在 assign 中被 capability/security/concurrency constraint 拒绝的 Task。

因此 aging 是 starvation prevention，不是 constraint override。

### 2.3 Project 与 group rotation

同 effective priority 内按 Project round-robin。每个 Project 内按 Job/group round-robin。Standalone Task 将自身 Task ID 作为 group。

Server 只保存轻量 in-memory cursor：projectCursor[effectivePriority] 和 groupCursor[effectivePriority,project]。

Cursor 不是 durable correctness state。Server 重启后从稳定 DB queue 状态重新开始公平轮转是允许的；它不影响 Job/Attempt 正确性。

### 2.4 Queue backpressure

新增 external admission limits：

~~~text
max_queued_tasks = 4096
max_queued_tasks_per_project = 1024
~~~

只对新的外部提交生效：SubmitTask +1；single Job +1；map_reduce Job 增加初始 Map partition 数。

Internal Reduce creation 不因 queue admission limit 被拒绝，因为已接受 Job 必须能继续执行自己的有限 Stage。

幂等 replay 必须先查询已接受对象，再做 admission check。因此已经成功接收的 request，即使此时队列已满，也仍返回原对象，而不是 ResourceExhausted。

过载错误为 QUEUE_BACKPRESSURE_GLOBAL 与 QUEUE_BACKPRESSURE_PROJECT。

### 2.5 Blocker taxonomy

容量 blocker 拆分为：

~~~text
JOB_CAPACITY_EXHAUSTED
PROJECT_CONCURRENCY_EXHAUSTED
CREDENTIAL_CONCURRENCY_EXHAUSTED
WORKER_CAPACITY_EXHAUSTED
NO_READY_WORKER
TEMPLATE_OR_CAPABILITY_MISMATCH
~~~

这些 blocker 是可观察的调度解释，不改变 Task state。

### 2.6 No preemption

v0.3.5 不抢占已经运行的 Attempt。Priority/Aging 仅决定“下一个可用执行机会给谁”。已经获得 lease 的 Attempt 继续遵守其 lease/deadline/cancel 语义。

## 3. Invariants

~~~text
equal-priority runnable Projects make bounded progress
one Project cannot permanently own every newly freed slot
low-priority work eventually reaches the highest effective priority
aging never bypasses retry_after or deadline
aging never bypasses hard execution constraints
queue admission is bounded
idempotent replay survives queue saturation
concurrency blockers are specific
no running Attempt is preempted
~~~

## 4. Rejected alternatives

Weighted fair queue / DRF：暂不引入。当前资源语义仍以 slots/account/project 为主，尚未进入 CPU/Memory/GPU 或 Agent-aware resource scoring 阶段。

Durable scheduler cursor：拒绝。公平 cursor 丢失只影响短期排序，不影响 correctness。为此写 DB 会增加没有必要的写放大。

Priority preemption：拒绝。终止正在运行的 Agent 需要 side-effect、安全重试和成本模型，超出 v0.3.5。

Queue message broker：拒绝。SQLite Task state 已经是 durable queue 事实源，不为公平调度提前引入 Redis/Kafka。

## 5. Consequences

正向：多 Project 共享 Worker 时具备明确公平性；长期低优先级任务有 starvation 上界；控制面过载时能够拒绝新工作；blocker 更可解释；不引入新基础设施或领域对象。

代价：Scheduler 每个 tick 要做 bounded group scan 和内存排序；cursor 非持久，Server restart 后短期公平顺序重置；Aging 仍是简单离散 boost，不是 v0.5 的 Agent-aware scoring。

## 6. Non-goals

workspace/repository affinity、Runtime warmness scoring、Credential routing optimization、CPU/Memory/GPU bin packing、preemption、distributed scheduler / HA、queue policy DSL 均不属于 v0.3.5。