# ADR-002：双入口网关与显式 Map/Reduce

- 日期：2026-09-22
- 状态：已实施于 v0.2；真实模型与双机验收待完成
- 约束：Go、SQLite、单活动 Server，整体轻量简单
- 关联：[主设计](../design/gateway-mapreduce-v0.2.md)、[契约](../contracts/job-gateway-v0.2.md)、[实施计划](../implementation/v0.2-plan.md)

## 背景

用户希望 Server 作为 Token 网关接入 Codex，并借鉴 Google MapReduce 分配和投递多个节点上的 Agent 任务。v0.1 已有单 Task 的可靠投递、租约、取消、产物和恢复，但没有 HTTP/MCP、Job、Reduce 输入或模型代理。

模型推理调用和 Agent 作业的生命周期不同：前者需要保持工具调用协议，后者需要仓库基线、权限、长任务状态与产物。将两者隐式转换会使本地/远程工具执行位置不清，并可能让 Worker 的模型请求再次触发任务调度。

## 决策

1. 同一 Go Server 提供 Job HTTP/MCP 和可选 Responses 两类入口，复用身份、配置和 SQLite；职责独立，不增加进程或外部中间件。
2. 本地 Codex 通过 MCP 显式委派远端 Job；base_url 用于模型代理。Responses 不启动 CLI，也不直接触发 Map/Reduce。
3. 先支持 single 和一个 Map 阶段加一个 Reduce，采用明确分片和固定输入清单；不实现任意 DAG、递归任务或自动 Planner。
4. Reduce 在 Worker 上作为普通 Task 运行，受现有并发额度、租约、停止与验收约束；Server 不运行模型或代码测试。
5. v0.2 仍保持每 Task 一次 Attempt，关闭自动重试和慢任务重复执行。多 Attempt 作为 R1 单独迁移，必须证明旧执行清理和可重放性。
6. 核心新增 jobs/job_events，G1 再增加请求用量表；使用既有队列、命令和产物机制。单活动 Server 和离线备份路线不变。
7. 模型网关仅对实际经过它的调用计量；未知用量显式保留，初始 token 预算不宣称严格费用上限。

## 选项比较

| 选项 | 结果 |
| --- | --- |
| 每次 Responses 自动转远端 CLI | 暂不采用；完整工具/流/会话兼容需要独立项目 |
| Job API + MCP + 可选模型代理 | 采用；边界明确，复用 v0.1，大部分逻辑留在一个服务中 |
| 引入 Hadoop / 通用工作流引擎 | 不采用；当前固定两阶段不需要这些部署和状态模型 |
| 直接让多个 Agent 写同一仓库 | 不采用；独立工作区和集中验收更容易确定结果来源 |

## 影响

调用方要显式提供固定基线和分片，不能仅改 base_url 就自动获得远端代码工作区。新增两个 HTTPS 入口和少量表，但无需 PostgreSQL、Redis 或新的调度平台。强耦合修改仍需按阶段组织，MapReduce 不解决任意自然语言任务的自动拆解。

若出现真实的多控制节点需求、复杂跨阶段依赖、无法接受人工处理的不明执行窗口，或验证过的模型协议后端需求，再通过新 ADR 扩展；不以节点数量本身作为引入复杂组件的理由。
