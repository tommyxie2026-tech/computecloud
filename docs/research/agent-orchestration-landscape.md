# GitHub 同类实现调研与借鉴边界

- 核查日期：2026-09-22
- 范围：多 Agent、多模型、多节点，尤其是 Go 对 Codex / Claude Code CLI 的 RPC 管理
- 证据：官方 README、架构/状态文档、部分关键源码和仓库元数据；未部署这些项目做多机实测
- 当前结论：按用户要求采用 Go + SQLite + 内置队列；第三方项目用于借鉴，外部引擎不作为首版依赖
- 关联：[主设计](../design/agent-orchestration-go.md)、[SQLite 决策](../adr/0001-sqlite-lightweight.md)

## 1. 比较口径

多 Agent 指可以并行管理独立任务或会话；多模型指有实际模型/Provider 配置和执行通路；多节点指任务在多台机器执行且具有节点分配/状态管理。远程 UI 连接一台机器，或在同机启动多个 CLI，都不能单独证明多节点调度。

通用任务引擎可以支撑跨节点执行，但还需要 coding agent 适配、会话与工作区管理。本调研区分“现有功能”“作者报告的验收”“本项目的推断/建议”，不以项目介绍或 stars 证明生产可靠性。

## 2. 候选比较

| 项目 | 技术与 Agent 能力 | 多节点边界 | computecloud 采用范围 |
| --- | --- | --- | --- |
| [dsh-alpha](https://github.com/songofhawk/dsh-alpha) | JavaScript；Codex、Claude Code、Kimi 等 | 反向 WebSocket Worker，能力/仓库/负载路由；有作者多设备验收记录 | 借鉴远端协议、能力登记及仓库亲和，不作为 Go 首版依赖 |
| [agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) | Go daemon；多 coding agent、会话和工作区 | 当前 STATUS 定位单用户本地运行；远程界面不等于集群执行 | 优先参考 Go 适配、事件、会话与生命周期 |
| [artificial](https://github.com/AndreBaltazar8/artificial) | Go；Claude、Codex、ACP 等 Harness | Worker 可连接 Hub；所查默认 spawn 路径仍在本机起进程 | 参考小型 Hub/Worker 结构，不认定为完整集群调度 |
| [kagent](https://github.com/kagent-dev/kagent) | Go 控制器、Agent 运行时；多模型与工具 | 依赖 Kubernetes 调度部署；不是开箱即用 CLI 执行器 | 首版不引入；已有 K8s 且平台范围扩大时再评估 |
| [Hatchet](https://github.com/hatchet-dev/hatchet) | Go 引擎及 Go SDK；通用任务/工作流 | 多 Worker 分发和并发槽位，CLI 语义需自建 | 未来外部引擎备选；不符合当前最小依赖目标 |
| [Temporal](https://github.com/temporalio/temporal) | Go 服务端及 Go SDK；持久工作流 | Worker 拉取持久任务；原生 Agent 恢复另行处理 | 未来复杂长流程备选；不作为首版前置条件 |

前四行的事实来自下列固定源码快照；Hatchet / Temporal 的 Worker 行为还核对了官方文档。各项目对“Agent”的定义不同，不能把通用模型调用框架的支持数量等同于已适配的 CLI 数量。

## 3. 值得借鉴的实现

### 3.1 agent-orchestrator：Go 执行与会话

所查 STATUS 明确为单用户本地 Go daemon；包含会话生命周期、事件回放、原生会话持久化和控制器代次。Codex Chat 使用 app-server，Claude Code 使用 claude-agent-acp。其已有接入不改变 computecloud 对实验性接口、版本验收和原生能力协商的要求。[A1]

本项目借鉴 Session 与 Process 分离、重复控制防护、工作区及事件机制；不照搬桌面 UI，也不把产品内叫作 worker 的会话自动解释为远端机器。

### 3.2 dsh-alpha：跨机器控制

README 描述 Worker 主动连接主控、注册能力、报告仓库位置与负载，并提供事件、取消和审批回传。[D1] 作者的验收记录报告两台 Linux Worker 运行真实 CLI 的核心闭环；其断线用例要求运行任务失败、重连后可再次派发，没有承诺活动进程无缝迁移。[D2]

本项目采用相同的主动连接思路，协议改为 Go gRPC。断线后使用本项目租约/核查语义，不能从第三方作者结果推定本项目已通过验收。GitHub 元数据没有识别出明确许可证；如需复制源码，先核对授权，本次只记录设计参考。

### 3.3 artificial：小型服务与进程管理

它提供 Go 服务、Hub 和 cmd-worker。所查 `spawnWorkerForEmployee` 使用本机 `exec.Command(workerBin, ...)`，并将 server 参数设置为 localhost；Worker 接口的远程连接能力不等于已经实现远端节点分配与故障接管。[B1][B2]

其较小的组件划分适合参考。PTY 兼容方案不能直接替代稳定结构化协议；computecloud 首版仍选择已验证的 CLI JSON 输出。

## 4. 外部引擎与 Kubernetes 的适用边界

Hatchet 的官方文档描述多个 Worker 注册相同任务后分担执行，并以 slots 限制单 Worker 并发。[H2] Temporal 的官方文档说明 Worker 通过任务队列拉取持久化工作。[T2] 这些能力能够减少通用编排开发，但也增加独立服务和运维面。

kagent 将 Agent、模型配置与工具集成进 Kubernetes 平台。[K1] 对 computecloud 当前“轻量、SQLite、管理现有 CLI”的目标，直接引入这些体系会扩大首版范围。因此当前选择内置任务队列；这是一项需求匹配判断，不是对其吞吐或可靠性的排名。

未来即使采用外部引擎，仍需区分工作流恢复、RPC 重连和原生 Agent 恢复；引擎重试不能保证外部文件修改、push 或发布恰好一次。

## 5. 不作为新核心依赖的项目

[coder/agentapi](https://github.com/coder/agentapi) 的当前仓库已归档，README 明确说明 deprecated、停止维护。[X1] 可阅读其 API 封装思路，本项目不将其作为新核心依赖。本次没有对其他未列出的项目作“不存在”的结论。

## 6. 对主设计的具体影响

| 观察 | 本项目决定 |
| --- | --- |
| Go 项目已有丰富 CLI/会话管理 | 适配器和状态机用 Go，小接口复用思路，不绑定桌面产品 |
| 远端 Worker 主动连接可穿过常见网络边界 | 使用认证的反向 gRPC 流，节点不开放额外入站控制端口 |
| Session 状态与代码文件状态不同 | 同节点明确恢复优先，跨节点能力单独验收 |
| 多模型不等于任意 CLI 可用任意 endpoint | 冻结受信 model/provider 配置，按节点能力匹配 |
| 完整工作流系统扩大组件数量 | 首版单 server + SQLite + 内置队列，保留必要去重/核查 |
| README 和作者测试不是本项目测试 | 两节点、两执行器独立验证，失败/未执行如实登记 |

## 7. 固定源码与官方链接

以下为核查时的 commit 快照，便于后续对照。许可证仅记录仓库元数据，不替代具体文件授权检查；本次没有复制第三方实现代码。

| 项目 | 核查快照 | 元数据许可证 |
| --- | --- | --- |
| agent-orchestrator | `bb4ba2b9f71c715c5b1649ccc356ed6b5fa18318` | Apache-2.0 |
| artificial | `e284d737502e24a30a8a4146f607de947a6ac90f` | MIT |
| dsh-alpha | `68e12320e3c249755d04aa8eff3fef8252fc89ed` | 未识别 |
| kagent | `e8961b8b582e11c852b176acd9f1687ab8ad66a0` | Apache-2.0 |
| Hatchet | `aef11d48421802e96aeb29a7f2fefc7bb69e5276` | MIT |
| Temporal | `543367eec690da3c23e536cf5dfe76928af11b2c` | MIT |
| agentapi | `7c468d5b25ec9d3d76c41313cf4e08904ff5968d` | MIT；已归档 |

- [A1：agent-orchestrator STATUS](https://github.com/Untrivial-ai/agent-orchestrator/blob/bb4ba2b9f71c715c5b1649ccc356ed6b5fa18318/docs/STATUS.md)
- [B1：artificial Worker 启动](https://github.com/AndreBaltazar8/artificial/blob/e284d737502e24a30a8a4146f607de947a6ac90f/src/svc-artificial/internal/server/api.go)
- [B2：artificial Worker 入口](https://github.com/AndreBaltazar8/artificial/blob/e284d737502e24a30a8a4146f607de947a6ac90f/src/cmd-worker/cmd/worker/main.go)
- [D1：dsh-alpha README](https://github.com/songofhawk/dsh-alpha/blob/68e12320e3c249755d04aa8eff3fef8252fc89ed/README.md)
- [D2：dsh-alpha 多设备验收](https://github.com/songofhawk/dsh-alpha/blob/68e12320e3c249755d04aa8eff3fef8252fc89ed/docs/multi-device-acceptance.md)
- [K1：kagent README](https://github.com/kagent-dev/kagent/blob/e8961b8b582e11c852b176acd9f1687ab8ad66a0/README.md)
- [H1：Hatchet README](https://github.com/hatchet-dev/hatchet/blob/aef11d48421802e96aeb29a7f2fefc7bb69e5276/README.md)
- [H2：Hatchet Workers](https://docs.hatchet.run/v1/workers)
- [T1：Temporal 源码快照](https://github.com/temporalio/temporal/tree/543367eec690da3c23e536cf5dfe76928af11b2c)
- [T2：Temporal Task Queues](https://docs.temporal.io/task-queue)
- [T3：Temporal Go SDK](https://github.com/temporalio/sdk-go)
- [X1：agentapi README](https://github.com/coder/agentapi/blob/7c468d5b25ec9d3d76c41313cf4e08904ff5968d/README.md)
