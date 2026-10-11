# Runtime `bwrap` 状态诊断记录

日期：2026-10-11

## 结论

当前证据不足，无法判定曾出现的 `bwrap` 文本属于结构化 Runtime 失败，还是 Agent 在普通回答中说明环境限制。此次实现工作区没有关联到该次运行的 Job/Task ID、Server 事件导出、Worker `report.json` 或可读取的 Worker 日志；因此不对成功状态语义作代码修改，也不把自然语言内容当作失败信号。

## 证据清单

| 证据 | 来源 | 状态 |
|---|---|---|
| Job/Task ID 与最终状态 | 当前引用会话的可读上下文 | 未提供 |
| `attempt.started` / `attempt.completed` 及清理字段 | `GET /v1/jobs/{id}/events` 或事件导出 | 无法获取，缺少 Job ID 与 Server 访问凭据 |
| Runtime 退出码、协议终态、清理确认 | Worker `report.json` 或 Worker 结构化日志 | 未提供 |
| Agent 最终文本中的 `bwrap` 提及 | 任务对话或结果包 | 无可定位的具体任务正文/结果包；不复制或推断 |

未将请求正文、Token、凭据、主机地址或私有路径写入本记录。

## 状态判定规则

Worker 当前仅在以下结构化条件全部满足时报告成功：进程无错误且退出码为 0、Runtime 清理已确认、无协议错误、存在最终事件且该事件报告成功。现有实现位于 `internal/worker/execute.go`。单凭最终文本提到 `bwrap` 不足以推翻这些结构化状态。

## 后续补证

若需完成该次运行的根因判定，应提供不含敏感正文的 Job ID 与对应脱敏事件/报告字段：进程退出码、Runtime final/success、协议错误码、清理状态、最终 Job 状态及事件时间戳。拿到这些证据后再针对确认的结构化失败添加回归测试。
