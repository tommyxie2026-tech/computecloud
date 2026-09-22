# computecloud

计算资源与 Agent 执行能力的统一调度项目。

## 当前状态

项目处于 **Go 实现的多 Agent、多模型、多节点 RPC 调度设计阶段**，首批执行器为 Codex / Claude Code。已补充 GitHub 实现调研、轻量化存储选型、分布式职责边界及两节点验收计划。文档中的服务、接口、数据表和目录均为实施设计，不表示已经实现或完成运行验证。

## 文档

- [文档索引](docs/README.md)
- [Go 多客户端 Agent RPC 调度实施方案](docs/design/agent-orchestration-go.md)
- [Agent Runtime v1 接口与事件契约](docs/contracts/agent-runtime-v1.md)
- [GitHub 同类实现调研与借鉴边界](docs/research/agent-orchestration-landscape.md)
- [ADR-001：SQLite 与轻量部署决策](docs/adr/0001-sqlite-lightweight.md)
- [多节点 PoC 与故障验收计划](docs/validation/multi-node-poc.md)

## 已确定的技术方向

- 一个 Go 二进制，提供 server、worker 和任务操作子命令。
- 一个活动 server，通过 gRPC 管理多台 Worker；Worker 主动连接。
- server 和各 Worker 分别使用本机 SQLite；内置任务队列和调度循环。
- 无需部署外部数据库、消息队列或工作流引擎；Hatchet / Temporal 仅作未来备选。
- 首批适配 Codex / Claude Code 非交互 CLI，模型和凭据由受信配置选择。
- 受信任务使用独立工作区与进程监督；容器按隔离需求启用。
- 保留启动去重、事件补传、取消确认、Session 独占和 SQLite 备份恢复。
- 持续交互、跨节点会话迁移、通用 DAG 和控制平面高可用延后。

具体实施阶段、验收条件和能力限制见设计文档。
