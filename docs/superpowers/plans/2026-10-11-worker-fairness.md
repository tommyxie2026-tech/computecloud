# Worker 负载均衡实施计划

> **执行说明：** 使用 `superpowers:executing-plans` 按任务逐项实施；步骤使用复选框跟踪。

**目标：** 同等负载时轮流选择合格 Worker，同时保留现有最少活跃任务优先策略和所有资格限制。

**架构：** 在 Server 的调度分配过程中先完成现有 Worker 在线、能力、模板、凭据、项目和槽位检查，再将最低活跃数的候选者按 Worker ID 排序并轮换。游标只在数据库分配事务成功后推进，不持久化，重启后允许重新开始。

**技术栈：** Go、SQLite、现有 Server 调度器与测试夹具。

**设计文档：** [2026-10-11-worker-performance-and-balancing-design.md](../specs/2026-10-11-worker-performance-and-balancing-design.md)

## 全局约束

- Worker 在线检查、Runtime/能力/模板匹配、凭据与项目并发配额、Worker 槽位限制必须先于轮换。
- 不改变最低活跃任务数优先规则；只改变最低负载候选者之间的平局结果。
- 只有成功提交分配事务后才推进轮换游标。
- 不增加调用方可控的 `worker_id` 或持久 Worker 亲和性。

## 重点复核场景

1. Peer 到达顺序反转仍产生相同的轮换结果；由 `TestAssignRotatesEqualLoadWorkers` 覆盖。
2. 一台 Worker 已满槽位时不会因轮换被选中；由 `TestAssignSkipsWorkerAtCapacity` 覆盖。
3. 能力或模板不匹配的 Worker 不进入轮换集合；由 `TestAssignExcludesIncompatibleWorkerFromRotation` 覆盖。
4. 活跃任务数不相同时仍选择负载最低者；由 `TestAssignPrefersLowerActiveLoad` 覆盖。
5. 事务提交失败时游标不前移；由 `TestAssignDoesNotAdvanceCursorOnFailure` 覆盖。

---

### Task 1：实现最低负载候选者轮换函数

**文件：**

- 新建：`internal/server/worker_selection.go`
- 修改：`internal/server/fair_scheduling_v35_test.go`

**接口：**

- 输入：`[]workerLoad`，每项含 `*session` 和活跃任务数；上一次成功选择的 Worker ID。
- 输出：被选中的 `*session` 和下一游标 Worker ID；无候选者时返回空选择。

- [x] 新增测试 `TestChooseWorkerRotatesEqualMinimumLoad`、`TestChooseWorkerUsesMinimumLoadBeforeRotation`、`TestChooseWorkerIgnoresPeerOrder`。
- [x] 先运行专项测试确认初始实现缺失，再实现调度函数。
- [x] 新建 `workerLoad` 类型和 `chooseLeastLoadedWorker` 函数；函数只在最低活跃数集合中按 Worker ID 轮换。
- [x] 重跑上述测试，确认所有用例通过。

### Task 2：将轮换接入实际分配事务

**文件：**

- 修改：`internal/server/server.go`
- 修改：`internal/server/fair_scheduling_v35_test.go`

**接口：**

- `Server` 增加仅进程内的 `workerCursor string`。
- `assign` 在数据库事务成功提交后更新游标；事务回滚时保持原值。

- [x] 新增 `TestAssignRotatesEqualLoadWorkers`，使用两个有效 Peer 连续提交相同条件任务，检查 Worker 选择轮流变化。
- [x] 在 `assign` 中收集通过现有所有过滤条件的候选者及其活跃数，调用 `chooseLeastLoadedWorker`；只在事务成功后更新 `workerCursor`。
- [x] 添加并通过 `TestAssignSkipsWorkerAtCapacity`、`TestAssignExcludesIncompatibleWorkerFromRotation` 和事务失败不推进游标测试；最低负载选择另由纯函数测试覆盖。
- [x] 运行调度专项测试，确认通过。

### Task 3：完整验证与提交

- [x] 运行 `go test ./internal/server`。
- [x] 运行 `go test ./...`。
- [x] 检查 `git diff --check`，确认没有格式错误或未跟踪的临时文件。
- [x] 提交本计划涉及的调度代码与测试，提交信息使用 `fix(scheduler): rotate equal-load worker selection`。

