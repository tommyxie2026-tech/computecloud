# Issue #137：原生客户端远端任务接入设计

日期：2026-10-11（Asia/Tokyo）。状态：待用户审阅，尚未进入产品实现。

## 目标与基线

Claude Code 和 Codex 各自使用独立客户端配置，连接 ComputeCloud 地址并加载平台分配的 Key。每个受支持的对话请求创建或恢复一个持久化 single Job，由 Worker 完整执行任务，结果以客户端原生格式返回。不得仅代理一次模型推理后宣称完成远端任务。

GitHub Issue： https://github.com/tommyxie2026-tech/computecloud/issues/137

main 基线：8786ac94e7c7ed419d207f2d5bd1335c30263be4。现有实现已包含 Job 调度、用户权限、受信执行模板、产物、任务事件及恢复。HTTP 模型入口只支持 Responses，不包含 Messages。开发复用现有隔离工作区，切换独立功能分支；不修改本机正在运行的测试环境与宿主 Codex/Claude 配置。

## 选定方案及取舍

新增默认关闭的 conversation_jobs 接入模块，两种协议共用任务服务，使用独立路径前缀 `/agent/v1`。Claude 的 base URL 为 `https://server/agent`，请求 `/agent/v1/messages`；Codex 的 provider base URL 为 `https://server/agent/v1`，请求 `/agent/v1/responses`。现有 `/v1/jobs`、`/mcp`、`/v1/responses` 保持原有含义。

相比直接重用模型代理入口，独立前缀明确了远端任务执行语义，防止现有客户端流量意外启动 Worker。相比编写 MCP/CLI 包装，本方案满足地址加 Key 的原生客户端接入要求。

首个实现包含两种协议的文本输入、普通响应、SSE、多轮文本上下文、模型别名发现、任务跟踪和取消。图像、文件、音频、远端托管工具、跨供应商推理状态及客户端工具循环不在支持子集内，明确返回协议格式的能力错误。

## 身份、执行配置及密钥

### 传输安全与性能平衡方案

最低原则：跨主机传输平台 Key、任务文本、事件和结果时必须有经过身份校验的加密通道。HTTPS 不是唯一选择，但 API Key/HMAC 只用于身份或完整性，不能替代内容加密。默认不提供公网/普通 LAN 的裸 HTTP 任务入口。

采用两种部署方式，保持客户端的原生 HTTP 协议和同一套 Key：

1. 推荐方式：TLS 1.3 HTTPS + 平台 Key。没有公网域名或商业证书也可使用私有 CA；证书 SAN 绑定实际域名/IP，客户端加载 CA 且校验服务端身份。不允许跳过证书验证。连接池/keep-alive、TLS 会话恢复、长连接 SSE 降低重复握手开销；任务提交和取消不接受 TLS 0-RTT early data，防止重放触发副作用。
2. 无 HTTPS 的兼容方式：应用 HTTP 只绑定服务器回环地址，客户端通过固定目的地 SSH 本地转发访问。SSH 校验预先分发的 host key，采用专用隧道身份，限制端口转发目的地和本地监听地址。网络上传输仍由 SSH 加密；两端回环段仅位于受信任主机。为避免影响 Worker 的现有 TLS，不将全局 insecure_loopback 作为此模式的开关；需要独立的客户端回环 listener/明确配置，保留 Worker TLS。

多机规模化可采用外部运维的 WireGuard 加密网络。HTTP 只可在明确指定的隧道接口和防火墙白名单中启用；私网地址不等于加密通道。初次实现不自动安装或管理 VPN，不依靠 X-Forwarded-Proto 等请求头来证明链路安全。对多租户/不可信中间节点仍优先保留端到端 TLS。

不设计应用层 RSA/AES 加密 JSON、每条 SSE 独立加密或自建握手协议：这会要求修改 Claude/Codex 客户端协议，增加 nonce/重放/密钥轮换复杂度，也无法自动隐藏明文 Authorization。存储/日志安全单独处理，传输加密不能保护已经失陷的终端或自动加密数据库。

