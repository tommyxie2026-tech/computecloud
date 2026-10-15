# Claude CLI 连接 ComputeCloud 对话 API

本文说明如何让 Claude CLI 通过 HTTPS 直接连接 ComputeCloud 的 Anthropic 兼容对话 API，并提交 Agent Job。示例中的域名、模型别名和文件位置均为占位符；部署时请替换为自己的值。本文不包含任何实际环境信息或密钥。

## 工作方式

Claude CLI 请求 `ANTHROPIC_BASE_URL` 下的 Messages API。ComputeCloud Server 校验专用于对话 API 的 Bearer 会话密钥，再按 Server 端固定的 Conversation Profile 创建 Agent Job。Worker 按该 Profile 使用配置好的 Runtime 执行任务。

这里的会话密钥是 ComputeCloud 客户端访问凭据，不是 Anthropic 供应商 API Key，也不会把 Server 上的模型供应商密钥交给 Claude CLI。模型和执行策略由 Server Profile 决定；客户端模型名只是公开别名。不要把 Claude CLI 当作 MCP 客户端配置，也不要把 Worker/供应商凭据放入客户端配置。

启用前，管理员需在 Server 配置中启用 `conversation_jobs`，创建固定 Profile，并给客户端身份仅授予所需的 `conversations:submit`、`conversations:read`（如需读取请求状态）和 `conversations:cancel`（如需取消）权限。服务端需通过受信任 HTTPS 入口提供 TLS 1.3；证书主机名必须匹配使用的 API 域名。Server 还必须有可用的执行身份、Job 模板、项目和 Worker Runtime。

## Claude CLI 用户配置

先确认从运行 Claude CLI 的机器可以通过 HTTPS 访问 API 域名，并且该域名的证书链受系统信任。然后在 Claude CLI 的用户级 `~/.claude/settings.json` 中配置以下内容：

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "ANTHROPIC_BASE_URL": "https://api.example.invalid/agent",
    "ANTHROPIC_MODEL": "cc-public-agent"
  },
  "apiKeyHelper": "/absolute/path/to/computecloud-token-helper"
}
```

将 `api.example.invalid` 替换为自己的 API 域名；将 `cc-public-agent` 替换为 Server Conversation Profile 的 `public_model` 值；将 helper 路径替换为本机安全存放会话密钥的读取程序路径。保留 `/agent` 路径前缀，Claude CLI 会在其后请求 `/v1/messages`。配置文件必须是严格 JSON，不要加入注释或尾随逗号。

推荐使用 `apiKeyHelper` 从受限文件读取 ComputeCloud 会话密钥，避免把密钥写进 `settings.json`。例如，helper 可按以下形式实现；请将密钥文件路径替换为本机安全位置：

```sh
#!/bin/sh
set -eu
exec cat "$HOME/.config/computecloud/conversation.key"
```

限制 helper 仅当前用户可读写和执行，并将密钥文件权限设为仅当前用户可读写（例如目录权限 `700`、密钥文件权限 `600`）。不要在 helper 中打印调试信息，不要把密钥放入命令行参数、Shell 历史、项目仓库、截图或日志。

Claude CLI 的鉴权配置可能受已有环境变量影响。使用此方式时，检查启动 Claude CLI 的进程环境，清理与此连接冲突的 `ANTHROPIC_AUTH_TOKEN` 或 `ANTHROPIC_API_KEY` 设置；不要通过 `env`、`set -x` 或调试日志输出凭据。服务端只接受一个 `Authorization` 请求头，并将其作为 ComputeCloud 会话 Bearer 凭据校验。

## 自签 CA 证书

若 API 使用私有 CA 或自签证书，应把签发 CA 的证书加入操作系统信任库，或在启动 Claude CLI 的环境中设置 `NODE_EXTRA_CA_CERTS` 指向本机的 CA 证书文件。证书必须包含与 API 域名相符的 SAN。不要关闭 TLS 验证、忽略主机名校验或将 `NODE_TLS_REJECT_UNAUTHORIZED=0` 作为解决办法。

## 启动与验证

1. 在 Claude CLI 中运行 `/status`，确认加载了预期的 Base URL、模型别名和鉴权 helper。若客户端显示仍使用 Anthropic 默认端点，检查用户级设置是否加载，以及进程环境中是否存在覆盖项。
2. 先提交不含敏感内容的简短测试请求。成功时，Server 会创建一个对话请求和对应的 Agent Job；可使用有权限的运维界面或状态 API 查看请求和 Job 状态。
3. 验证 TLS 证书校验正常、未授权请求被拒绝、客户端密钥没有出现在 Claude 配置文件和服务日志中。完成后按密钥轮换策略保管或撤销测试密钥。

## 当前接口边界

该接口接收文本对话并转为普通 Agent Job，不提供 Claude 账户或供应商 API 代理。Server 固定执行 Profile、模型、项目和资源上限；客户端不能通过请求体改写这些策略。图片/附件、多模态输入、工具执行以及部分 Claude API 辅助端点当前不受支持；遇到接口不支持的字段时，Server 会拒绝请求。流式连接中断只会停止客户端观察，不代表已创建的 Job 自动取消。

不要将本文配置与 Worker 内部的 Claude Attempt Gateway 混淆。前者是 Claude CLI 作为 ComputeCloud 对话 API 客户端；后者供 Server/Worker 内部执行 Attempt 使用，二者的地址、凭据和请求流程不同。Attempt Gateway 的实现与验收边界见[专用说明](claude-attempt-gateway.md)。

## 官方配置参考

- [Claude Code Settings](https://code.claude.com/docs/en/settings)
- [Claude Code LLM gateway configuration](https://code.claude.com/docs/en/llm-gateway)
- [Claude Code network configuration](https://code.claude.com/docs/en/network-config)
