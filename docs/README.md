# computecloud 文档

| 文档 | 内容 | 状态 |
| --- | --- | --- |
| [Agent-aware Distributed Job Execution Platform 总体架构](design/agent-job-executor-architecture.md) | Job/Stage/Task/Attempt、Runtime/Tool 分层、Artifact/Workspace 生命周期与 Agent-aware Scheduler | 当前目标架构 |
| [Agent-aware 长期演进路线图](implementation/long-term-roadmap.md) | v0.2.x 到 v1.0：可靠性内核、Agent Runtime/Tool、Agent-aware Scheduler、治理与规模化 | 当前长期主路线 |
| [AI Execution OS 总体架构](design/ai-execution-os-architecture.md) | 早期大平台方向探索 | 历史探索；已被 ADR-003 替代 |
| [v0.2 网关与 Map/Reduce 设计](design/gateway-mapreduce-v0.2.md) | 双入口、分片/汇总、投递、取消、恢复、计量与升级 | 已实现，fixture 验证通过 |
| [v0.2 接口与数据契约](contracts/job-gateway-v0.2.md) | HTTP/MCP、Worker 扩展、SQLite、Responses 边界 | v0.2 已实现，固定子集 |
| [v0.2 实施计划与验收](implementation/v0.2-plan.md) | M1–M6、G1、R1 依赖与 V01–V22 验收 | 代码与本地验证完成；真实环境待验收 |
| [v0.2 运行与升级](implementation/v0.2-runbook.md) | 开关、模板、CLI/MCP、网关、迁移与回退 | 当前操作依据 |
| [v0.2 正式部署指南](deployment/production-v0.2.md) | 产物核验、TLS、配置、systemd、验收、升级与回退 | v0.2.0 部署依据 |
| [v0.2 验证记录](validation/v0.2-results.md) | 自动测试、故障烟测与未覆盖项 | 本地通过，真实环境待验收 |
| [CI 任务执行流程模拟](validation/ci-task-flow.md) | single、Map/Reduce、取消、Worker 故障与结构化报告 | GitHub Actions 自动执行 |
| [容量与部署验收](validation/capacity.md) | fixture 容量工具、指标口径、完整矩阵与真实双机门槛 | 本机矩阵通过，真实环境待验收 |
| [v0.2 接入示例](examples/v0.2/README.md) | Job JSON Schema、请求及 Codex/Server 配置 | v0.2 示例；需要替换部署引用 |
| [ADR-002：网关与 Map/Reduce](adr/0002-gateway-mapreduce.md) | 双入口和固定两阶段作业的决策依据 | 已实施 |
| [ADR-003：Agent Job Executor 产品边界](adr/0003-agent-job-executor-product-scope.md) | 推翻 AI Execution OS 线性演进，固定 Agent Job Executor 产品边界 | Accepted |
| [ADR-004：Agent-aware 执行语义](adr/0004-agent-aware-execution-semantics.md) | Stage、Artifact/Workspace、Runtime/Tool 分层、Agent-aware Scheduler 与 G1 边界 | Accepted |
| [v0.1 实施计划与进度](implementation/v0.1-plan.md) | 交付阶段、完成状态与后续工作 | 第一版已实现，本地验证完成 |
| [v0.1 运行指南](implementation/v0.1-runbook.md) | 构建、CLI 接入、TLS、多机、恢复、设计差异 | 旧 Task 与基础部署依据 |
| [v0.1 验证记录](validation/v0.1-results.md) | 自动测试、二进制故障烟测与覆盖边界 | 本机双 Worker 已验证；真实 CLI/独立双机未执行 |
| [Go 多客户端 Agent RPC 调度实施方案](design/agent-orchestration-go.md) | 完整目标架构、调度、持久化、恢复与里程碑 | v0.1 实现批任务子集；扩展仍为设计 |
| [Agent Runtime v1 接口与事件契约](contracts/agent-runtime-v1.md) | 目标字段、RPC、事件、状态与幂等约束 | 已实现子集以 runtime.proto 为准 |
| [Agent-aware 产品与竞品调研（2026）](research/agent-job-execution-product-landscape-2026.md) | OpenHands、Agents API、Microsoft Agent Framework、Cursor、Sandbox、Temporal/Ray 等产品比较及 computecloud 市场定位 | 2026-09-24 公开资料调研 |
| [GitHub 同类实现调研与借鉴边界](research/agent-orchestration-landscape.md) | 多 Agent / 多模型 / 多节点项目比较与复用边界 | 官方文档及部分源码核查，未部署实测 |
| [ADR-001：SQLite 与轻量部署决策](adr/0001-sqlite-lightweight.md) | 单二进制、内置队列、SQLite 与备份 | 核心路线已落地，本地存储/备份验证通过 |
| [多节点 PoC 与故障验收计划](validation/multi-node-poc.md) | 两台独立主机、两执行器、完整故障矩阵 | 完整计划未执行；局部映射见验证记录 |

运行第一版先阅读实施进度和运行指南；设计、ADR 与调研用于追溯目标及选型。核查日期为 2026-09-24，客户端升级必须重新执行兼容性验收。

实现事实以代码、[Proto](../api/agent/v1/runtime.proto) 和对应版本验证记录为准。第三方测试不算本项目测试，fixture 测试不算真实模型或独立双机验收。

v0.2 网关/Job 已落地，实际边界以运行指南及验证记录为准；旧主设计中的 Session、交互、通用 DAG 和 R1 重试仍是后续目标。
