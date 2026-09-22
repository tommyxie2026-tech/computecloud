# computecloud 文档

| 文档 | 内容 | 状态 |
| --- | --- | --- |
| [Go 多客户端 Agent RPC 调度实施方案](design/agent-orchestration-go.md) | 技术选型、架构、Go 执行器、会话、持久化、调度、恢复与里程碑 | 实施设计，待开发 |
| [Agent Runtime v1 接口与事件契约](contracts/agent-runtime-v1.md) | 任务字段、RPC 方法、输入语义、事件、错误、状态与幂等约束 | 契约草案，待编码验证 |
| [GitHub 同类实现调研与借鉴边界](research/agent-orchestration-landscape.md) | 多 Agent / 多模型 / 多节点能力比较、固定源码快照、复用边界 | 文档及部分源码核查，未部署实测 |
| [ADR-001：SQLite 与轻量部署决策](adr/0001-sqlite-lightweight.md) | 单二进制、单活动 server、内置队列、SQLite 事务与备份、扩展边界 | 按用户要求确定，待实现验证 |
| [多节点 PoC 与故障验收计划](validation/multi-node-poc.md) | 两节点、两执行器、并发、恢复、故障注入、轻量部署门槛和证据模板 | 验收计划，尚未执行 |

建议依次阅读实施方案、ADR、接口契约和验收计划；调研文档用于追溯选型依据。设计核查日期为 2026-09-22；客户端接口升级必须重新执行兼容性验收。

文档使用两类状态：第三方能力标明官方文档、源码或作者验收记录；computecloud 能力统一标为设计或待验证。第三方测试结果不代表本项目已经通过测试。
