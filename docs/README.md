# computecloud 文档

| 文档 | 内容 | 状态 |
| --- | --- | --- |
| [v0.1 实施计划与进度](implementation/v0.1-plan.md) | 交付阶段、完成状态与后续工作 | 第一版已实现，本地验证完成 |
| [v0.1 运行指南](implementation/v0.1-runbook.md) | 构建、CLI 接入、TLS、多机、恢复、设计差异 | 当前可用命令的依据 |
| [v0.1 验证记录](validation/v0.1-results.md) | 自动测试、二进制故障烟测与覆盖边界 | 本机双 Worker 已验证；真实 CLI/独立双机未执行 |
| [Go 多客户端 Agent RPC 调度实施方案](design/agent-orchestration-go.md) | 完整目标架构、调度、持久化、恢复与里程碑 | v0.1 实现批任务子集；扩展仍为设计 |
| [Agent Runtime v1 接口与事件契约](contracts/agent-runtime-v1.md) | 目标字段、RPC、事件、状态与幂等约束 | 已实现子集以 runtime.proto 为准 |
| [GitHub 同类实现调研与借鉴边界](research/agent-orchestration-landscape.md) | 多 Agent / 多模型 / 多节点项目比较与复用边界 | 官方文档及部分源码核查，未部署实测 |
| [ADR-001：SQLite 与轻量部署决策](adr/0001-sqlite-lightweight.md) | 单二进制、内置队列、SQLite 与备份 | 核心路线已落地，本地存储/备份验证通过 |
| [多节点 PoC 与故障验收计划](validation/multi-node-poc.md) | 两台独立主机、两执行器、完整故障矩阵 | 完整计划未执行；局部映射见验证记录 |

运行第一版先阅读实施进度和运行指南；设计、ADR 与调研用于追溯目标及选型。核查日期为 2026-09-22，客户端升级必须重新执行兼容性验收。

实现事实以代码、[Proto](../api/agent/v1/runtime.proto) 和 v0.1 验证记录为准。第三方测试不算本项目测试，fixture 测试不算真实模型或独立双机验收。
