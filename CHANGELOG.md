# 版本记录

## 未发布 — v0.4.3 EnvironmentCapability Foundation

- 新增独立 `internal/environment` Registry，Descriptor 包含 name / version / isolation_class / filesystem_mode / network_mode / legacy_default。
- 首个内建 Environment 为 `process`，保持当前可信 Worker process + workspace 执行模式。
- Worker 广告改为 Runtime-compatible Environment 与已注册 Environment 的交集；未注册 Environment 不进入 WorkerHello。
- Job Execution 新增可选 `environment`；省略时等价 `process`，接受后冻结为 `environment:<name>` Task required capability。
- Worker Policy 新增 `allowed_environments`；Environment 注册/兼容/Policy 校验在 Workspace prepare 与 Runtime Prepare/Start 前 fail closed。
- `allowed_environments` 进入既有 TemplateDigest，Policy 改动继续受 template mismatch fencing 保护。
- Scheduler 继续只处理 generic `required_capabilities`，不增加 Environment 名称特例。
- 新增独立 `environment-contract-flow`；release package 增加该 Gate 依赖。
- container/VM/external sandbox execution、Prepared Workspace、network isolation 与 v0.5 scoring 不属于 v0.4.3。
## 未发布 — v0.4.2 ToolCapability Foundation

- 新增独立 `internal/tool` Registry，Tool descriptor 包含 name / version / side_effect / legacy_default；Tool existence 不再只由 Runtime Provider 声明。
- 首批内建 platform ToolCapability 为 `artifact_inputs_v1` 与 `job_io_v1`；本版本不增加通用外部 Tool execution proxy。
- Worker 广告改为 Runtime-compatible Tools 与实际 Tool Registry 的交集；未注册 Tool 不进入 WorkerHello。
- Job Execution 新增可选 `tools[]`，接受后冻结为 `tool:<name>` Task required capability，继续复用 generic Scheduler capability matching。
- Worker Policy 新增 `allowed_tools`；Tool 注册/兼容/Policy 校验在 Runtime Prepare/Start 前 fail closed，Policy 拒绝返回 `TOOL_POLICY_DENIED`。
- Standalone Task 支持有界 `runtime:* / tool:* / environment:*` namespaced capability，同时保留 legacy event_stream/cancel compatibility。
- 非空 `allowed_tools` 自动进入既有 TemplateDigest，Policy 改动继续受 Server/Worker template digest fencing 保护。
- 新增独立 GitHub Actions `tool-contract-flow`，并要求 release package 依赖既有九个 Gate + tool-contract-flow。
- Tool side_effect 目前只做描述，不自动推断 replay_safe、Retry 或审批决策。
- 真实外部 Tool invocation、interactive approval、EnvironmentProvider、Prepared Workspace、v0.5 scoring 不属于 v0.4.2。

## 未发布 — v0.4.1 Transport-neutral Runtime Execution（实现完成，待版本发布）

- Runtime Provider contract 增加 Version / Transport / Prepare / Start / Inspect / Stop，使 Agent execution transport 不再由 Worker 按 profile 或 PID 模型决定。
- 新增 transport-neutral ExecutionRef、RuntimeState 与 CleanupState；remote/API-backed Runtime 可使用稳定远端 ID 而无需本地 PID。
- Worker schema 升级到 v4，持久化 runtime_provider / runtime_transport / runtime_ref / runtime_state / runtime_cleanup。
- Codex/Claude 保持 local_cli 行为，但 Agent process launch、parser 和 cleanup semantics 通过 Provider bridge 封装；可信 Verifier 仍是独立 Worker local process。
- Codex Gateway CLI/env mutation 移入 Codex Provider Prepare，Worker 不再拼接 Codex-specific model-provider 参数。
- Worker restart 对有 durable runtime_ref 的记录执行 Provider Inspect/Stop；UNKNOWN cleanup fail closed，不自动 resume。
- 增加 remote/API-backed fixture，证明 Worker 可以在没有本地 Agent PID 的情况下完成 Runtime execution contract。
- Built-in CLI Runtime 新增 `runtime:local_cli` capability；remote fixture 使用 `runtime:remote_api`。
- 新增独立 GitHub Actions `runtime-execution-flow`；release package 依赖既有八个 Gate + runtime-execution-flow。
- Session resume、Approval、Tool execution framework、EnvironmentProvider、Prepared Workspace 和 v0.5 scheduling 不属于 v0.4.1。
## 未发布 — v0.4.0 Runtime API v2（实现完成，待版本发布）

