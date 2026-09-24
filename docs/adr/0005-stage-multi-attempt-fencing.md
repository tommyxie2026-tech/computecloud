# ADR-005：Stage、Multi-Attempt 与 Generation Fencing

- 日期：2026-09-24
- 状态：Accepted
- 版本：v0.3.0 Reliability Kernel I
- 依赖：ADR-003、ADR-004

## Context

v0.2 已经具有 Job / Task / Attempt、lease、heartbeat、generation 字段和 single/MapReduce，但仍有两个结构限制：

- tasks.stage 只是 single/map/reduce 字符串，Stage 不是一等持久化对象；
- attempts.task 为 UNIQUE，实际只能保存一个 Attempt，Assignment generation 固定为 1。

这使 Retry Safety 无法建立在可审计的历史 Attempt 上，也无法形式化地拒绝旧 generation 的迟到写入。

## Decision

v0.3.0 将内部模型正式化为：

~~~text
Job -> Stage -> Task -> Attempt(generation)
~~~

Stage 仅表达 Agent Job 内有限的阶段与屏障，不引入通用 Workflow DSL。

一个 Task 可以拥有多个历史 Attempt，但通过 partial unique active index 和 Task current-generation anchor 保证最多一个 active Attempt。

Worker 的 lease renew、event、Artifact、completion 等有效写入必须匹配当前 attempt/generation。Worker epoch 变化后，旧 epoch 不能继续产生有效执行结果。

Worker restart 是一个特殊 cleanup 场景：新 epoch 可以为旧 Attempt 提交 WORKER_RESTARTED + cleanup_confirmed=true 的失败证明，但不能提交 success、Artifact 或继续 execution event。这样既保留 fencing，又允许旧执行安全释放资源。

Artifact 在 v0.3.0 引入最低生命周期：STAGED / ACCEPTED / ORPHANED。只有当前 generation 的 ACCEPTED Artifact 可以进入 Reduce、Job Result 或正式下载。

## Consequences

正面影响：
- 为 v0.3.1 Retry Safety 提供安全基础；
- 旧 Attempt 可保留审计历史而不影响 current Task；
- Map/Reduce 屏障开始由 Stage lifecycle 表达；
- stale Worker / stale generation 不能发布正式结果。

代价：
- Server schema 从 v3 升级到 v4；
- v0.2 旧二进制不能直接打开 v4 数据库；
- rollback 必须恢复升级前备份；
- Worker restart cleanup 需要区分 execution ownership 与 cleanup proof。

## Non-goals

本 ADR 不启用自动 Retry，不引入任意 DAG，也不改变 Runtime/Tool/Environment 产品边界。