初版 Key 使用 crypto/rand 生成至少 256-bit 随机 token，按身份/项目/profile 最小授权，配置文件仅操作者可读；不进入 URL、日志和产物。复用现有 SHA-256 token 查找，避免对高熵 API Key 逐请求运行密码用的昂贵 KDF。轮换/吊销按已声明的配置重新加载/重启边界生效。高风险或长期暴露入口可另行扩展短期凭据，不把自建认证握手作为首版必需项。

性能验收使用协议 fixture 分离模型运行耗时：相同硬件和负载下比较 loopback HTTP、TLS 冷连接、TLS 连接复用、SSH 长隧道；输入大小为 1 KiB/64 KiB/256 KiB，并发 1/16/64。保存提交 P50/P95、SSE 首个协议事件延迟、CPU、RSS、吞吐与重连次数。首轮工程目标：复用连接的加密方式相对同机基线 P95 增量不超过 max(5 ms, 基线的 10%)；这是待测目标，不是性能承诺。跨网延迟另列，完整 Agent 执行时间不得用来掩盖入口开销。

安全验收覆盖错误 CA/host key、Key 缺失/吊销/越权、裸 HTTP 非回环监听配置拒绝、伪造 forwarded headers、响应/事件敏感信息泄露和重试幂等。跨主机抓包仅检查受控测试请求，确认实际外部接口上没有测试 token 或任务明文，不保存真实 Key。

Server 通过已有 token_file 配置平台 Key，不引入账号系统或供应商 Key 分发。支持轮换/吊销 token 文件并重启生效，文档明确生效边界。新用户 scope 为 conversations:submit/read/cancel；接入身份不能调用模型网关或其他项目任务 API，内部服务使用经校验的固定执行身份，不从请求获取该身份。

每个 Key 显式绑定 execution_profile。Profile 冻结 owner、project、repository_ref、完整 base_commit、runtime_profile、实际模型、credential_ref、policy、acceptance_profile、environment、timeout、最大并发及最大输入/输出字节。配置校验要求原身份拥有固定项目和凭据权限，并登记受信模板。

客户端 model 是配置中允许的别名，例如 remote-worker，映射到固定 profile；不得从客户端请求覆盖仓库、凭据、路径、策略或 Runtime。模型发现只返回当前 Key 可见别名。跨身份请求、结果和会话统一隐藏为未找到。

## 共享任务服务与持久化

新增独立 SQLite 迁移，存储接入身份、协议、规范化请求摘要、会话/响应标识、profile 摘要、Job ID 和完成状态。Key 正文与上游凭据不进入数据库、日志、错误或产物。

先完成协议规范化、权限及能力验证，再通过既有 SubmitJob 路径提交 single Job。执行身份与 trace_id 保留。请求映射与 Job 使用可恢复提交流程：稳定幂等键写入任务请求记录，重启后使用同一键恢复 Job，避免“Job 已创建但映射未保存”导致重复执行。

调用方 Idempotency-Key 优先；原生客户端没有提供时，以身份、协议、冻结 profile 和规范化完整请求计算稳定键。相同完整请求会恢复同一结果；用户确实需要再次执行相同内容时使用新的幂等键/会话标识。该取舍在文档中显式说明，不能一边保证无键重试不重复，一边保证相同内容总是重新执行。

Job 结果必须等 Worker 完成和清理后发布。适配层读取授权 Job 的完整最终结果/报告，不依赖目前最多 4096 字节的 Job summary 作为完整回答。产物及 job/task/attempt/worker 关联保留。响应附带平台 request/job 标识，允许用户通过管理 API 复核。

## 请求与执行所有权

输入按顺序保留用户消息、历史 assistant 文本和受支持的 instructions/system 文本，并标注其来源。固定平台策略不能被客户端 system 文本覆盖。多轮默认重放完整文本上下文；不是 Worker CLI 的 session resume，不宣称原生工具状态持续存在。