- Runtime 接入从 Worker profile 特例改为注册式 Provider Registry；内置 `codex_exec` 与 `claude_print` 作为 Provider。
- Provider contract 统一 `Profile / Probe / Args / Parser / Capabilities / SupportsGateway`，新增 Runtime 不需要修改 Scheduler profile 分支。
- Worker probe 与 execute 路径通过 Registry 发现 Provider；unknown Runtime 在执行前 fail closed。
- Capability 开始使用 `runtime:* / tool:* / environment:*` 命名空间，同时继续广告 legacy capability 以保持 v0.3.x JobSpec 兼容。
- Model Gateway eligibility 由 Provider 声明，不再通过 `runtime_profile == codex_exec` 判断。
- 新增 Runtime contract 测试，覆盖第三方 fixture Provider 注册、deterministic capability、namespaced Scheduler match 与 legacy compatibility。
- 新增独立 GitHub Actions `runtime-contract-flow`，并静态检查 Worker/Server 核心文件不重新引入 Codex/Claude profile 特例。
- Release package 依赖 verify、task-flow、retry-flow、artifact-flow、workspace-flow、long-run-flow、fair-flow、runtime-contract-flow 八个 Gate。
- v0.4.0 不实现 API-backed Runtime、EnvironmentProvider、Prepared Workspace、interactive approval 或 breaking protobuf migration。
## 未发布 — v0.3.5 Fair Scheduling（实现完成，待版本发布）

- Scheduler 从固定 priority + 单 group cursor 演进为 `effective priority -> Project round-robin -> Job/group round-robin -> Task` 的有界公平队列。
- JobSpec 新增向后兼容的可选 `limits.priority`（0..10，省略等价 0），并明确传播到 managed Task 的 TaskSpec 与持久 `tasks.priority`。
- 新增 priority aging：`effective=min(10, base+floor(queue_age/scheduler_aging_seconds))`；默认 300 秒提升 1 级，但不改变持久 priority。
- `retry_after`、deadline 和硬 capability/concurrency 约束先于 aging，低优先级提升不会绕过安全边界。
- 新增 `max_queued_tasks=4096` 与 `max_queued_tasks_per_project=1024` external admission backpressure；已接受请求的 idempotent replay 不受之后队列饱和影响。
- Job admission 按初始 Task 数原子检查；已接受 Job 的 internal Reduce Stage 不被 external queue limit 阻断。
- blocker 细分为 `JOB_CAPACITY_EXHAUSTED / PROJECT_CONCURRENCY_EXHAUSTED / CREDENTIAL_CONCURRENCY_EXHAUSTED / WORKER_CAPACITY_EXHAUSTED / NO_READY_WORKER / TEMPLATE_OR_CAPABILITY_MISMATCH`，区分健康 Worker 不存在、能力不匹配与 slot 饱和。
- 单次 Scheduler candidate group scan 上限 512；公平 cursor 只存在内存，Server restart 允许短期顺序重置而不影响 durable correctness。
- 新增独立 GitHub Actions `fair-flow`；package 依赖 verify、task-flow、retry-flow、artifact-flow、workspace-flow、long-run-flow、fair-flow 七个 Gate。
- v0.3.5 不引入 preemption、DRF、CPU/GPU placement、Workspace affinity 或 Agent-aware score；这些属于后续 Scheduler 演进。
## 未发布 — v0.3.4 Long-running Job Reliability（实现完成，待版本发布）

- Server schema 升级到 v7：Attempt 增加 `last_renewed`，Task 增加 `event_floor_seq`，新增紧凑 `event_dedup` replay hash 索引。
- Worker Renew 成为 Attempt liveness 的事实依据；Runtime 长时间无 stdout/stderr 不再等同于 hung。
- Worker 不再使用 Assignment 的冻结 deadline 作为本地固定 timer；Server 通过 mutable Job/Task deadline、lease 和 stop command 保持执行权威。
- 新增 HTTP `POST /v1/jobs/{id}/deadline` 与 MCP `extend_job_deadline`，要求 `jobs:extend` scope，支持 operation-id 幂等、单调延长、单次上限与总运行时长上限。
- Deadline extension 同步更新未终态 child Task，但不改变 Retry budget、Attempt generation、Artifact 或 Workspace ownership。
- process group 停止路径记录 SIGTERM/SIGKILL escalation，Worker 产生 `attempt.stop_escalation` 证据；cleanup unknown 继续 fail-closed。
- Task event payload 按 `max_task_events` 有界保留；压缩后旧 cursor 返回 `EVENT_CURSOR_COMPACTED floor=N`，不产生静默 gap。
- `event_dedup` 在 payload 压缩后仍保留 worker_seq/hash，使 Worker 断线重放继续可校验。
- 新增独立 GitHub Actions `long-run-flow`；release package 依赖 verify、task-flow、retry-flow、artifact-flow、workspace-flow、long-run-flow 六个 Gate。
- 真实 Codex/Claude 24h+、独立主机与真实网络故障仍由 Production Baseline #1 独立验收。

