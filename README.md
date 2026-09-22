# computecloud

计算资源与 Agent 执行能力的统一调度项目。

## 当前状态

本次提交建立 **Go 实现的 Codex / Claude Code 多客户端 RPC 调度设计**。文档中的服务、接口、数据表和目录均为实施设计，不表示已经实现或完成运行验证。

## 文档

- [文档索引](docs/README.md)
- [Go 多客户端 Agent RPC 调度实施方案](docs/design/agent-orchestration-go.md)
- [Agent Runtime v1 接口与事件契约](docs/contracts/agent-runtime-v1.md)

## 已确定的技术方向

- 调度器、Worker、客户端适配器使用 Go。
- 平台通过 gRPC 提交任务、控制执行并订阅事件。
- 首版由 Go 启动 Codex / Claude Code 非交互 CLI，读取结构化事件。
- 使用 PostgreSQL 保存任务、执行尝试、控制请求和事件；按任务隔离工作区。
- 持续会话、执行中追加输入和原生审批回传按客户端能力逐步启用。
- 先完成单调度器、多执行槽位的可靠闭环，再扩展多节点和任务依赖图。

具体实施阶段、验收条件和能力限制见设计文档。
