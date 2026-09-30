# Codex CLI 模型网关（实验性）

本文描述通用的 CLI 网关配置，不包含具体主机、用户名、Token、SSH 别名、绝对路径或真实任务结果。路径使用环境变量占位符，实际部署时由操作者替换。

## 架构

```text
受信客户端 → HTTPS /v1/responses → Server CLI 网关 → 已登录 Codex CLI
受信客户端 → HTTPS /mcp          → Job API → Worker → 结果包
```

网关在 Server 所在的受信执行节点调用已登录 CLI。客户端工具调用作为结构化数据返回给客户端执行，网关本身不执行远程客户端工具。

## Server 配置

保留现有 TLS、数据目录和身份配置，增加模型路由：

```yaml
model_gateway:
  enabled: true
  max_inflight: 1
  request_timeout_seconds: 300
  routes:
    codex-local:
      backend: codex_cli
      cli:
        executable: __CODEX_EXECUTABLE__
        version: __CODEX_CLI_VERSION__
      allowed_models: [__MODEL_NAME__]
      max_inflight: 1

users:
  - owner: __MODEL_OWNER__
    token_file: __SECRETS_DIR__/model.token
    projects: [__PROJECT_ID__]
    scopes: [models:invoke]
    model_project: __PROJECT_ID__
    model_route: codex-local
```

模型 Token 与 Job Token 必须分离。Token 文件应仅本人可读，权限为 0600。不要把 CLI 登录凭据复制给客户端。

Server 启动前会执行 CLI `--version` 检查。版本不一致、可执行文件不可用或历史进程无法确认清理时，网关拒绝启动或恢复。

## 客户端配置

使用 TLS URL；跨主机部署禁止明文 HTTP 和 `insecure_loopback`：

```toml
model = "__MODEL_NAME__"
model_provider = "computecloud_cli"

[model_providers.computecloud_cli]
name = "computecloud CLI bridge"
base_url = "https://__SERVER_HOST__:7444/v1"
wire_api = "responses"
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
```

MCP 使用同一 Server 的 HTTPS `/mcp`，通过独立 Job Token 认证。新开客户端会话以加载最新工具定义。

## 进程和安全边界

- 每个请求使用独立临时目录和进程 journal。
- journal 保存 PID 和启动身份；恢复时先检查真实进程，再决定清理或拒绝恢复。
- CLI 使用受限参数和临时工作目录；远程工具定义只作为数据传入。
- 网关不自动重试上游 POST，不在未知执行状态下伪造成功或切换账号。
- CLI 订阅额度和上游 API 账单不是同一计量口径。
- 基础 Worker 容器不包含 Codex/Claude CLI；真实 CLI 验证需要固定版本的派生镜像。

## 协议边界

支持：

- `/v1/models` 和 `POST /v1/responses`
- 文本输入/输出、function/custom tool call 和结果续传
- Codex `additional_tools`、工具 namespace 扁平化和有限 SSE 生命周期事件

明确限制：

- 不支持服务端会话、`previous_response_id`、后台响应、WebSocket、持久化响应和多模态。
- 不支持 `/responses/compact`、托管工具、任意 JSON Schema 文本格式和未实现的高级请求参数。
- 流式响应不是逐 token 实时转发；CLI 完成并校验后才发送结构化内容。
- 工具调用由客户端执行，网关不会读取客户端文件或执行客户端 shell。

## 验证

```sh
go test ./internal/server -run 'TestCLI|TestGateway' -count=1
go test -race ./internal/config ./internal/server -run 'TestCLI|TestGateway' -count=1
```

真实模型、跨主机网络、凭据权限和长时间稳定性需要在目标环境单独验收。