## 未发布 — v0.3.3 Workspace Lifecycle（实现完成，待版本发布）

- Worker schema 升级到 v3，新增 `workspaces` metadata，把 Workspace 与 Attempt / Task / generation / repository baseline / path 一次性绑定。
- Workspace 生命周期明确为 `PREPARING -> READY -> IN_USE -> RETAINED -> DELETING -> DELETED`；cleanup proof 不确定时进入 `QUARANTINED`，默认不自动 GC。
- Runtime/Verifier spawn 前必须持久化 `IN_USE`，Worker restart 可用 Workspace 状态区分安全 pre-spawn 与不确定 spawn window。
- Retention GC 只有在 `RETAINED + retain_until expired + runs.completed=1` 时才开始，删除使用可恢复的 `DELETING -> DELETED` tombstone。
- 升级时仅接管能与本地 run 对应的 legacy Workspace；未知目录不自动删除、不自动认领。
- 新增 `workspace_retention_ms` 与 `workspace_max_bytes`；quota 在 prepare、运行期与 completion 前检查，超限阻止成功 completion。
- Reduce input 临时目录跟随 Attempt cleanup proof 清理；cleanup unknown 的 Attempt 不删除其输入目录。
- 新增独立 GitHub Actions `workspace-flow`，覆盖 ownership、generation isolation、restart recovery、quarantine、retention、GC、legacy adoption、path/quota guard 与 Worker schema v3。
- Release package 现在依赖 verify、task-flow、retry-flow、artifact-flow、workspace-flow 五个 Gate。
- Prepared/warm Workspace、共享 Workspace、snapshot/checkpoint、EnvironmentProvider 与全局历史 GC 不属于 v0.3.3。

## 未发布 — Control Client Design

- 在既有 v0.2.x→v1.0 Agent Job Executor 路线图中新增 Control 客户端横向能力线。
- 增加客户端控制端竞品调研、ADR-008 和完整技术方案；确定 Server 单事实源、PWA 先行、移动端不作为通用 Worker、E2EE Relay 延后按证据引入。

## 0.3.2 — 2026-09-26 — Full Artifact Lifecycle

- Server schema 升级到 v6；Artifact 增加 `created / updated / gc_after / deleted_at`，状态扩展为 `STAGED / ACCEPTED / ORPHANED / DELETING / DELETED`。
- 新增不可变 `artifact_refs`，显式固化 `task_result / reduce_input / job_result` provenance；只有 ACCEPTED Artifact 可以被引用。
- Reduce frozen manifest 与显式 `reduce_input` reference 共同授权下游读取；Job 最终结果通过 `job_result` reference 固化。
- failed / retried / 未选中的 generation Artifact 进入 ORPHANED，并设置固定安全窗口，不进入正式列表、下载、Reduce 或 Job Result。
- 增加可恢复两阶段删除：`ORPHANED -> DELETING -> DELETED`，文件已删除或 Server 中途重启均可安全继续；DELETED 保留 tombstone。
- 增加老化 upload temp 与无 DB metadata 文件的安全清理；不把 Artifact 目录作为 source of truth。
- 新增独立 GitHub Actions `artifact-flow`，覆盖 accepted visibility、immutable refs、orphan GC、deletion recovery、Reduce/Job refs、schema v6 lifecycle guards。
- Release package 现在依赖 verify、task-flow、retry-flow、artifact-flow 四个 Gate。
- 用户 TTL、全历史 retention、Workspace GC、远端存储 Provider 与 HA 不属于 v0.3.2。

## v0.3.1 Retry Safety（随 v0.3.2 一并发布，未单独打 tag）

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
