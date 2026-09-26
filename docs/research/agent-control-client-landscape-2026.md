# Agent 客户端控制端方案调研

- 项目：computecloud
- 日期：2026-09-26
- 状态：已形成架构决策，作为客户端控制面设计输入
- 关联：[Control 客户端技术方案](../design/client-control-plane.md)、[ADR-008](../adr/0008-client-control-plane.md)、[长期路线图](../implementation/long-term-roadmap.md)

## 1. 调研结论

Agent 客户端正在收敛为同一种结构：代码、凭据、终端和工具留在稳定执行主机，手机、Web 和桌面客户端作为可丢失、可重连的控制视图。客户端用于提交工作、查看状态、回复问题、审批权限、调整方向和审阅结果，不承担通用 Agent Worker。

computecloud 应实现自己的轻量控制端，而不是 Fork 某个完整 Agent IDE：

- Server 继续作为 Job / Task / Attempt、租约、配额、结果和 Artifact 的唯一事实源。
- Control App 只消费公开版本化 API，不读取 Server 或第三方工具的内部 SQLite。
- Worker 继续运行 Codex、Claude 等受验收的适配器；手机不保存模型供应商主密钥。
- 首版采用直连 HTTPS + 分页事件，随后增加 SSE；确有公网穿透需求后才增加可选 E2EE Relay。
- 先交付响应式 Web/PWA，验证控制工作流；再以 React Native + Expo 共享 Web、iOS 和 Android 代码。

## 2. 同类方案

| 方案 | 主要结构 | 优点 | 与 computecloud 的边界 |
| --- | --- | --- | --- |
| [Orca](https://github.com/stablyai/orca) | Electron/React 桌面、移动伴侣、PTY/worktree、远程 Server、Relay | Agent 工作台完整；已有 Run/Task/Dispatch、移动控制和 E2EE | 体量和运行时偏重；生产云部分私有；仅借鉴交互、证据状态和精确资源释放 |
| [Paseo](https://github.com/getpaseo/paseo) | Daemon + Expo App + Electron + CLI + WebSocket SDK/MCP + E2EE Relay | 多 Provider、多端、自托管、协议和 SDK 较完整 | 最接近客户端原型；不让其 Daemon 替代 computecloud Server |
| [Happy](https://github.com/slopus/happy) | CLI Wrapper + Expo App + 加密同步服务 | 移动优先、推送、设备切换、MIT | 借鉴配对、通知和交互；不采用 Wrapper 作为可靠调度器 |
| [Soromi](https://github.com/soromi/soromi) | Rust Daemon + 无状态 Viewport + PWA + E2EE Relay | 小而清晰；单事实源；无入站端口；每设备可撤销密钥；单写者控制 | 借鉴无状态客户端、传输可替换和写控制租约 |
| [OpenAI Codex Mobile](https://openai.com/index/work-with-codex-from-anywhere/) | ChatGPT App 连接 Codex 执行主机 | 主机、工作区、worktree、审批、Diff、测试和实时输出体验完整 | 厂商封闭且 Codex 专用；只作为产品体验基准 |
| [Claude Code Remote Control](https://code.claude.com/docs/en/remote-control) | Claude App/Web 连接本地 Claude Code | 本地环境不搬迁；终端、浏览器、手机同步；支持子 Agent/工作流 | 厂商账号和端点限制明显；只借鉴会话续接和设备同步 |
| [Omnara](https://github.com/omnara-ai/omnara) | Postgres 持久 Agent 平台、REST/SDK、机器与 RBAC | durable agent、模型无关、自托管和治理能力较完整 | 比 computecloud 重且控制面重叠；作为 API/RBAC 参考，不引入其平台 |
| [AgentAPI](https://github.com/coder/agentapi) | 将多个 CLI Agent 包装为 HTTP API | Provider Adapter 小、接口直接 | 当前仓库已归档；只参考适配思想，不作为依赖 |

## 3. 可复用的设计模式

### 3.1 必须采用

1. **主机执行、客户端控制**：客户端不复制仓库、CLI 登录目录或模型密钥。
2. **Server 单事实源**：客户端缓存只是投影，断线后按版本和事件游标恢复。
3. **读共享、写仲裁**：多设备可以观察；同一交互会话仅一个活动写控制租约。
4. **明确证据状态**：网络失联是 `UNKNOWN/UNVERIFIABLE`，不是执行已退出。
5. **幂等控制操作**：提交、取消、审批、输入和重试都带 `operation_id`；响应丢失不能盲目重放。
6. **Attempt fencing**：控制请求携带预期 Attempt 和 generation，旧客户端不得影响新 Attempt。
7. **内容最小化推送**：APNs/FCM 只传不敏感的通知类别和 opaque event ID，内容回到应用后拉取。
8. **可撤销设备身份**：每台设备独立密钥、Token、最后活动时间和撤销状态。

### 3.2 不进入首版

- 手机本地运行完整 Codex/Claude Worker。
- 手机 IDE、完整终端模拟器和 Git 冲突解决器。
- 把第三方 Relay、账号体系或私有云作为 computecloud 正确性依赖。
- 把 PTY 文本解析成权威任务状态。
- 为客户端提前引入 PostgreSQL、Redis、MQ、独立 BFF 或微服务网格。
- 在未经原生协议验收时声称支持执行中输入或原地审批。

## 4. 对产品路线的影响

客户端控制面是 Agent Job Executor 的产品表面，不改变 computecloud 的核心定位，也不是新的 AI Execution OS：

- v0.3.0–v0.3.2 已完成 Stage、multi-Attempt、Retry Safety 和 Artifact Lifecycle；v0.3 后续先补事件游标和只读控制 API，允许内部 PWA 观察真实 Job。
- v0.4 在 Runtime 能力完成后开放输入、审批和安全控制命令，形成可操作 PWA。
- v0.5 随 Agent-aware Scheduler 提供主机/运行时/模型选择和原生移动 Beta。
- v0.6 增加设备身份、RBAC、审计、推送和可选 E2EE Relay。
- v0.7/v1.0 完成规模、弱网、升级兼容和正式商店交付。

这一路线避免“先做漂亮 App、后补服务端语义”的倒置。客户端每一项可操作能力必须由已验证的 Server/Worker 契约支撑。

## 5. 来源与核查边界

- [Orca README](https://github.com/stablyai/orca/blob/da6d483ab9aae8aa81eaf773bcf0fda9fc4e3495/README.md)
- [Orca Mobile](https://github.com/stablyai/orca/blob/da6d483ab9aae8aa81eaf773bcf0fda9fc4e3495/docs/site/content/docs/mobile.mdx)
- [Orca Orchestration Guide](https://github.com/stablyai/orca/blob/da6d483ab9aae8aa81eaf773bcf0fda9fc4e3495/skill-guides/orchestration.md)
- [Paseo README](https://github.com/getpaseo/paseo/blob/main/README.md)
- [Happy README](https://github.com/slopus/happy/blob/main/README.md)
- [Soromi README](https://github.com/soromi/soromi/blob/main/README.md)
- [Omnara README](https://github.com/omnara-ai/omnara/blob/main/README.md)
- [OpenAI：Work with Codex from anywhere](https://openai.com/index/work-with-codex-from-anywhere/)
- [OpenAI：手机作为 Codex control plane](https://developers.openai.com/blog/mastering-codex-remote-for-engineering)
- [Claude Code Remote Control](https://code.claude.com/docs/en/remote-control)

调研是 2026-09-26 的源码和公开文档快照。第三方项目更新很快；采用代码前必须固定 commit/tag、核对许可证、运行安全审计和目标环境 PoC。
