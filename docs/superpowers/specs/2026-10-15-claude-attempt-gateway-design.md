# Claude Attempt 级凭据代理设计

状态：已批准实施；CI 与目标环境验收仍待完成
范围：在现有 Attempt Gateway 与 Claude HTTP Runtime 上提供受限的 Anthropic Messages 协议路径，使 strict isolated Runtime 不持有长期供应商 API key。

## 背景

当前 strict `isolated_only` 执行路径支持 Codex HTTP Attempt Gateway；Claude HTTP Runtime 虽能使用固定版本 CLI 和 HTTP Runtime container，但 Server Gateway 暴露 Responses API，没有 Anthropic Messages 适配。因此 Claude strict 模式目前 fail closed。直接把 Claude API key 放进 Worker 的 `credential_env` 或个人 `~/.claude` 会把长期凭据交给与任务内容同一执行信任域，不能作为公网不可信任务的安全修复。

## 目标

- 让 Claude CLI 在 strict Attempt 中仅持有短期、单 Attempt、可撤销 token，不接触长期 Anthropic key。
- 增加范围严格限定的 Anthropic Messages 代理契约，复用现有身份、Attempt lease、路由摘要、限流、用量预算、审计与脱敏基础。
- 保持凭据、项目、模型、Attempt、预算和租约均由 Server 管理；Worker 与任务输入不可改变目标上游或授权范围。
- 对错误、未知用量、预算耗尽、租约过期及不支持功能 fail closed。
- 不把 ComputeCloud 扩展成通用 LLM Gateway 或 Model Serving Platform。

## 非目标

- 不支持完整 Anthropic API 全面代理、任意 provider、模型服务、供应商账号管理门户或客户端直连上游。
- 不把长期 Anthropic key 下发到 Worker、CLI 子进程、Runtime 环境文件或任务可读挂载。
- 不通过猜测模型回答验证模型身份；上游身份和计量只记录代理实际配置/响应可验证的字段。
- 不自动启用受控生产环境中的 Claude strict 调度，也不在该功能 CI 通过后自动发布或切换线上配置。
- 不宣称本地 token/cost 预算等于供应商账单硬限额；上游账户仍须设置账单上限。

## 设计

### 协议面

新增仅供已签发 Attempt 使用的 Anthropic Messages endpoint，并支持 Claude CLI 当前需要的请求/响应形状、必要 headers（包括版本/可选 beta header）、流式 SSE 与错误映射。路由固定到管理员配置的 Anthropic 上游地址及模型 allowlist；客户端不得通过请求字段、header 或 URL 覆写上游、代理或 TLS 验证行为。若锁定版 CLI 必须使用 `?beta=true`，仅接受该精确查询项；其余 query 一律拒绝。

先以锁定的 Claude CLI 版本确定最小协议合同。未知字段可按 Anthropic API 兼容约定安全透传的非敏感字段；认证、路由、模型、stream、预算和工具交互相关字段必须显式解析/验证。请求与响应大小须有硬上限；不支持的 endpoint（包括 compact、Files、批量、计费/管理 API 等）明确返回不可重试的 unsupported 错误，除非协议探针证明 CLI 必需且经设计批准。

### Attempt 身份与密钥

Server 验证 Bearer Attempt token、Attempt ID/generation、租约、profile digest、项目、credential reference、模型 allowlist 与取消/撤销状态后才访问上游。token 短时有效且 scope 单一 Attempt；存储只保留不可逆摘要；错误和审计日志不包含 token 或供应商 key。上游 Anthropic key 只存在于 Server 侧安全凭据文件/secret provider，永不进入 Worker 注册信息或普通数据库请求正文。

Worker 在隔离 Runtime 中只设置 `ANTHROPIC_BASE_URL` 指向受控 Gateway 和 `ANTHROPIC_AUTH_TOKEN` 为 Attempt token。专用 egress 规则仅允许访问 Gateway；Attempt 不允许继承宿主代理或 TLS root 覆盖。Gateway 到供应商上游执行 TLS 证书校验。

### 预算与并发

每个 Attempt 配置模型 allowlist、最大输入/输出 token、费用估算上限、并发请求数、请求速率和请求字节限制。能获得 Anthropic usage 时按供应商 usage 结算；不能确定用量的请求按保守估算预留，若额度无法安全结算则 fail closed。预算耗尽时不再向上游发请求，并将可识别的限额错误返回固定脱敏格式。价格由管理员显式配置并带配置版本/摘要；它仅是应用侧预算，不是上游硬封顶。

客户端请求在同一 Attempt 内按有界方式串行化或受控并发，以避免竞态超支；具体并发数依据 CLI 合同测试确定，默认最小值。断连、重试和 stream 部分响应必须有清楚的计量状态；无法判断是否已产生用量时不得无限重发。

