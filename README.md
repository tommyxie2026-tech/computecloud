# computecloud

当前执行台账：[P0–P2 实施与验收](docs/implementation/p0-p2-execution.md)。
v0.4.x 收尾：[实施计划](docs/implementation/v0.4-closeout-plan.md) / [v0.4.9 迭代计划](docs/superpowers/plans/v0.4.9-release-iteration.md) / [双机预检](docs/validation/dual-host-preflight.md)。

轻量的 **Agent-aware Distributed Job Execution Platform**。使用 Go、gRPC 和本机 SQLite，把 Agent Job 可靠地分配到远程 Worker 执行，并围绕 Runtime、Tool、Environment、Workspace、Artifact、Retry、Control 与 Goal-oriented Computing 建立可恢复、可审计的执行语义。

computecloud 的产品本质仍然是 **Agent Job Executor**，不是通用 Workflow Engine、LLM Gateway、Model Serving Platform 或 AI Execution OS。

## 当前状态

- 稳定发布基线：**[v0.4.9](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.9)**。Linux amd64/arm64 归档和 Server/Worker 多架构容器已发布；发布证据见 [v0.4.9 发布记录](docs/implementation/v0.4.9-release-status.md) 与[部署指南](docs/deployment/production-v0.4.9.md)。
- v0.4.9 已包含 PR #148 的 Agent HTTP 长请求处理修复，以及 PR #150 的 Worker 部署加固与 Claude Attempt Gateway。真实 Provider、独立双 Worker 升级恢复、OCI/egress 负向、证书轮换撤销、NAT Relay、缓存性能和供应商硬账单上限仍待目标环境验收；CI 发布不代表公网生产认证完成。
- 当前功能基线：Agent Job Executor、Runtime / Tool / Environment、Prepared Workspace、synthetic Goal、Control 与实验性 Relay。
- 已实现并有 CI 覆盖：Goal 原子 Plan/Job 发布和受限 `claude_http` 估算费用控制、Relay direct-first 运维基础、Worker strict OCI 边界、Codex/Claude Attempt Gateway 合约及敏感值脱敏。
- Server schema：**v19**；Worker schema：**v6**。
- 仍待目标环境验收：固定版本真实 Runtime、独立双 Worker 故障/升级恢复、代表性 Linux Worker 缓存性能、真实 NAT Relay、OCI/egress 隔离负向测试、mTLS 轮换/撤销、公网源站保护及供应商账单硬上限。CI 通过不等于这些验收完成，也不代表可接纳任意不可信公网租户。
- v0.4.9 发布收尾记录见[迭代计划](docs/superpowers/plans/v0.4.9-release-iteration.md)；v0.5.0 主线仍是 Agent-aware Scheduler，保持确定性软评分、默认关闭。
- 发布状态以 [GitHub Actions](https://github.com/tommyxie2026-tech/computecloud/actions) 与 [GitHub Releases](https://github.com/tommyxie2026-tech/computecloud/releases) 为准；代码合入不等于版本已发布。

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

以下 RPG-1～3 状态指 Guard 原语及其测试。Server 已完成 synthetic Goal 的原子 Attempt 预留、Guard → Plan/Job 发布、一次性批准消费与限定 Runtime 费用上限。自动 Re-plan 仍关闭；RPG 完整闭环仍需固定版本真实 Runtime 和独立主机验收。

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

### 原生 Claude / Codex Job 客户端（实验性，默认关闭）

`/agent/v1/messages` 和 `/agent/v1/responses` 将受限的纯文本对话提交为持久化 Worker Job。每个 API Key 绑定一个固定执行档案；仓库、提交、Runtime、模型、凭据、工具策略和项目都由 Server 配置，客户端不能覆盖。客户端的工具声明仅作为元数据，不会在客户端重复执行；Worker 继续按既有 Job 策略执行。断开 SSE 只停止观察，显式取消请求会进入既有 Job 清理流程。

HTTPS 路径要求 TLS 1.3 和正常证书验证。也可单独启用只绑定 loopback 的 HTTP 监听，并通过校验 SSH 主机密钥的本地端口转发访问；此选项不会放宽 Worker TLS。响应字节上限按 UTF-8 边界截断，客户端的 token 上限不会被伪装成 Runtime 硬预算。协议范围、配置样例和验收状态见[原生对话 Job 合约](docs/contracts/native-conversation-jobs.md)与[客户端部署指南](docs/deployment/native-conversation-clients.md)。

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

## 当前主线：Prepared Workspace 验收

Prepared Workspace / Workspace Template 与缓存、warm pool、有界 GC 和观测已合入 main。当前重点是用代表性工作负载复核准备时延、并发影响和独立主机上的完整 Job 效果，同时保持 Workspace 可恢复、可审计。本机 macOS 与 Linux CI provider fixture 已达到缓存 P50 ≤ cold P50 40% 的目标；真实 Worker/Job 性能验收仍待执行。

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

Goal Governance 决策与审计已接入 Control；原子发布与限定 `claude_http` 估算费用强制已通过 CI。RPG-4 仍需真实 Provider、恢复路径和独立主机验收，不能由 fixture 结果推定生产闭环完成。

## 构建与试跑

需要 Linux、Go 1.27.2+ 和 Git；该补丁版本包含当前已知的 Go 标准库安全修复。CLI 烟测额外需要 Python 3.12+，不调用模型或消耗账号额度。

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
- [执行级 Token 可观测与效率治理实施计划](docs/implementation/execution-token-observability-roadmap.md)（规划中；非通用 Token Gateway）
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
