# ADR-012：Runtime API v2 采用注册式 Provider 与能力命名空间

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.4.0
- Issue：[#25](https://github.com/tommyxie2026-tech/computecloud/issues/25)

## 1. Context

v0.3.x 已完成 Reliability Kernel，但 Worker 仍在两个位置直接理解具体 Agent Runtime：

- Worker probe 中显式识别 `codex_exec / claude_print`；
- execute 路径直接依赖 profile 分支决定 Args、Parser 与 Gateway eligibility。

这种结构在只有两个 Runtime 时可接受，但继续增加第三个 Agent、API-backed Runtime 或 managed Agent 时，会迫使 Worker/Scheduler 核心代码不断增长特例，违背“Provider/Runtime 解耦”和“新增 Runtime 不修改核心 Scheduler”的长期方向。

同时，现有 `Runtime.capabilities` 是单一字符串集合，Runtime 自身能力、Agent 可调用 Tool 和执行 Environment 容易混在一起。

## 2. Decision

### 2.1 Runtime Provider Registry

v0.4.0 引入内部 Runtime API v2 Provider contract：

~~~text
Provider
├── Profile()
├── Probe()
├── Args()
├── Parser()
├── Capabilities()
└── SupportsGateway()
~~~

Worker 只通过 Registry 查找 Provider，不再通过 profile 名称分支选择 Codex 或 Claude。

内置 Provider：

~~~text
codex_exec
claude_print
~~~

第三方/后续 Provider 注册后，不需要修改 Worker probe、Worker execute 或 Server Scheduler 的 profile-specific 分支。

### 2.2 Wire compatibility first

v0.4.0 不立即发布 breaking protobuf v2。

现有：

~~~text
Runtime.capabilities []string
TaskSpec.required_capabilities []string
~~~

继续保留，并通过命名空间表达语义：

~~~text
runtime:<capability>
tool:<capability>
environment:<capability>
~~~

同时 Worker 在 v0.4.0 继续广告 legacy capability 名称，保证旧 JobSpec/客户端兼容。

例如 Codex：

~~~text
runtime:event_stream
runtime:cancel
runtime:gateway_inference_v1
tool:job_io_v1
tool:artifact_inputs_v1
environment:process

# legacy compatibility
event_stream
cancel
gateway_inference_v1
job_io_v1
artifact_inputs_v1
~~~

### 2.3 Gateway capability belongs to Provider

Model Gateway eligibility 不再由：

~~~text
runtime_profile == "codex_exec"
~~~

判断，而由 Provider 的 `SupportsGateway()` 声明。

这样以后支持 Gateway 的新 Runtime 不需要修改 Worker 执行核心。

### 2.4 Probe belongs to Provider

Worker 不再固定执行 `executable --version` 的 Runtime 选择逻辑。

当前内置 CLI Provider 仍使用 pinned executable version probe；但 probe 行为已经属于 Provider contract，为后续 API-backed/self-hosted Agent Server 留出扩展点。

### 2.5 Parser belongs to Provider

Worker 不直接构造具体 Parser，而通过 Provider 获取 StreamParser。

v0.4.0 仍复用现有 Codex/Claude parser 实现；后续 Runtime 可以提供自己的 event parser，而不修改 Worker 核心。

## 3. Invariants

~~~text
Runtime dispatch is registry-driven
unknown Runtime is rejected before execution
Provider capability advertisement is deterministic
Runtime/Tool/Environment capabilities are namespaced
legacy capability requirements remain compatible
Gateway eligibility is Provider-owned
adding a new Provider does not modify Scheduler matching code
~~~

## 4. Compatibility

- Server/Worker SQLite schema 不变化；
- 现有 protobuf wire 字段不变化；
- 现有 `codex_exec / claude_print` profile 名称不变化；
- legacy required_capabilities 继续匹配；
- 新 namespaced capability 可以立即用于新 Job；
- v0.4.0 binary 仍可接受 v0.3.x JobSpec 子集。

## 5. Rejected alternatives

### 5.1 立即发布 breaking protobuf Runtime v2

拒绝。当前重点是消除实现耦合，不需要为内部 contract 重构制造 wire migration 风险。

### 5.2 每新增 Runtime 继续增加 Worker if/switch

拒绝。这会把 Runtime 生态扩展成本永久绑定到核心 Worker。

### 5.3 把 Tool 也建模为 Runtime

拒绝。Tool 与 Agent Runtime 语义不同；v0.4.x 后续会独立建设 ToolCapability。

### 5.4 v0.4.0 同时实现 API-backed Runtime / EnvironmentProvider

拒绝。先稳定 Runtime contract，再逐项增加实现，降低一次性变更面。

## 6. Consequences

正向：

- Codex/Claude 从核心分支变成 Provider；
- 第三个 Runtime 的接入边界清晰；
- Scheduler 继续只做 capability match；
- Runtime/Tool/Environment 能力开始语义分层；
- 为 API-backed Runtime、EnvironmentProvider 与 Prepared Workspace 建立稳定扩展点。

代价：

- v0.4.0 同时维护 namespaced + legacy capability；
- 当前 Provider contract 仍保留 CLI execution 假设的一部分，API-backed Runtime 需要后续继续抽象 Start/Inspect/Stop；
- wire schema 的真正 v2 清理需要未来 deprecation window 后单独决策。

## 7. Non-goals

- API-backed Runtime 正式实现；
- Tool Runtime/Browser/MCP 执行器；
- EnvironmentProvider；
- Prepared Workspace；
- interactive approval；
- session resume；
- breaking protobuf v2。
