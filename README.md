# computecloud

当前执行台账：[P0–P2 实施与验收](docs/implementation/p0-p2-execution.md)（2026-10-06）。

轻量的 **Agent-aware Distributed Job Execution Platform**。使用 Go、gRPC 和本机 SQLite，把 Agent Job 可靠地分配到远程 Worker 执行，并围绕 Runtime、Tool、Environment、Workspace、Artifact、Retry、Control 与 Goal-oriented Computing 建立可恢复、可审计的执行语义。

computecloud 的产品本质仍然是 **Agent Job Executor**，不是通用 Workflow Engine、LLM Gateway、Model Serving Platform 或 AI Execution OS。

## 当前状态

- 稳定发布基线：**[v0.4.6 Post-0.4.5 Stabilization](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.6)**
- v0.4.6 已通过完整 CI、package、multi-arch container 与 release；发布范围和升级步骤见 [v0.4.6 部署指南](docs/deployment/production-v0.4.6.md)，验收证据见 [v0.4.6 发布记录](docs/implementation/v0.4.6-release-status.md)
- main 功能基线：**v0.4.x Runtime / Tool / Environment Ecosystem**
- 已完成：EnvironmentCapability、EnvironmentProvider Execution、Runtime/Tool/Environment 分层、Agent Control ACP-4a durable approval
- 当前功能主线：**Prepared Workspace / Workspace Template**
- Relay/P2P 当前进度：**设计已纳入 v0.4.x，代码实现尚未开始**；v0.4.x 只交付实验性 Transport foundation，`direct` 仍为默认路径
- Relay 设计与实施计划：[ADR-021](docs/adr/0021-relay-assisted-p2p-transport.md) / [v0.4.x Relay 传输计划](docs/implementation/relay-p2p-transport-plan.md)
- 下一步 Relay：**RLY-1 Transport seam**；RLY-2～RLY-5 按独立 contract、security-negative、fallback、restart-recovery Gate 逐步实施
- Goal-oriented Computing：计算模型与 Re-plan Guard RPG-1～RPG-3 持久化原语已实现；Goal/Plan/Graph/Evaluator 与实际执行路径的集成尚未完成
- RPG：固定只做 RPG-1～RPG-4，RPG-4 完成后正式关闭，不继续 RPG-5
- 当前分支 Server schema：**v16**（Goal 治理迁移；需依次应用 v15/v16）（v0.4.6 tag 为 v13）；Worker schema：**v6**
- 下一轮任务与缺口：[v0.4.5 后续发布计划](docs/implementation/v0.4.5-follow-up-release-plan.md)；Relay RLY-1 及 Goal/生产验收仍按路线图独立推进；PWA、Goal 闭环与生产基线不能由本版发布状态推定完成
- 发布状态以 [GitHub Actions](https://github.com/tommyxie2026-tech/computecloud/actions) 与 [GitHub Releases](https://github.com/tommyxie2026-tech/computecloud/releases) 为准；代码实现完成不等于已发布

当前演进关系：

~~~text
Product Mainline
v0.4.x Runtime / Tool / Environment + Relay Foundation (experimental)
        ↓
Prepared Workspace / Workspace Template
        ↓
v0.5 Agent-aware Scheduler
        ↓
v0.6 Enterprise Governance
        ↓
v0.7 Scale & Resilience
        ↓
v1.0 Stable Platform

Goal-oriented Computing
Goal -> Plan -> Execution Graph -> Scheduler -> Worker
     -> Artifact -> Evaluator -> Re-plan Guard
                              ↓
                    ALLOW / REJECT /
                    NEEDS_APPROVAL
~~~

### Re-plan Guard 当前进度

以下 RPG-1～3 状态指 Guard 原语及其测试，不表示 Guard 已接入 Server 的 Attempt 创建与自动 Re-plan 执行闭环。

~~~text
RPG-1  ✅ Bound
       max_replans / attempts / wall-time / generation fencing

RPG-2  ✅ Evidence
       failure_class / evidence fingerprint / duplicate plan guard

RPG-3  ✅ Convergence
       strategy cycle / repeated failure / progress guard

RPG-4  FINAL
       approval / token-cost budget / policy / audit
       ↓
       RPG CLOSED
~~~

RPG 是一次性建立 Re-plan 安全边界的实施序列，不是长期产品路线。RPG-4 完成后，后续能力分别归入 Goal Governance、Provider Contract、Adaptive Scheduling 或普通维护。

## 计算模型

最高层计算语义采用 **Goal-oriented Computing**：

~~~text
Goal
  ↓
Plan
  ↓
Execution Graph
  ↓
Scheduler
  ↓
Worker
  ↓
Artifact
  ↓
Evaluator
  ↓
Re-plan Guard
  ├── ALLOW -> new Plan revision / Graph generation
  ├── REJECT
  └── NEEDS_APPROVAL
~~~

底层 durable execution model 继续保留：

~~~text
Job
  ↓
Stage
  ↓
Task
  ↓
Attempt
~~~

两层关系：

~~~text
Goal / Plan / Execution Graph
            ↓
        Graph Node
            ↓
      Job / Stage
            ↓
           Task
            ↓
          Attempt
~~~

因此现有 Job API 不需要一次性重写；Legacy Job 可以逐步映射成 Synthetic Goal / Plan / Graph。

## 已实现能力

### Durable Agent Job Execution

- Job / Stage / Task / Attempt
- Attempt generation 与 fencing
- one active Attempt per Task
- late event / artifact / completion rejection
- Retry Safety
- Artifact lifecycle 与 provenance
- Workspace lifecycle
- 长任务与 lease 机制
- Fair Scheduling 基础
- SQLite WAL / backup / restore

### Runtime / Tool / Environment

- Runtime API v2
- Codex / Claude CLI Adapter
- RuntimeCapability
- ToolCapability
- EnvironmentCapability
- EnvironmentProvider Execution
- Runtime / Tool / Environment 解耦
- Worker capability advertisement

### Agent Control

Control 客户端是现有执行系统的产品表面，不是第二个调度器，也不建立独立事实源。

当前已具备：

- durable control operation ledger
- principal-scoped idempotency
- Attempt / generation / resource-version fencing
- structured Worker control command
- SessionControlProvider execution path
- structured ACK / completion event
- fail-closed capability handling

手机端不作为通用 Worker；离线客户端不缓存危险写操作；Push 不携带提示词、代码或审批正文。

### Re-plan Safety

自动 Re-plan 当前必须同时满足：

~~~text
BudgetAvailable
AND CurrentGeneration
AND NewEvidence
AND NewPlan
AND NoRecentStrategyCycle
AND FailureClassNotRepeated
AND ProgressNotStalled
~~~

因此 Re-plan 是 **bounded autonomy**，而不是无限自治。

## 当前主线：Prepared Workspace

下一功能主线是 **Prepared Workspace / Workspace Template**，目标是降低 Agent Job 在远程 Worker 上的重复准备成本，同时保持 Workspace 可恢复、可审计。

主要方向：

~~~text
repository baseline
dependency / image fingerprint
preinstalled Runtime / Tool
cached checkout
warm workspace
workspace template
environment fingerprint
prepare latency
startup latency
~~~

它属于 Agent Job 执行性能与恢复能力，不发展成通用 IDE / Dev Environment 产品。

RPG-4 会作为旁路收尾完成 Approval / Budget Governance，但不会改变上述主线优先级。

## 构建与试跑

需要 Linux、Go 1.26+ 和 Git；当前验证工具链为 Go 1.27.1。CLI 烟测额外需要 Python 3.12+，不调用模型或消耗账号额度。

~~~sh
make build
./bin/computecloud version
make test
make smoke
make ci-container-image
~~~

`make ci-container-image` 使用 Docker Buildx 构建并验证 Server/Worker 的 linux/amd64 + linux/arm64 OCI 镜像，同时执行镜像体积预算和本机 smoke。容器部署与 GHCR tag 规则见 [多架构容器镜像部署](docs/deployment/container-images.md)。

macOS 原生验证使用系统进程接口；`make race` 需要 Xcode Command Line Tools。详见 [macOS 验证指南](docs/validation/macos-local.md)。

make smoke 临时启动一个 server、两个 Worker 进程和协议测试程序，验证执行、取消、崩溃恢复、事件与产物、备份；结束后清理临时目录。

make capacity-check 运行轻量容量工具自测与小矩阵；make capacity 运行完整 Worker/槽位矩阵并输出 JSON。指标口径与真实部署清单见[容量与部署验收](docs/validation/capacity.md)。

make ci-flow 启动真实 Server 和两个 Worker 进程，使用 Codex/Claude 协议 fixture 模拟 single、Map/Reduce、取消和 Worker 故障，并生成可归档 JSON 报告；流程见 [CI 任务执行流程模拟](docs/validation/ci-task-flow.md)。

主分支 CI 通过后会生成 Linux amd64/arm64 压缩包和 SHA256SUMS。正式版本可由版本化 release request 或语义版本标签触发。

## 部署原则

- 一个 Go 二进制同时提供 server、worker 和 CLI。
- 容器交付仍使用同一个 binary，只提供轻量 server image 与 runtime-compatible worker base image 两种 filesystem target。
- Server 容器使用 scratch/non-root；Worker base 使用 Debian slim + Git/SSH，Agent Runtime CLI 不打入基础镜像。
- 一个活动 server 通过 gRPC 管理多个主动连接 Worker。
- Server 与 Worker 各自使用本机 SQLite。
- 不要求 PostgreSQL、Redis、Kafka、etcd 或外部 Workflow Engine。
- Worker 主动出站连接，适合私有网络 / BYO Worker 场景。
- Secret / Credential 通过引用和最小权限传递，不进入普通 Artifact/Event。
- HA 只在真实 SLO / SQLite contention / RTO 数据证明有必要时再评估。

核心原则：

> **先模块化，再服务化。**

## 产品边界

computecloud 持续做深：

- durable Agent Job execution
- Runtime / Tool / Environment integration
- Workspace / Artifact lifecycle
- Agent-aware scheduling
- Credential / repository / workspace affinity
- private worker governance
- approval / policy / audit

以下不作为本产品线性演进：

~~~text
AI Execution OS
Model Serving Platform
Model Deployment
KV Cache Fabric
Training Scheduler
General GPU Cloud
General Workflow Engine
General LLM Gateway
~~~

如果上层 AI 平台需要复用 computecloud：

~~~text
External AI Platform
        ↓
Goal / Agent Job Contract
        ↓
computecloud
        ↓
Worker / Runtime / Tool / Environment
~~~

## 文档

核心文档：

- [文档索引](docs/README.md)
- [长期路线图](docs/implementation/long-term-roadmap.md)
- [3–5 人并行开发计划](docs/implementation/parallel-development-plan.md)
- [Agent-aware 总体架构](docs/design/agent-job-executor-architecture.md)
- [ADR-017：Goal-oriented Computing Model](docs/adr/0017-goal-oriented-computing-model.md)
- [ADR-018：Re-plan Guard 与自治循环防护](docs/adr/0018-replan-guard-loop-prevention.md)
- [ADR-019：Prepared Workspace Core](docs/adr/0019-prepared-workspace-core.md)
- [Prepared Workspace Core 实施记录](docs/implementation/prepared-workspace-core.md)
- [ADR-020：多架构最小容器镜像交付](docs/adr/0020-multiarch-container-packaging.md)
- [多架构容器镜像部署与发布](docs/deployment/container-images.md)
- [RPG-1 Re-plan Guard Foundation](docs/implementation/rpg-1-replan-guard-foundation.md)
- [RPG-2 Evidence Guard](docs/implementation/rpg-2-evidence-plan-guard.md)
- [RPG-3 Loop / Progress Guard](docs/implementation/rpg-3-loop-progress-guard.md)
- [Control 客户端控制面技术方案](docs/design/client-control-plane.md)
- [Agent Control Protocol](docs/design/agent-control-protocol.md)
- [v0.2 Token 网关与 Map/Reduce 设计](docs/design/gateway-mapreduce-v0.2.md)
- [v0.2 Job / MCP / 网关接口契约](docs/contracts/job-gateway-v0.2.md)
- [v0.2 运行与升级指南](docs/implementation/v0.2-runbook.md)
- [容量与部署验收](docs/validation/capacity.md)
- [版本记录](CHANGELOG.md)

## 项目方向一句话

> **computecloud = Goal-aware control semantics + durable Agent Job execution + remote Worker runtime.**

更具体地说：

> **可靠、可恢复、可治理地把 Agent Job 分配到合适的远程 Worker，并让 Runtime、Tool、Environment、Workspace、Artifact、Evaluator 和 Control 都服从同一个执行事实源。**
