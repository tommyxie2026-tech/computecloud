# 对话请求大小与阶段观测实施计划

> **执行说明：** 使用 `superpowers:executing-plans` 按任务逐项实施；步骤使用复选框跟踪。

**目标：** 让 Claude Messages 请求的字节大小可追踪，并在请求超过配置上限时返回明确、不会回显正文的错误。

**架构：** HTTP 接入层保留现有硬上限，不自动截断或压缩上下文；接受的请求把原始请求体字节数作为元数据持久化到 `conversation_requests`，并在请求状态查询中显示。阶段耗时从已有任务/Attempt 事件时间戳计算，不保存提示正文或附件内容。

**技术栈：** Go、SQLite schema migration、HTTP JSON API、现有 Job/Attempt 事件和 Trace 查询。

**设计文档：** [2026-10-11-worker-performance-and-balancing-design.md](../specs/2026-10-11-worker-performance-and-balancing-design.md)

## 全局约束

- 不自动裁剪或总结会话上下文，不改变调用方发送的消息。
- 继续遵守 `ConversationJobs.MaxRequestBytes` 和 Profile 的输入限制。
- 新增数据只记录字节数与阶段时间，不记录请求正文、附件内容、Token 或凭据。
- 新增 HTTP 错误字段必须向后兼容；错误正文不得包含收到的请求片段。
- SQLite 升级必须可重入、事务化，并保留 Server schema v17 的既有数据。

## 重点复核场景

1. 请求刚好达到配置上限仍可处理，超过 1 字节明确拒绝；由 `TestConversationRequestLimitIncludesExactBoundary` 覆盖。
2. Chunked 请求没有 `Content-Length` 仍能安全拒绝；由 `TestConversationOversizeChunkedRequest` 覆盖。
3. 超限错误包含配置上限但不回显正文；由 `TestConversationOversizeErrorDoesNotEchoBody` 覆盖。
4. 幂等重放保存并返回同一 `request_bytes`，且不重复创建 Job；由 `TestConversationRequestBytesPersistAcrossIdempotentReplay` 覆盖。
5. 历史 schema v17 升级后旧记录的 `request_bytes` 为 0，且数据库可再次打开；由 `TestV18AddsConversationRequestBytes` 覆盖。

---

### Task 1：扩展 Conversation Request 存储与迁移

**文件：**

- 修改：`internal/store/conversation_migration.go`
- 修改：`internal/store/migrations.go`
- 修改：`internal/store/db.go`
- 新建或修改：`internal/store/conversation_migration_test.go`

**接口：**

- schema v18 为 `conversation_requests` 增加 `request_bytes INTEGER NOT NULL DEFAULT 0 CHECK(request_bytes >= 0)`。
- `conversationRecord` 增加 `RequestBytes int64`。

- [x] 新增 `TestV18AddsConversationRequestBytes`，从 v17 数据库升级并验证旧记录默认为 0、已有字段不变、重开成功。
- [x] 先运行迁移测试确认字段/迁移缺失，再实现迁移。
- [x] 增加 serverV18 迁移并将 Server schema 目标版本更新为 18。
- [x] 重跑迁移测试，确认通过。

### Task 2：记录接入字节数并改进超限错误

**文件：**

- 修改：`internal/server/conversation_http.go`
- 修改：`internal/server/conversation_service.go`
- 修改：`internal/server/http.go`
- 修改：`internal/server/conversation_http_test.go`

**接口：**

- `beginConversationRequest` 增加 `requestBytes int64` 参数，并在幂等记录创建时写入 `request_bytes`。
- `GET /agent/v1/requests/{id}` 响应增加 `request_bytes` 数值字段。
- 对 `REQUEST_TOO_LARGE` 错误增加稳定的 `limit_bytes` 字段；不得返回请求正文或其前缀。

- [x] 新增上述边界、Chunked、错误不回显及幂等测试。
- [x] 先运行专项测试确认新断言失败，再实现记录与错误响应。
- [x] 将 `len(body)` 传入 Service，写入并读出 `request_bytes`；为受限请求错误增加 `limit_bytes`，保留现有状态码和错误码。
- [x] 重跑专项测试，确认通过。

### Task 3：在 Trace 中输出阶段耗时

**文件：**

- 修改：`internal/server/trace.go`
- 修改：`internal/server/trace_test.go`
- 如现有事件缺少阶段标记，再修改：`internal/server/tasks.go`、`internal/worker/runtime_execution.go`

**接口：**

- `JobTrace` 增加 `stage_durations_ms`，以命名阶段和毫秒数表示可从已有事件推导出的阶段耗时；没有足够事件时返回 `null`，不伪造 0。
- 阶段标记优先使用现有 `recorded_at_ms`，不另存请求文本或敏感字段。

- [x] 新增 `TestJobTraceReportsStageDurations` 和 `TestJobTraceLeavesMissingDurationsNull`。
- [x] 先运行 Trace 专项测试确认实现缺失，再从已有 Job/Task/Attempt 事件计算阶段耗时。
- [x] 由已有事件计算入队、派发、Runtime 和结果收尾耗时，不增加冗余事件。
- [x] 重跑 Trace 专项测试；按独立审查建议将 `runtime_ms` 明确定义为 Attempt 覆盖区间的墙钟耗时。

### Task 4：全量迁移与回归验证

- [x] 运行 `go test ./internal/store ./internal/server`。
- [x] 运行 `go test ./...`。
- [x] 运行 `git diff --check` 并检查迁移兼容性测试。
- [x] 提交本计划涉及的迁移、HTTP、Trace 代码与测试，提交信息使用 `feat(telemetry): expose conversation request size and stage timings`。

