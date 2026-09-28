# Agent 客户端控制端方案调研

- 项目：computecloud
- 初始日期：2026-09-26
- 更新日期：2026-09-28
- 状态：已形成架构决策，并补充多 Agent 后端 / Mobile Control / Agent Gateway 协议级竞品研究
- 关联：[Control 客户端技术方案](../design/client-control-plane.md)、[ADR-008](../adr/0008-client-control-plane.md)、[长期路线图](../implementation/long-term-roadmap.md)

## 1. 调研结论

Agent 客户端正在收敛为同一种结构：代码、凭据、终端和工具留在稳定执行主机，手机、Web 和桌面客户端作为可丢失、可重连的控制视图。客户端用于提交工作、查看状态、回复问题、审批权限、调整方向和审阅结果，不承担通用 Agent Worker。

computecloud 应实现自己的轻量控制端，而不是 Fork 某个完整 Agent IDE：

- Server 继续作为 Job / Task / Attempt、租约、配额、结果和 Artifact 的唯一事实源。
- Control App 只消费公开版本化 API，不读取 Server 或第三方工具的内部 SQLite。
- Worker 继续运行 Codex、Claude 等受验收的适配器；手机不保存模型供应商主密钥。
- 首版采用直连 HTTPS + 分页事件，随后增加 SSE；确有公网穿透需求后才增加可选 E2EE Relay。
- 先交付响应式 Web/PWA，验证控制工作流；再以 React Native + Expo 共享 Web、iOS 和 Android 代码。

本次更新进一步确认：

> 市场正在从“Multi-LLM Client”分化出“Universal Coding Agent Client / Mobile Agent Control Plane”。真正值得标准化的不是 Chat Completion API，而是 Agent Session、Approval、Diff、Terminal、File、Resume 和 Event 等运行时控制语义。

## 2. 同类方案

