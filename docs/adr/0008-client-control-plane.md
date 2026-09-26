# ADR-008：客户端控制面采用薄客户端与服务端事实源

- 日期：2026-09-26
- 状态：接受
- 决策范围：computecloud Web、iOS、Android 控制客户端
- 关联：[调研](../research/agent-control-client-landscape-2026.md)、[技术方案](../design/client-control-plane.md)、[长期路线图](../implementation/long-term-roadmap.md)

## 背景

computecloud v0.3.2 已是轻量 Agent Job Executor：单 Go 二进制、一个活动 Server、多 Worker、本机 SQLite、Job/Stage/Task/Attempt、Token HTTP/MCP、固定 Map→Reduce、Retry Safety、Artifact Lifecycle、事件和取消。下一步需要让用户在手机和 Web 上观察并控制长时间运行的 Agent 工作。

Orca、Paseo、Happy、Soromi、Codex Mobile 和 Claude Code Remote Control 均验证了“稳定主机执行、移动设备控制”的产品模式。直接 Fork 这些项目会引入第二套任务状态、Daemon、Relay、账号或工作区语义，并与 computecloud Server 的权威状态冲突。

## 决策

1. 新增 **computecloud Control** 产品表面；它是现有 Agent Job Executor 的客户端，不是新的调度器。
2. computecloud Server 继续作为 Job、Task、Attempt、Worker、租约、操作回执、Artifact 和审计的唯一事实源。
3. 客户端采用无状态/弱状态设计：本地只保存设备凭据、用户偏好和可重建缓存，不保存权威任务状态。
4. 第一可用版本为响应式 Web/PWA；验证工作流后使用 React Native + Expo 共享 Web、iOS、Android 代码。
5. v0.3/v0.4 直接连接现有 HTTPS API，并增加 SSE；不提前增加独立 BFF、MQ 或外部数据库。
6. 公网/NAT 场景得到验证后，才增加可选 E2EE Relay。Relay 只转发密文，不参与调度、租约或状态判断。
7. 手机永不成为通用 Worker。受限 Android 边缘执行若未来存在需求，作为单独 Worker 类型设计和验收。
8. 客户端新增写操作必须具备权限范围、`operation_id`、预期资源版本和 Attempt generation fencing。

## 选择理由

- 保持 Go + SQLite + 单活动 Server 的轻量边界。
- 不复制已有 Job Executor 语义，不引入双事实源。
- PWA 可先验证任务观察、审批和控制的真实价值，再承担应用商店成本。
- Expo 能共享多端界面和协议代码，同时原生提供安全存储、推送、二维码和生物识别能力。
- 可替换传输让直连、VPN 和 Relay 共享同一上层协议。

## 后果

正向后果：

- Server/Worker 正确性不依赖 App、推送或 Relay 在线。
- 客户端可独立发布，并通过 capabilities 协商兼容旧 Server。
- 丢失手机只需撤销设备身份，无需轮换模型供应商凭据。
- Web、iOS、Android 使用同一资源模型和命令语义。

代价：

- 首版不提供完整移动终端和代码编辑体验。
- 交互式审批和追加输入必须等待 Runtime 适配器完成真实协议验收。
- 原生 App 与推送会增加 TypeScript/Expo、App Store 和 Play Console 的发布维护成本。
- E2EE Relay 需要独立威胁模型和密钥生命周期验证，不能以普通 WebSocket 转发代替。

## 被否决方案

| 方案 | 原因 |
| --- | --- |
| Fork Orca/Paseo 作为新控制面 | 引入第二套 Daemon/状态与大型依赖；长期合并成本高 |
| 手机直接运行 Agent Worker | 后台限制、电量、热管理、存储和 CLI/工具链不满足长任务可靠性 |
| 首版直接做原生双端 | 在控制契约未稳定前扩大 UI 和发布成本 |
| 首版引入独立 BFF、PostgreSQL、Redis、MQ | 与当前轻量单体约束冲突，且尚无容量证据 |
| 只做远程桌面/PTY 转发 | 无法可靠表达 Job 状态、审批、Attempt fencing 和 Artifact |

## 复审条件

出现以下任一证据时重新评估：

- 独立客户端流量使单 Server 的 HTTP/SQLite 写入成为已测量瓶颈。
- 企业网络明确禁止直连/VPN，且 E2EE Relay 成为主要连接方式。
- App Store 交付成本显著高于移动 Web收益。
- 有可验证需求要求手机承担受限边缘 Worker，并能满足操作系统后台执行约束。
