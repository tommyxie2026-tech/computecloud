# Runtime 失败状态诊断实施计划

> **执行说明：** 使用 `superpowers:executing-plans` 按任务逐项实施；步骤使用复选框跟踪。

**目标：** 查清 `bwrap` 错误文本与任务成功状态同时出现的原因，并只在结构化证据证明状态错误时修正成功判定。

**架构：** 先从现有 Job 事件、Worker `report.json`、进程退出码、协议终态和清理结果还原故障路径。现有 Worker 已组合检查进程错误、退出码、清理确认、协议错误、最终事件和最终成功标记；若这些信号一致地报告成功，就不通过匹配最终文本来改写状态。

**技术栈：** Go、Worker Runtime Adapter、事件/产物测试夹具、CI。

**设计文档：** [2026-10-11-worker-performance-and-balancing-design.md](../specs/2026-10-11-worker-performance-and-balancing-design.md)

## 全局约束

- 未确认结构化根因前，不改变 Runtime 成功判定。
- 不把 stdout、stderr 或 Agent 的最终文本当作唯一失败依据。
- 未确认清理完成的 Attempt 不能被标记为成功。
- 诊断产物必须脱敏，不保存 Token、凭据、提示正文或用户私有文件。
- 不通过给容器增加特权或关闭安全沙箱来规避 `bwrap` 故障。

## 重点复核场景

1. 进程非零退出但 Runtime final 成功时仍判失败；由 `TestRuntimeNonZeroExitCannotSucceed` 覆盖。
2. 缺失最终事件时返回 `MISSING_FINAL`；由 `TestRuntimeMissingFinalCannotSucceed` 覆盖。
3. Runtime 协议错误时返回 `PROTOCOL_ERROR`；由 `TestRuntimeProtocolErrorCannotSucceed` 覆盖。
4. 清理状态未知时不能成功；由 `TestRuntimeUnconfirmedCleanupCannotSucceed` 覆盖。
5. Agent 的普通文本提到 `bwrap` 但结构化 Runtime 成功时不误判失败；由 `TestRuntimeTextMentionDoesNotChangeOutcome` 覆盖。

---

### Task 1：从既有任务证据还原失败路径

**文件：** 不改产品代码；诊断记录写入 `docs/validation/runtime-failure-diagnosis-2026-10-11.md`。

**输入：** 最近一次显示 `bwrap` 的 Job/Task ID、Job 事件、结果包 `report.json`、Runtime 退出状态和清理状态。

- [x] 检查当前会话可读上下文及本地可用材料；没有 Job ID、Server 事件导出、Worker `report.json` 或日志，无法访问真实 Server。
- [x] 在诊断记录中列出缺失证据，不记录正文、Token、路径和主机地址。
- [x] 结论为“缺少证据”；不推断 Runtime 状态根因。
- [x] 因无法取得退出码、协议终态或清理状态，停止状态语义改动，并记录限制。

### Task 2：针对已确认的结构化失败补回归测试

**文件：**

- 测试：`internal/worker/execute_test.go`（若不存在则新建）或最接近 `execute.go` 的现有 Worker 测试文件。
- 必要时修改：`internal/worker/execute.go`。

- [x] 不适用：任务 1 没有获得可确认的结构化根因，因此不新增误导性的根因回归测试。
- [x] 不适用：没有可复现故障，不能声称修复前测试失败。
- [x] 不适用：不改动已存在的结构化成功条件和清理约束。
- [x] 既有 Worker 测试覆盖结构化执行结果与成功条件；本次不增添状态语义改动。

### Task 3：验证并记录结果

- [x] 运行 `go test ./internal/worker ./internal/server`。
- [x] 运行 `go test ./...`。
- [x] 运行 `git diff --check`。
- [x] 更新诊断记录：目前属于证据获取限制；不提交成功语义变更。

