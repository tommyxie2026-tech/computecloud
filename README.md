# computecloud

轻量的多 Agent、多模型、多节点任务调度服务。使用 Go、gRPC 和本机 SQLite，调度 Codex / Claude Code 非交互 CLI。

## v0.1.0

一个 `computecloud` 二进制提供 server、worker、任务、产物和备份命令。一个活动 server 管理多个主动连接的 Worker；无需 PostgreSQL、Redis、消息队列或工作流服务。

已实现批任务提交/去重、节点与模型匹配、节点和账号并发限制、事件回放、取消、进程组清理、租约失联处理、可信验收命令、结果包及离线备份。每次执行使用固定 Git commit 的独立工作区。

首版适用于**受信 Linux 节点与任务**。会话 resume、执行中追加输入、在线审批、自动重试、容器隔离、GUI 和控制平面高可用尚未实现；不支持的 RPC 能力会明确拒绝。

## 构建与试跑

需要 Linux、Go 1.26+ 和 Git；当前验证工具链为 Go 1.27.1。CLI 烟测额外需要 Python 3.12+，不调用模型或消耗账号额度。

```sh
make build
./bin/computecloud version
make test
make smoke
```

`make smoke` 临时启动一个 server、两个 Worker 进程和协议测试程序，验证执行、取消、崩溃恢复、事件与产物、备份；结束后清理临时目录。

实际接入从 [运行指南](docs/implementation/v0.1-runbook.md) 开始，按 [examples](examples) 配置本机已经安装和登录的 Codex / Claude CLI。配置中的版本、模型、仓库和 commit 占位符需要替换。

```sh
./bin/computecloud server --config examples/server.yaml
./bin/computecloud worker --config examples/worker.yaml
./bin/computecloud task submit --config examples/client.yaml --file examples/task.json
./bin/computecloud task watch --config examples/client.yaml --id TASK_ID
```

多机部署使用 TLS 与每节点独立令牌；Worker 只需要向 server 发起连接。数据库位于各自本机磁盘，不通过网络文件系统共享。

## 文档

- [文档索引](docs/README.md)
- [实施计划与进度](docs/implementation/v0.1-plan.md)
- [运行、部署和恢复指南](docs/implementation/v0.1-runbook.md)
- [v0.1 验证记录](docs/validation/v0.1-results.md)
- [版本记录](CHANGELOG.md)
- [已生成的 gRPC 契约](api/agent/v1/runtime.proto)
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
- 已实现启动去重、事件补传、取消确认和 SQLite 离线备份恢复；Session 能力延后。
- 持续交互、跨节点会话迁移、通用 DAG 和控制平面高可用延后。

本地验证使用可控协议程序；真实模型账号及两台独立主机验收仍需在目标环境执行。通过项和未执行项分别记录，测试程序结果不代表真实客户端已通过兼容性验收。
