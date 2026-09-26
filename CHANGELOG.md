# 版本记录

## 未发布 — v0.3.1 Retry Safety

- Job Execution 增加显式 `replay_safe`；当 `max_attempts_per_task > 1` 时禁止隐式推断可重放性。
- `max_attempts_per_task` 支持 1..3；Retry budget 不重置 Job/Task 原 deadline。
- Server 使用固定 retryable error allowlist，只有 cleanup-confirmed 的 replay-safe Job Task 才能自动创建下一 generation。
- Server schema 升级到 v5，持久化 `tasks.retry_after`，Scheduler 在 backoff 到期前不会重新 dispatch。
- 增加 `task/job.retry_scheduled` 与 `task/job.retry_exhausted` 事件，并保留完整 Attempt history。
- v0.3.0 generation / Worker epoch fencing 和 Artifact accepted-only 路径继续作为 Retry 的安全边界。
- 新增独立 GitHub Actions `retry-flow`，覆盖 retry-once、budget exhaustion、non-retryable failure、replay-safe gate、schema/integrity。
- Release package 现在必须同时通过 verify、原 task-flow 和 retry-flow。
- 真实 Codex / Claude、独立主机、真实 MCP、24h+ Job 和真实 Runtime 容量仍由 Production Baseline #1 独立验收。


## 未发布 — v0.3.0 Reliability Kernel I

- Server schema 升级到 v4：新增一等 Stage 记录，并把现有 single / map / reduce 迁移到 Stage。
- Attempt 从 `UNIQUE(task)` 演进为 `UNIQUE(task,generation)`，保留历史 Attempt，同时通过 partial unique index 保证每个 Task 最多一个 active Attempt。
- Assignment generation 改为事务性递增，Task 持久化 current generation / current attempt；Worker 写入增加 current-generation 与 Worker epoch fencing。
- Worker 重启后的旧 epoch 只允许提交 cleanup-only 的失败证明，不能继续上报执行事件、成功结果或 Artifact。
- Artifact 增加 generation 和 STAGED / ACCEPTED / ORPHANED 状态；Reduce、Job Result 和正式下载只接受当前 generation 的 ACCEPTED Artifact。
- Map/Reduce 继续保持 v0.2 API 兼容，但内部建立 Stage lifecycle，为后续有限 fan-out/barrier/fan-in 留出稳定边界。
- 新增 Stage / multi-Attempt / fencing 测试，并保持 CI task-flow、vet、unit、race、smoke、capacity-check 回归。
- 自动 Retry 仍未启用；`max_attempts_per_task` 继续报告 1。v0.3.1 才进入 Retry Safety。

### 仍待生产 Gate

真实 Codex / Claude、独立双机、真实 MCP、24h+ Job 和真实 Runtime 容量仍属于 v0.2.1 Production Baseline（Issue #1）。在这些证据完成前，v0.3.0 只视为代码/自动化验收完成，不宣称生产发布完成。

## 0.2.x — CI 端到端任务流

- 增加独立 GitHub Actions `task-flow`：使用真实 Server/Worker 进程和 Codex/Claude 协议 fixture 模拟 single、Map/Reduce、取消与 Worker 故障。
- 输出包含 Job/Attempt、事件、Worker 分配、产物 SHA-256 和 SQLite 完整性的 `ci-task-flow.v1` 报告；发布打包依赖该流程通过。

## 0.2.0 — 2026-09-24

- 实现 Job HTTP API、官方 Go SDK MCP 四工具、Job CLI 和受信模板摘要导出。
- 实现 single、显式 Map/Reduce、按 Job 轮转、并发与共同期限、持久取消及清理屏障。
- 实现冻结产物清单、Reduce 专用授权下载、哈希/tar 检查、报告证据/来源校验和受信补丁合并验收。
- 增加默认关闭的 Responses JSON/SSE/compact 网关、固定路由、Attempt 模型 Token、在途撤销、并发准入与可观察未知用量。
- 增加 Server schema v2/v3 与 Worker schema v2 的迁移门槛、旧库兼容、备份不升级源库。
- 增加 HTTP/MCP/网关、跨执行器、恢复/存储故障测试及 Job CLI 二进制烟测；更新运行指南、设计契约与进度。
- 增加零额外依赖的本机容量矩阵、CI 小矩阵和完整指标口径；不修改服务依赖或数据库版本。
- 增加 Linux amd64/arm64 发布包、SHA-256 清单、主分支 CI artifact、标签 Release 和正式部署指南。

本地 fixture、模拟 HTTPS 上游、race、二进制故障测试和 fixture 容量矩阵通过。真实模型、独立主机及真实模型容量/账单对照仍待目标环境验收。自动重试 R1 继续延后；不增加 PostgreSQL、Redis、外部队列或控制面 HA。

## 0.1.0 — 2026-09-22

首个可构建运行版本，适用于受信 Linux 节点的批任务调度。

- Go 单二进制，内置 gRPC server、Worker、任务/产物 CLI、离线备份；SQLite 无外部数据库服务。
- 静态模型和凭据允许列表、节点能力匹配、节点/账号/项目并发限制、提交幂等与持久命令。
- Codex exec、Claude print 的非交互参数与 JSONL 适配；固定版本检查、独立 Git 工作区、进程组监督、可信验收和结果包。
- 事件回放/补传、重复请求冲突检查、持久停止墓碑、租约、崩溃恢复与清理确认后释放额度。
- TLS、用户/节点令牌、任务所属身份与项目授权、产物大小和哈希检查。
- 示例配置、实施进度、运行/恢复指南、Go 测试、二进制故障烟测和 CI 配置。

真实模型账号和两台独立机器尚未验收。Session resume、交互输入/审批、自动重试、通用 DAG、容器启动器、历史 GC、控制平面 HA 均未实现；详细边界见运行指南。
