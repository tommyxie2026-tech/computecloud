# ADR-014：ToolCapability Registry 与 Runtime/Policy 分层

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.4.2
- Issue：[#29](https://github.com/tommyxie2026-tech/computecloud/issues/29)
- 依赖：[ADR-012 Runtime API v2](0012-runtime-api-v2.md)、[ADR-013 Runtime Execution](0013-runtime-execution.md)

## Context

v0.4.0 已经把 capability 分为 `runtime:* / tool:* / environment:*`，但 Tool 仍由 Runtime Provider 直接声明。这样只能表达“某 Runtime 理论上兼容哪些 Tool”，不能区分：

- Worker 实际安装了哪些 Tool；
- Runtime 能使用哪些 Tool；
- Policy 允许当前 Job 使用哪些 Tool；
- Job 明确要求哪些 Tool。

v0.4.2 需要建立 ToolCapability 的独立控制面基础，同时避免提前建设通用 Tool execution proxy。

## Decision

### 1. Tool Registry 独立于 Runtime Registry

新增独立 `internal/tool` Registry。Tool Descriptor 至少包含：

~~~text
name
version
side_effect
legacy_default
~~~

`side_effect` 只允许：

~~~text
read_only
idempotent
mutating
~~~

该字段目前用于能力描述和后续策略演进，不自动推导 Job 的 replay_safe，也不自动授权 Retry。

首批内建 platform Tool：

~~~text
artifact_inputs_v1
job_io_v1
~~~

它们描述 computecloud 自身的 Job I/O 能力，不代表开放任意外部命令执行。

### 2. Runtime compatible != Worker installed

Runtime Provider 的 `CapabilitySet.Tools` 表示“该 Runtime 能兼容哪些 Tool”。

Worker 实际广告：

~~~text
Runtime-compatible Tools
        ∩
Worker Tool Registry
        =
advertised tool:* capabilities
~~~

未注册 Tool 即使 Runtime Provider 声明兼容，也不得被 Worker 广告。

### 3. Job Tool requirement

Job Execution 增加可选 `tools[]`。

提交后冻结为 Task capability：

~~~text
tools: ["job_io_v1"]
        ↓
required_capabilities:
  - tool:job_io_v1
~~~

Tool requirement 因此沿用现有 generic Scheduler capability filter，不增加 Tool-specific Scheduler 分支。

### 4. Worker Policy Gate

Worker Policy 增加 `allowed_tools`。

语义：

- 未配置/空列表：只允许 Descriptor 标记为 `legacy_default` 的平台 Tool；
- 非空列表：显式 allowlist；
- 未注册 Tool：CAPABILITY_UNAVAILABLE；
- 注册但 Policy 不允许：TOOL_POLICY_DENIED。

Worker 必须在 Runtime Prepare/Start 前完成该校验。

### 5. Template digest

`allowed_tools` 属于 Policy，因此非空 allowlist 自动进入既有 TemplateDigest。

Policy 变化会改变 template digest，并沿用现有 Server/Worker template mismatch fencing。

### 6. Namespace

新的 Tool requirement 必须使用：

~~~text
tool:<name>
~~~

Standalone Task 只允许 legacy `event_stream/cancel` 或受限的：

~~~text
runtime:<name>
tool:<name>
environment:<name>
~~~

Tool name 本身不得包含 namespace 分隔符。

## Invariants

~~~text
Tool Registry independent from Runtime Registry
Worker advertises only installed AND Runtime-compatible Tools
Job tool requirements are immutable after acceptance
Policy denial happens before Runtime Start
unregistered Tools cannot be advertised
Tool-specific logic does not enter Scheduler core
legacy Jobs without tools remain compatible
Tool side-effect class does not silently change Retry semantics
~~~

## Rejected alternatives

### Runtime Provider directly owns all Tool existence

拒绝。它无法区分 Runtime compatibility 与 Worker installation，也让 Tool 生态继续耦合 Runtime。

### v0.4.2 直接实现通用 Tool executor

拒绝。本版本只稳定 capability / policy contract。具体 Tool invocation、审批、credential、network/isolation 应按 Tool 类型逐步实现。

### Scheduler 增加 Tool 名称特例

拒绝。Scheduler 继续只处理 capability set。

## Consequences

正向：

- Runtime 与 Tool 开始真正解耦；
- Worker capability 更可信；
- Job 可以声明 Tool dependency；
- Policy 能在 Runtime 启动前 fail closed；
- 后续 MCP/Git/Browser 等能力可沿同一 Registry/Policy contract 扩展。

代价：

- 增加 Tool Registry 与 Policy 配置；
- capability compatibility window 同时存在 legacy 与 namespaced 表达；
- 后续仍需单独设计真实 Tool invocation 与 approval。

## Non-goals

- 通用命令执行 Tool；
- Browser/HTTP/MCP invocation proxy；
- interactive approval；
- EnvironmentProvider；
- Prepared Workspace；
- v0.5 Agent-aware scoring；
- Multi-tenant RBAC / HA。
