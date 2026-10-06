# dev 分支修改总览

本文总结当前 `dev` 分支相对 `origin/main` 的实际修改。基线为 `origin/main`，当前分支额外提交为：

```
27f52c7 feat: add macOS Codex gateway and task tracing
e660dea docs: add v0.4.6 Ubuntu and Mac Air container deployment
```

## 修改范围

本分支围绕两项核心能力扩展：

1. 在 macOS 上通过已登录的 Codex CLI 提供受限的 Responses 模型网关。
2. 为 Job、模型请求、Worker Attempt 和运行用量建立可授权的 Trace 查询链路。

主线已有的 Control、Lease、Manual Retry、移动端基础能力、Runtime/Tool/Environment、审批和发布能力全部保留；本分支没有替换主线控制面。

## 1. macOS Codex CLI 网关

新增 `internal/server/gateway_cli.go` 和 `gateway_cli_stream.go`，允许 Server 将兼容的 Responses 请求交给本机已登录 CLI，再把结构化结果返回给远程客户端。

网关行为包括：

- 启动前执行 CLI `--version` 检查，版本不匹配时拒绝服务。
- 每个请求使用独立临时目录和受控进程记录。
- 通过 `process.json` 持久化 PID 和启动身份，支持重启后的清理核查。
- 使用结构化 JSON 输出约束 CLI 只产生 message、function call 或 custom tool call。
- 不在网关进程中执行远程客户端工具；工具调用作为数据返回给客户端执行。
- 拒绝不支持的 compaction、文本格式、请求参数和 hosted tool 类型。
- 支持有限的 Responses SSE/流式转发和明确的大小、超时、空闲限制。
- 对 namespace tool 做扁平化和命名空间校验，拒绝重复或非法工具名。
- 不自动重试上游 POST，不在未知执行状态下伪造成功或切换账号。

网关相关配置、协议边界和运行方式见 [Codex CLI 模型网关](../deployment/codex-cli-gateway.md)。

基础容器 Worker 不包含 Codex/Claude CLI；真实 CLI 验证需要构建固定版本的派生镜像。macOS 原生 CLI 网关与 Linux 容器 Worker 是两条不同的运行路径。

## 2. Trace 与运行用量

新增 `internal/server/trace.go` 和对应 HTTP/MCP 接口：

- Job 支持 `trace_id`，Job 响应增加 Trace 链接。
- Responses 请求可以从 Header 或 `prompt_cache_key` 关联 Trace。
- 同一会话可复用稳定 Trace；没有会话标识时生成独立 Trace。
- Trace 查询校验 owner、project 和 `jobs:read` 权限。
- 新增 `GET /v1/jobs/{id}/trace`。
- MCP 的 `submit_job` 支持传入 `trace_id`。
- MCP 增加 `list_jobs` 和 `get_trace`，支持分页和对象归属检查。
- Trace 返回关联 Job、模型请求、Attempt、Worker 用量和用量覆盖范围。
- 未知或缺失供应商用量保留为 unknown/null，不估算费用。
- 对同一 Attempt 只选择一个用量来源，避免把 native metrics 与 gateway metrics 重复相加。

新增 `internal/telemetry/metrics.go`：

- 统一 token 字段解析和上限校验。
- 支持 input/output/cached token。
- 支持 wall time、CPU time、peak RSS 等进程指标。
- 运行时指标必须带 source，并与任务 runtime profile 匹配。

## 3. 数据库迁移与兼容性

主线 v13 的控制写租约保持不变。本分支将追踪相关结构放到 v14：

- `traces`
- `jobs.trace_id`
- `gateway_requests.trace_id`
- `jobs_trace` 和 `gateway_requests_trace` 索引
- `attempt_metrics`

当前 Server schema target 为 v14，Worker schema 仍为 v6。

迁移逻辑额外处理了两类历史数据库：

- 主线 v13：已有控制写租约，补充追踪表和字段。
- 实验性追踪 v13：已有追踪表，补充主线控制写租约。

迁移会检查审批、控制操作、liveness、事件去重、Trace、Gateway 和 Attempt metrics 等关键结构；发现部分迁移或损坏结构时回滚，不会直接把数据库标记为新版本。

回退限制：已经升级到 schema v14 的数据目录不能直接由 v0.4.6 schema v13 二进制打开。回退必须恢复升级前完整 data directory，禁止手改 `PRAGMA user_version`。

## 4. 跨平台进程监管

进程监管拆分为平台实现：

- `internal/process/process_darwin.go`：macOS 进程身份查询、进程组停止和清理确认。
- `internal/process/process_linux.go`：Linux `/proc` 身份、进程组和环境清理。
- 公共层统一启动身份、停止、检查和恢复语义。

Worker 恢复时不只依赖数据库状态，还要核对持久启动记录和真实进程身份；无法确认时保持核查状态，不能盲目启动第二份 CLI。

## 5. Job 与运行链路调整

Job 逻辑增加：

- Job Trace ID 的创建、幂等复用和冲突检查。
- Job、Task、Attempt 与 Gateway request 的关联。
- Job 详情和结果中的 Trace 链接。
- Worker metrics 的持久化和校验。
- HTTP Job 列表与 Trace 查询和主线 Control collection 路由共存。

MCP 和 HTTP 都继续执行身份、scope、project 和 owner 校验。Trace 只增加可观测性，不扩大 Job、模型或 Worker 权限。

## 6. 验证与测试

新增和调整的测试覆盖：

- CLI 网关版本检查、请求限制、工具规范化、SSE、恢复和清理。
- Trace 创建、授权、分页、跨项目拒绝、幂等关联。
- Gateway request 与 Worker attempt 用量聚合。
- v7/v8/v12/v13 历史数据库到 v14 的迁移。
- 部分迁移、损坏表、活动 Attempt 和 Gateway request 的回滚保护。
- Darwin/Linux 进程实现和环境指标。

当前验证命令：

```sh
go test -p 1 ./... -count=1
go vet ./...
python3 -m unittest discover -s scripts -p 'test_*.py'
```

最近一次结果：Go 全仓测试通过，`go vet` 通过，Python 测试 8 项通过。

## 7. 容器化与部署文档

新增或更新：

- macOS 双机部署、隧道、备份和追踪记录。
- macOS 本机验证和真实 CLI 验证边界。
- Ubuntu Server + MacBook Air Worker 的 v0.4.6 容器化验证文档。
- 容器 TLS、IP/端口、镜像 digest、卷权限和回滚说明。
- 容量工具对容器和固定资源的使用说明。

当前推荐的容器化验证方式是：

```
Ubuntu: computecloud-server v0.4.6
Mac Air: Docker Desktop + Linux worker v0.4.6
通信: gRPC TLS 7443，HTTPS 7444
数据: Server 和 Worker 使用独立新数据卷
```

部署步骤见 [v0.4.6 Ubuntu Server + MacBook Air Worker 容器化验证](../deployment/v0.4.6-ubuntu-server-mac-air.md)。

## 8. 已知边界

- CLI 网关不是通用模型沙箱，不执行远程客户端工具。
- 基础 Worker 镜像不包含 Codex/Claude，需要固定版本派生镜像。
- Relay/P2P、Server HA、Push/pairing、企业 RBAC 和 Goal Governance 不在本分支交付范围。
- Trace 用量不是供应商账单对账；未知数据必须保持 unknown。
- fixture、单机容量和容器测试不能替代真实模型账号、独立主机网络和生产容量验收。
- 当前分支 schema v14 与 v0.4.6 正式发布 schema v13 不兼容回退。