| 方案 | 主要结构 | 优点 | 与 computecloud 的边界 |
| --- | --- | --- | --- |
| [Orca](https://github.com/stablyai/orca) | Electron/React 桌面、移动伴侣、PTY/worktree、远程 Server、Relay | Agent 工作台完整；已有 Run/Task/Dispatch、移动控制和 E2EE | 体量和运行时偏重；生产云部分私有；仅借鉴交互、证据状态和精确资源释放 |
| [Paseo](https://github.com/getpaseo/paseo) | Daemon + Expo App + Electron + CLI + WebSocket SDK/MCP + E2EE Relay | 多 Provider、多端、自托管、协议和 SDK 较完整 | 最接近客户端原型；不让其 Daemon 替代 computecloud Server |
| [Happy](https://github.com/slopus/happy) | CLI Wrapper + Mobile/Web/macOS App + E2EE 同步 | 明确支持 Claude Code / Codex；移动优先、设备切换、MIT | 借鉴配对、E2EE、通知和交互；不采用 Wrapper 作为可靠调度器 |
| [CC Pocket](https://github.com/K9i-0/ccpocket) | Mobile/Desktop App + WebSocket Bridge + Local Agent CLI | Codex/Claude 直接控制；审批、Diff、文件、Git、弱网恢复；Bridge 很轻 | 很适合参考 Agent Adapter / Bridge 协议；Bridge 不替代 Server/Job Scheduler |
| [OvertChat](https://github.com/yoloyash/overtchat) | Web/Android + Server + Host Connector | Host Connector 可接 Codex、Claude Code、Pi、Oh My Pi、OpenCode；支持 session resume、plan、tool call、approval | 重点参考 Host Connector / 多 Agent 适配；不引入其聊天/搜索产品边界 |
| [Soromi](https://github.com/soromi/soromi) | Rust Daemon + 无状态 Viewport + PWA + E2EE Relay | 小而清晰；单事实源；无入站端口；每设备可撤销密钥；单写者控制 | 借鉴无状态客户端、传输可替换和写控制租约 |
| [OpenAI Codex Mobile](https://openai.com/index/work-with-codex-from-anywhere/) | ChatGPT App 连接 Codex 执行主机 | 主机、工作区、worktree、审批、Diff、测试和实时输出体验完整 | 厂商封闭且 Codex 专用；只作为产品体验基准 |
| [Claude Code Remote Control](https://code.claude.com/docs/en/remote-control) | Claude App/Web 连接本地 Claude Code | 本地环境不搬迁；终端、浏览器、手机同步；支持子 Agent/工作流 | 厂商账号和端点限制明显；只借鉴会话续接和设备同步 |
| [Oriveo](https://github.com/oriveo/oriveo) | iOS/Android/Web BYOK Multi-LLM Client | 支持 OpenAI / Anthropic / Gemini 及三类 compatible endpoint、自托管、local-first | 属于 Universal LLM Client 对照组，不具备完整 Coding Agent session/approval/runtime 语义 |
| [Omnara](https://github.com/omnara-ai/omnara) | Postgres 持久 Agent 平台、REST/SDK、机器与 RBAC | durable agent、模型无关、自托管和治理能力较完整 | 比 computecloud 重且控制面重叠；作为 API/RBAC 参考，不引入其平台 |
| [AgentAPI](https://github.com/coder/agentapi) | 将多个 CLI Agent 包装为 HTTP API | Provider Adapter 小、接口直接 | 当前仓库已归档；只参考适配思想，不作为依赖 |

## 3. 协议级竞品矩阵（2026-09-28）

本轮新增重点不是比较 UI，而是比较“一个控制客户端如何连接不同 Coding Agent 后端”。

| 能力 | OvertChat | Happy | CC Pocket | Oriveo | computecloud 目标 |
| --- | --- | --- | --- | --- | --- |
| Codex | 是 | 是 | 是 | API 级，不是 Codex CLI Session | 是 |
| Claude Code | 是 | 是 | 是 | Anthropic API 级 | 是 |
| Gemini / Gemini CLI | Host Connector 可继续扩展 | 当前核心不是 Gemini | 当前核心不是 Gemini | Gemini API/compatible | 应通过 Runtime Adapter 支持 |
| 多 Agent Adapter | 强 | 中 | 中 | Provider API 强、Agent Runtime 弱 | 强 |
| Session create/resume | 是 | 是 | 是 | Chat conversation | 必须 |
| Streaming events | 是 | 是 | 是 | 是 | 必须 |
| Approval | 是 | Agent 侧支持 | 是 | 非核心 | 必须 |
| Diff / changed files | 是 | 是 | 是 | 非核心 | 必须 |
| File browse | 是 | 是 | 是 | 附件/聊天文件 | 必须 |
| Terminal / PTY | Host 侧能力 | 桌面能力较强 | 有主机控制能力 | 非核心 | 可选 Runtime capability |
| Mobile native | Android | iOS/Android | iOS/Android | iOS/Android | C3 |
| Web | 是 | 是 | 否/非主要入口 | 是 | C1/C2 |
| E2EE / Relay | 可自托管架构 | 明确 E2EE | 可通过自托管/Tailscale；部分实现强调 E2EE | local-first/BYOK | v0.6 可选 |
| Worker discovery / scheduling | 否 | 否 | 否 | 否 | computecloud 核心差异 |
| Attempt fencing | 否 | 否 | 否 | 否 | computecloud 核心差异 |
| Durable Job model | 弱/无 | 弱/无 | 弱/无 | 无 | computecloud 核心差异 |
| Multi-worker routing | 弱 | 无 | Host 直连 | 无 | computecloud 核心差异 |
| Enterprise policy/audit | 有限 | 有限 | 有限 | 有限 | v0.6 |

关键判断：

1. **OvertChat 最值得研究 Host Connector / Agent Adapter 层。**
2. **CC Pocket 最值得研究最小 Bridge、WebSocket、approval/diff/file UX。**
3. **Happy 最值得研究 Mobile + E2EE Relay + Session continuity。**
4. **Oriveo 代表另一类产品：Universal LLM Client。它说明 OpenAI/Anthropic/Gemini compatible endpoint 已相对成熟，但不能替代 Coding Agent Runtime Protocol。**
5. 当前仍缺少一个同时覆盖“多 Agent Runtime + Durable Job + Worker Scheduling + Mobile Control + Enterprise Governance”的开放事实标准，这正是 computecloud 可以建立差异化的位置。

## 4. 从 Chat API 演进为 Agent Runtime API

普通 Multi-LLM Client 通常抽象：

~~~text
POST /chat/completions
POST /responses
stream tokens
~~~

Coding Agent 控制端需要的是：

~~~text
create_session()
resume_session()
send_prompt()
cancel()
interrupt()

subscribe_events()

get_plan()
get_files()
get_diff()
get_terminal()

approve_tool()
reject_tool()

get_usage()
get_artifacts()
~~~

因此 computecloud 后续协议不应该把客户端直接绑定到 Codex / Claude Code CLI，而应形成：

~~~text
Mobile / Web / Desktop
        |
        v
Agent Control API
        |
        v
computecloud Server
        |
        v
Runtime Adapter
   |      |       |
 Codex  Claude  Gemini/Other
        |
        v
Job Executor / Worker
~~~

客户端只理解稳定的 Agent Control Protocol；Server/Worker 负责把通用语义翻译为具体 Runtime 协议。

## 5. 建议的 Agent Control Protocol 最小模型

### 5.1 Session

~~~text
session_id
runtime
runtime_session_ref
job_id
task_id
attempt_id
generation
worker_id
state
capabilities[]
~~~

Session 仍然不是新的顶级业务领域对象，而是 Runtime 能力在 Job / Task / Attempt 上的稳定控制引用。

### 5.2 Event

统一事件流至少覆盖：

~~~text
session.started
session.resumed
message.delta
message.completed
plan.updated
tool.requested
tool.started
tool.completed
approval.requested
approval.resolved
file.changed
diff.updated
artifact.created
runtime.warning
runtime.failed
session.completed
~~~

客户端使用事件游标重连，而不是依赖 PTY 文本重放来推断状态。

### 5.3 Approval

~~~text
approval_id
attempt_id
generation
tool
action
risk_class
arguments_summary
requested_at
expires_at
decision
actor
~~~

Approval 必须 generation fenced，并进入 Audit。

### 5.4 Capability Negotiation

不同 Agent 能力并不相同，因此协议必须显式声明：

~~~text
session_resume
interactive_input
approval
plan
diff
file_read
file_write
terminal
image
usage
structured_output
tool_events
~~~

禁止因为 UI 有某个按钮，就假设所有 Runtime 都支持相同语义。

## 6. 多 Agent 后端接入原则

建议 Adapter 形态：

~~~text
RuntimeProvider
├── CodexProvider
├── ClaudeCodeProvider
├── GeminiProvider
├── OpenCodeProvider
└── CustomProvider
~~~

每个 Provider 实现相同 contract：

~~~text
Prepare
Start
Resume
Input
Approve
Interrupt
Cancel
Inspect
Subscribe
CollectArtifacts
Cleanup
~~~

其中某些能力允许返回：

~~~text
UNSUPPORTED
~~~

而不是伪造兼容行为。

客户端选择：

~~~json
{
  "runtime": "codex",
  "workspace": "repo-a",
  "task": "fix issue #123"
}
~~~

Server 决定：

~~~text
runtime -> adapter
workspace -> prepared workspace
credential -> allowed credential
worker -> scheduler selection
environment -> provider
~~~

这保持 Mobile Control 与 Agent Job Scheduler 解耦。

## 7. 与 computecloud 当前架构的关系

本轮调研没有推翻现有架构，反而强化原有判断：

~~~text
Control App
    |
    v
Control API / Event API
    |
    v
Agent Job Server
    |
    +---- Job / Stage / Task / Attempt
    +---- SessionRef
    +---- Approval
    +---- Artifact
    +---- Audit
    |
    v
Agent-aware Scheduler
    |
    v
Worker
    |
    v
Runtime Adapter
    |
    +---- Codex
    +---- Claude Code
    +---- Gemini CLI / API-backed Agent
    +---- OpenCode
    +---- Custom Agent
~~~

computecloud 与现有移动 Coding Agent 产品最重要的区别不是 UI，而是：

- Durable Job；
- Attempt / generation fencing；
- Retry Safety；
- Scheduler；
- Multi-worker；
- Credential / Workspace / Environment-aware placement；
- Policy / Audit；
- Artifact provenance。

因此不要把产品演化成“另一个 Happy / CC Pocket”，而应把它们证明有效的控制交互，建立在 computecloud 的可靠执行语义之上。

## 8. 可复用的设计模式

### 8.1 必须采用

1. **主机执行、客户端控制**：客户端不复制仓库、CLI 登录目录或模型密钥。
2. **Server 单事实源**：客户端缓存只是投影，断线后按版本和事件游标恢复。
3. **读共享、写仲裁**：多设备可以观察；同一交互会话仅一个活动写控制租约。
4. **明确证据状态**：网络失联是 `UNKNOWN/UNVERIFIABLE`，不是执行已退出。
5. **幂等控制操作**：提交、取消、审批、输入和重试都带 `operation_id`；响应丢失不能盲目重放。
6. **Attempt fencing**：控制请求携带预期 Attempt 和 generation，旧客户端不得影响新 Attempt。
7. **内容最小化推送**：APNs/FCM 只传不敏感的通知类别和 opaque event ID，内容回到应用后拉取。
8. **可撤销设备身份**：每台设备独立密钥、Token、最后活动时间和撤销状态。
9. **Runtime capability negotiation**：UI 行为必须来自 Adapter 明确宣告能力，而不是按 Agent 名字硬编码。
10. **协议优先于 PTY**：PTY 可以作为调试/终端能力，但不是权威业务状态源。

### 8.2 不进入首版

- 手机本地运行完整 Codex/Claude Worker。
- 手机 IDE、完整终端模拟器和 Git 冲突解决器。
- 把第三方 Relay、账号体系或私有云作为 computecloud 正确性依赖。
- 把 PTY 文本解析成权威任务状态。
- 为客户端提前引入 PostgreSQL、Redis、MQ、独立 BFF 或微服务网格。
- 在未经原生协议验收时声称支持执行中输入或原地审批。
- 为了“支持更多模型”把 Agent Runtime 降级为普通 Chat Completion Provider。

## 9. 对产品路线的影响

客户端控制面是 Agent Job Executor 的产品表面，不改变 computecloud 的核心定位，也不是新的 AI Execution OS：

- v0.3.x 已完成 Stage、multi-Attempt、Retry Safety、Artifact Lifecycle、Workspace、Long-running、Fair Scheduling 等 Reliability Kernel 主体。
- v0.4.x 在 Runtime / Tool / Environment 基础上，继续强化 Runtime Adapter、SessionRef、Approval、Prepared Workspace。
- C1/C2 Control PWA 应直接基于 Agent Control API / Event API，而不是绑定某个 Runtime CLI。
- v0.5 在 Agent-aware Scheduler 阶段，让客户端选择“意图/Runtime/Workspace”，由 Server 选择 Worker。
- v0.6 增加设备身份、RBAC、审计、推送和可选 E2EE Relay。
- v0.7/v1.0 完成规模、弱网、升级兼容和正式商店交付。

路线需要坚持：

> **Universal Agent Client 是产品表面；Agent Gateway 是协议边界；Agent Job Executor 才是 computecloud 的核心。**

避免“先做漂亮 App、后补服务端语义”的倒置。客户端每一项可操作能力必须由已验证的 Server/Worker 契约支撑。

## 10. 后续研究 / PoC 项

建议在进入 C1/C2 前完成：

1. 固定 OvertChat、Happy、CC Pocket 的 commit/tag，做源码级协议拆解。
2. 把三者的 session / event / approval / diff / file / transport 映射成统一表格。
3. 为 Codex / Claude Code 分别写最小 Runtime Adapter contract test。
4. 增加第三个 Runtime（Gemini CLI / OpenCode 二选一）验证 Adapter 不依赖 Agent 名称分支。
5. 做“手机断线 -> Agent 继续执行 -> 重连补事件 -> generation 已变化”的恢复测试。
6. 做“旧设备审批旧 Attempt”的负向 fencing 测试。
7. 比较 HTTPS+SSE、WebSocket、E2EE Relay 三种传输；传输层不得改变 Job 正确性。
8. 明确 Control Protocol v0.1 schema，作为未来 PWA / iOS / Android 的共同契约。

## 11. 来源与核查边界

### 2026-09-28 新增核查

- [OvertChat README](https://github.com/yoloyash/overtchat/blob/main/README.md)
  - Host Connector：Codex、Claude Code、Pi、Oh My Pi、OpenCode。
  - 支持 start/resume session、plan/tool call、review change、approval。
- [Happy README](https://github.com/slopus/happy)
  - Mobile/Web/macOS client；Claude Code / Codex；E2EE。
- [CC Pocket README](https://github.com/K9i-0/ccpocket)
  - Mobile client + Bridge Server + Codex/Claude；WebSocket；approval、diff、files、Git、弱网恢复。
- [Oriveo README](https://github.com/oriveo/oriveo)
  - iOS/Android/Web BYOK Multi-LLM；OpenAI/Anthropic/Gemini + compatible endpoint。
  - 作为 Universal LLM Client 对照组，不视为完整 Coding Agent Control Plane。

### 原始研究来源

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

调研是截至 2026-09-28 的源码和公开文档快照。第三方项目变化很快；采用代码前必须固定 commit/tag、核对许可证、运行安全审计和目标环境 PoC。