客户端声明的工具 schema 可作为客户端协议附带元数据接收，但不会成为远端工具配置、不会转发为可执行工具，也不会返回客户端 tool_use/function_call。执行工具完全受 Worker 的固定策略管理。强制 tool_choice、tool_result/function_call_output、要求客户端执行的 custom tool 输入拒绝。响应始终为文本结果；客户端接入文档明确它是远端完整任务模式，本地目录不会自动同步到 Worker。

不得将 Worker 内部工具事件伪装为客户端工具调用；这避免两端重复执行。客户端启动报文兼容性需真实 CLI 验证，任何必要字段放行都不能放宽执行策略。

## 协议、流式结果与限制

Messages 接收受支持的文本 messages/system；Responses 接收字符串或受支持的文本 message items/instructions。已知但未支持的状态、内容与工具类型返回明确 4xx；未知字段按协议契约逐项处理，不静默改变执行语义。输入大小、JSON 结构和文本编码在提交前校验。

实现两种协议所需的创建、文本块/输出 item、文本 delta、结束和错误事件，遵循各自 SSE 顺序。等待 Job 时发送兼容的 keepalive，不将排队进度混入用户回答。初版最终文本可在 Job 完成后分块发送，不宣称模型 token 实时透传。

输出 cap 约束返回给客户端的文本；与 Worker 的实际推理用量分开。Claude 必填 max_tokens 和 Responses max_output_tokens 不直接宣称为 Runtime 硬上限：按经过验证的保守输出截断规则约束协议输出，明确返回截断 stop_reason。实际 Worker token/cost 记录保存在任务追踪中，不能伪造客户端 token 数或宣称供应商账单硬上限。若请求显式要求 Runtime 硬预算而该执行模板不支持，在创建 Job 前拒绝。

模型发现及 token-count/compact 的行为必须有独立契约：不支持的辅助能力返回稳定错误，不给出伪造计数或压缩。真实 CLI 如强制要求某辅助能力，需补齐并测试后才能将该客户端标为可用。

## 断线、取消与恢复

HTTP/SSE 断线只停止当前观察，不取消持久化 Job；重试可恢复同一 Job。显式平台 conversation cancel 端点持久化取消，复用 Job 取消及清理流程。客户端 Ctrl-C 是否发出可识别取消请求须实际捕获验证；如果只关闭连接，文档注明任务继续执行，不把它标为远端已取消。

观察超时返回可重试错误和请求标识；执行超时由冻结 Job limit 管理。Server 重启恢复映射并继续观察，Worker 失联沿用已有租约/fencing/清理语义。不得在 HTTP handler 中单独启动无退出条件的 goroutine。

## 实施与验证边界

代码主要涉及 config/rpcutil 身份与 scope、store 迁移、独立 conversation 规范化/任务服务/两种协议 handler、HTTP 注册；复用 Jobs、Worker 和现有恢复，不重构模型网关。

测试必须覆盖：默认关闭与配置拒绝；双协议普通/SSE；输入及工具支持边界；授权隔离；固定 profile；幂等冲突与无键重试；提交事务间故障；真实 Worker fixture 执行；完整输出；排队/失败/超时/断线/取消清理；Server 重启；模型/预算能力不足；迁移升级及已有接口回归。

运行仓库规定的 Go/集成检查及 CI。真实 Claude Code、Codex CLI 的版本、独立客户端配置、Server 请求、Job/Task/Worker 事件和结果必须关联保存。CI fixture、同机容器和真实物理双机证据分别记录；尚未完成的验收保留在 Issue，不将模拟标为真实通过。

PR 包含实现、契约、README、独立配置示例及验收证据，并引用 Issue #137。除非全部验收条目实际满足，不使用自动关闭 Issue 的表述。

## 审阅要点

请确认独立 `/agent` 入口、远端完整任务执行、客户端工具不执行、断线不自动取消，以及无幂等键的相同请求复用结果这些产品语义。传输采用默认 TLS + Key、无 HTTPS 时 HTTP 回环 + 固定 SSH 隧道；VPN 为外部部署选项。批准本设计后，编写具体实施计划，再按照计划实现并提交 PR。
