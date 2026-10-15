# Claude HTTP Runtime 的 Attempt Gateway

本文说明 Server 如何为受控的 `claude_http` Worker Attempt 提供 Anthropic Messages API。该能力仍处于实现与 CI 合约验收阶段；文中的配置项均为占位示例，不代表任何线上环境已经启用。

## 请求与凭据边界

1. Server 根据固定 Job Profile、模型白名单、路由摘要和 Worker 能力选择任务。
2. Assignment 仅含 Gateway 地址、路由引用和短期 Attempt Token；长期 Anthropic Key 保存在 Server 配置引用的私有文件中。
3. Worker 只为隔离的 Attempt 容器设置 `ANTHROPIC_BASE_URL` 与 `ANTHROPIC_AUTH_TOKEN`，并清除既有 `ANTHROPIC_*` 和 `CLAUDE_CODE_USE_*` 覆盖项。Attempt Token 会作为敏感值参与日志脱敏。
4. Server 仅接受固定的 `POST /v1/messages`，校验 Attempt 租约、模型、请求字段、请求大小、并发和预算，然后将认证替换为 Server 端 `x-api-key` 并连接配置好的 HTTPS 上游。上游 TLS 校验开启且不使用环境代理，不跟随重定向。
5. 非流式响应和 SSE 流均须解析到完整 Anthropic usage 后才能完成结算；未知 usage 会记为不确定并阻止该 Attempt 后续调用。

## 启用前条件

- Server `public_execution_mode` 使用 `isolated_only`，Gateway 与 Anthropic route 配置完整。
- route 指定固定 HTTPS `base_url`、`protocol_version: "2023-06-01"`、唯一 `api_key_ref`、受限模型列表、并发上限、每 Attempt token/费用上限和价格参数。
- 上游 Key 通过私有文件提供；文件必须是普通文件且仅服务账户可读。不要将 Key 放进 YAML、环境变量、镜像、Worker 配置、Job、日志或本仓库。
- Claude Worker 使用固定 `claude_http` Runtime `2.1.292`。只有该版本的健康端点声明 `gateway_anthropic_messages_v1` 时才会被调度；升级 CLI 需要先更新协议合约和测试。
- 上游费用由已结算 usage 与管理员配置价格估算。它不能限制供应商账单，也不能保证单个在途请求不超预算；应另配供应商侧支出限额。

Server 配置结构示例（路径和值仅作占位）：

```yaml
model_gateway:
  enabled: true
  public_base_url: https://gateway.example.invalid/agent
  routes:
    claude-route:
      backend: anthropic_messages
      base_url: https://provider.example.invalid/v1
      api_key_ref: provider-key-ref
      api_key_file: /path/to/private/provider-key
      protocol_version: "2023-06-01"
      allowed_models: [approved-model]
      max_inflight: 2
      max_attempt_tokens: 20000
      max_attempt_cost_microusd: 500000
      input_cost_per_million_tokens_microusd: 3000000
      output_cost_per_million_tokens_microusd: 15000000
  credential_routes:
    claude-runtime-credential: claude-route
```

具体字段和校验以当前 Server 配置结构为准。不得将上述演示价格视为建议价格；必须用自己的供应商合同数据配置，并在上线前单独验证计费口径。

## 已验证范围与限制

本地测试使用假的 Anthropic 上游和 fixture-only 凭据；CLI 合约脚本使用临时 HOME、临时配置目录和 loopback mock。当前开发机 CLI 版本与 CI 固定版本不同，因此本机合约运行只作为协议探索；固定版本门禁由 `Dockerfile.agent-http` 对应的 CI 镜像验证。CI 与本地 mock 均不证明真实供应商兼容性、Worker 主机隔离、出站防火墙、账单硬限额或公网部署安全。

暂不支持 `count_tokens`、其他 Anthropic API、任意 beta feature、`x-api-key` 客户端认证、代理重定向和未知 usage 字段。Beta Header 仅接受当前代码内列出的值并按原值转发；CLI 升级若改变请求路径、字段或 Beta Header，应先更新固定合约、Gateway allowlist 与回归测试。Strict mode 对不支持的协议和凭据路径失败关闭。
