# computecloud 文档

| 文档 | 内容 | 状态 |
| --- | --- | --- |
| [Go 多客户端 Agent RPC 调度实施方案](design/agent-orchestration-go.md) | 技术选型、架构、Go 执行器、会话、持久化、调度、恢复与里程碑 | 实施设计，待开发 |
| [Agent Runtime v1 接口与事件契约](contracts/agent-runtime-v1.md) | 任务字段、RPC 方法、输入语义、事件、错误、状态与幂等约束 | 契约草案，待编码验证 |

建议先阅读实施方案，再按契约实现服务与适配器。设计核查日期为 2026-09-22；客户端接口升级必须重新执行兼容性验收。