### 可观测与审计

记录调用者/项目、Attempt、配置 route digest、允许的模型标识、上游 HTTP 状态类别、请求字节、输入/输出用量、估算费用、延迟、重试与结算状态。不得记录提示词、原始 Messages body、供应商 key、Attempt token、完整原始响应或认证 header。错误日志需对供应商返回内容设大小上限并脱敏。

运行来源显示“Server 配置模型/路由”与“上游可验证返回元数据”两个字段；缺少可验证的上游部署身份时必须显示未验证，不能使用模型自然语言自述填充。

### 严格准入

`isolated_only` 下 `claude_http` 只有在 Worker 宣告支持 Claude Gateway capability、profile 固定 `attempt_gateway` credential mode、Runtime 与 CLI 版本被允许、Gateway route digest/预算完整时才可调度。Worker 在创建 workspace/Attempt 容器前再次校验 capability 和 credential mode；任何配置缺失或版本不匹配均拒绝执行，禁止静默回退到 `credential_env`、个人 `~/.claude` 或 `claude_exec`。

### 网络拓扑

Attempt 容器只连接 operator-created 的私有 egress 网络与本地/受控 Gateway 接入点；不能直连 Anthropic、任意公网或 Server 管理端口。Gateway 上游连接由 Server 控制并通过 TLS 验证。Server Nginx 的外部客户端 API 与内部 Attempt Gateway 认证路径须区分，以免把服务端 Attempt token 当作公开客户端 key 使用。

## 兼容性与迁移

- 功能初期为 opt-in；现有 Codex Attempt Gateway 行为和路由保持不变。
- 普通 `trusted_only` Claude 宿主 CLI 路径保持现状，不被隐式改写或误标为 strict。
- 新版本服务端与 Worker capability 协商不匹配时，调度器拒绝 Claude strict Job 并返回可操作原因。
- 必须先锁定 CLI 版本并以协议 mock/本地 fixture 验证，再做真实供应商凭据的受控验收；不得把 mock 通过说成真实上游验证。
- 生产部署迁移需后续另行给出排空、备份、key 配置、证书/路由安装、低额度 canary、回退和重新启用步骤，并由用户单独确认。

## 验收条件

1. 固定版本 Claude CLI 的非流式、流式、工具使用、错误与取消路径均满足本地协议合同测试。
2. 未授权、错误 Attempt、过期/撤销租约、route digest 变化、模型越权、预算耗尽、超限请求均在触达供应商前拒绝。
3. 任何 Worker/Attempt 可见配置、环境或文件中都找不到长期 Anthropic key；测试用 canary 确认任务执行身份不能读取 Server key 或其他 Attempt token。
4. Worker egress 负向测试证明 Attempt 无法直连 Anthropic/任意公网，允许路径仅到指定 Gateway；Gateway 上游证书校验开启。
5. 未知用量/计价、超限、断连重试和流式部分响应采用确定且 fail-closed 的计量行为。
6. 日志/事件/错误/SQLite 中不出现请求正文、认证头、Attempt token 或供应商密钥；保留可关联的脱敏 usage/audit 记录。
7. strict 模式只有在 Claude Gateway route 和 Worker capability 都明确启用时接受 Claude Job；其余配置仍 fail closed。
8. 真实上游验收绑定 CLI 版本、候选 commit、镜像 digest、平台和低额度测试账户；CI 不替代真实供应商协议、账单和目标主机 egress 证据。

## 风险与权衡

- Anthropic 协议随 CLI 版本变化；必须固定版本、显式合同测试，未知 endpoint 先拒绝。
- 流式用量通常在响应后才完整可知；需保守预留并限制在途请求，不能只在结束后扣费。
- Gateway 会成为模型请求路径的额外跳点与潜在瓶颈；限制其作用域、测量 P50/P95 延迟并对请求施加有界并发。
- 应用估算费用不能阻止供应商侧其他应用或并行凭据产生账单；依然需要供应商账户级硬限额。
- 代理服务可见提示词和模型响应；Server 管理面和存储访问必须继续最小化，配置正文保留期限并在独立生命周期设计中明确。
- TLS 或 egress 错误配置可能导致不可用；需同时验证允许路径成功和拒绝路径失败。

## 后续决策

- 支持的 Anthropic API/CLI endpoint 与 beta header 白名单。
- Claude 模型名、价格版本、最大上下文/输出与应用侧预算默认值。
- stream 计量缺失或上游断连时的预留和结算策略。
- Attempt Gateway 的内部监听/服务发现方式，以及 Nginx 是否需要专用内部路由。
- 供应商侧硬账单限额、目标主机验收凭据和变更窗口。
