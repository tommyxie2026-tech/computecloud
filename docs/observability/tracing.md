# 任务追踪与运行用量

## 查询

A 的 Codex 可使用 `list_jobs` 列出自己的历史任务，使用 `get_trace(job_id)` 查看任务的模型请求和 Worker 指标。`get_job`、`get_result` 均返回 `trace_id`，并提供按 Attempt 汇总的 Worker Token 用量。

自然语言例子：

> 列出当前执行过的任务，说明状态、输入摘要和 trace_id；查询最近一次任务的追踪数据，分别显示控制端模型请求与 Worker 的 Token 用量、运行耗时、CPU 时间和峰值内存。

B 的命令行（将环境变量替换为实际部署路径）：

```sh
cc_bin="${COMPUTECLOUD_BIN}"
cc_cfg="${COMPUTECLOUD_CLIENT_CONFIG}"
"$cc_bin" job list --config "$cc_cfg" --limit 20
"$cc_bin" job trace --config "$cc_cfg" --id YOUR_JOB_ID --limit 20
```

分页时将 `next_cursor` 传给 `--cursor`，直到 `has_more=false`。MCP 的两个新工具对应 `before` 和 `limit` 参数。`list_jobs` 按当前 Token 的 owner/project 过滤，不是全系统管理员视图。

HTTP 对应接口：

- `GET /v1/jobs?limit=20&before=CURSOR`
- `GET /v1/jobs/{id}/trace?limit=20&before=CURSOR`

均使用现有任务 Token 和 `jobs:read` 权限。查询结果不包含 Token、上游凭据或模型请求全文。

## 关联规则

模型网关为请求生成 `trace_id`，通过 `X-Computecloud-Trace-ID` 响应头返回。Codex 请求包含 `prompt_cache_key` 时，在授权 owner/project/route 范围内将该值哈希为稳定的会话 trace；没有该值时生成单请求 trace。可在请求头显式传入已有的 `X-Computecloud-Trace-ID`，但必须通过 owner/project 校验。

CLI 适配将服务端 trace 上下文加入模型指令，要求调用 `submit_job` 时传递 `trace_id`。MCP 已增加该可选参数；HTTP 提交可用同名请求头。Job 以事务绑定 trace，重试不能改变原任务 trace。没有传递 trace 的普通提交会建立独立的 Job trace，不会根据时间或请求内容猜测关联。

同一 A 会话可能提交多个 Job：`trace_scope=conversation`，`linked_jobs` 表示关联 Job 数；`controller_usage` 是整段会话的网关用量，不能为每个 Job 重复加总。Worker 网关请求已由 Attempt 身份绑定 Job，使用该 Job 的 trace。

模型 Token 和任务 Token 使用不同 owner 时，管理员在模型身份配置中显式指定归属：

```yaml
users:
  # 保留现有身份其余配置
  - owner: model-owner
    scopes: [models:invoke]
    model_project: demo
    model_route: codex-local
    trace_owner: job-owner
```

`trace_owner` 必须指向同项目内具备 jobs:submit/jobs:read 的已配置身份。它仅允许把模型请求追踪归属该身份，不授予模型 Token 提交、查询或取消 Job 的权限。

## 指标含义

`get_trace` 返回：

- `model_requests`：分页的 request_id、owner、model、状态、开始/结束时间、Token、完整性和错误码；对应网关的 `X-Request-ID`。
- `controller_usage`：该 trace 中非 Worker 请求的独立汇总；正在进行或未计量的请求计入 unknown_requests。
- `attempts`：Task、Attempt、Worker、generation、released 和 `metrics`；`task_state` 是 Task 当前状态，不代表每个历史 Attempt 的结局。
- `worker_usage`：当前 Job 的各执行尝试累计 Token，包括重试。与 `get_job/get_result` 的 usage 一致。

Worker 每次 CLI 结束后将 `attempt.metrics` 写入持久事件队列，Server 在事件提交的同一事务保存指标；事件重放不会重复累加。结果包 `report.json` 同时保存该次指标。

- Codex usage 来自 `turn.completed`，Claude 来自最终 `result.usage`。Codex 多轮累计；Claude input 包含其单独报告的缓存读取/写入 Token。缺失或无效用量不填 0。自定义 Runtime Provider 可通过 Outcome 报告用量、StartResult 报告可选进程指标；Server 校验指标来源与任务的 runtime_profile 一致，远程 Provider 无进程指标时保留 null。
- 每个 Attempt 只选择一个 Token 来源：配置了上游模型网关时使用网关计量，否则使用原生 CLI 报告。原生计量和网关计量不相加，避免重复计算。
- `coverage=complete` 表示 Job 已终止且所有执行尝试均有完整计量；部分已知为 partial，全部未知为 unavailable。失败但提供了完整用量的尝试也可被完整计量。
- `process.wall_ms` 是 CLI 进程启动、等待和清理阶段的墙钟时间，不含仓库准备、独立 verifier 和产物上传。
- CPU 时间及 peak_rss_bytes 来自操作系统 wait/rusage，Linux 的 KiB 已换算为字节。它们是操作系统报告的 CLI 进程资源指标，不是整个进程树或整台机器的连续监控，不提供 CPU/内存时间曲线。
- 该接口不提供费用金额；CLI 登录额度及 API 计费并非同一口径。

历史 Job 没有 trace 或指标时返回空 trace_id / null metrics / unavailable，不追溯伪造。A 旧会话可能仍缓存旧工具定义，修改 enabled_tools 后请新开 Codex 会话。

## 升级与恢复

Server 数据库迁移到 schema 14，新增 traces、attempt_metrics，以及 jobs/gateway_requests 的 trace_id。需先排空所有执行尝试及模型请求，停止 Server/Worker，再用旧二进制执行离线 backup，备份配置和二进制后升级。

旧二进制拒绝打开 schema 14；回退必须停止服务，并恢复升级前数据库备份和配套二进制/配置，不能只替换二进制。历史记录及产物保留，Worker 数据库沿用主线 schema 6。

升级兼容主线 Server schema 7–12，以及实验分支 schema 7（仅 tracing）、schema 8（tracing + liveness）和 schema 12（tracing + 主线 Control/Goal，不含审批表）。先识别历史表结构，再保留主线 control_operations、approval_requests、Goal 与重规划表，补齐 tracing。迁移在同一事务中补齐另一组表与字段，保留原有数据；不完整的结构会拒绝升级并回滚。需要先排空 Attempt 和模型请求。

验证：完整 `go test ./...`、`go vet ./...`，以及针对 TestTrace/TestNative/TestCLI/TestRun 的竞态检查。覆盖身份隔离、跨项目 trace 拒绝、幂等重试、分页、未知计量、Worker 事件持久化和迁移。

真实部署验证应在目标环境单独归档，记录脱敏后的 Job/Task/Attempt 标识、事件水位和指标覆盖范围，不把本机路径、凭据、网络地址或原始任务内容提交到仓库。
