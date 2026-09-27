# ADR-015：EnvironmentCapability Registry 与 Policy 分层

- 状态：Accepted
- 日期：2026-09-28
- 目标版本：v0.4.3
- Issue：[#31](https://github.com/tommyxie2026-tech/computecloud/issues/31)
- 依赖：[ADR-012 Runtime API v2](0012-runtime-api-v2.md)、[ADR-014 ToolCapability](0014-tool-capability.md)

## Context

v0.4.0 已经把 capability namespace 拆成 `runtime:* / tool:* / environment:*`，但 Environment 仍只是 Runtime Provider 的附属字符串。这样无法区分 Runtime 理论兼容环境、Worker 实际具备的环境能力、Policy 允许环境以及 Job 明确要求环境。

v0.4.3 只建立 Environment capability/policy contract，为后续 container/VM/external sandbox Provider 提供稳定边界，不在本版本实现完整 Sandbox execution。

## Decision

### 1. Environment Registry 独立

新增 `internal/environment` Registry。Descriptor 至少包含：

~~~text
name
version
isolation_class
filesystem_mode
network_mode
legacy_default
~~~

首个内建 Environment：`process`，表示当前可信 Worker process + workspace 模式。

### 2. Runtime compatible != registered Environment

Runtime Provider 的 `CapabilitySet.Environment` 只表示兼容性。Worker 实际广告：

~~~text
Runtime-compatible Environment
        ∩
registered Environment
        =
environment:* advertisement
~~~

未注册 Environment 不得进入 WorkerHello。

### 3. Job Environment requirement

Job Execution 新增可选 `environment`。省略时等价 `process`。接受后冻结为：

~~~text
environment:<name>
~~~

并沿用 generic Scheduler capability matching。

### 4. Worker Policy

Policy 新增 `allowed_environments`。空列表仅允许 `legacy_default` Environment；非空列表为显式 allowlist。

Environment 校验必须发生在 Workspace prepare 和 Runtime Prepare/Start 之前。拒绝返回 `ENVIRONMENT_POLICY_DENIED`；未注册或 Runtime 不兼容返回 `CAPABILITY_UNAVAILABLE`。

### 5. TemplateDigest

`allowed_environments` 属于 Policy，因此自动进入 TemplateDigest，继续受既有 template mismatch fencing 保护。

## Invariants

~~~text
Environment Registry independent from Runtime Registry
Worker advertises only registered AND Runtime-compatible environments
Job environment requirement immutable after acceptance
Policy denial happens before Workspace/Runtime preparation
legacy Jobs default to process
Scheduler contains no Environment-name special cases
Environment capability does not change Attempt/Artifact/Workspace ownership semantics
~~~

## Non-goals

- container/VM execution implementation；
- external sandbox API；
- Prepared/warm Workspace；
- network policy enforcement implementation；
- filesystem snapshot/checkpoint；
- v0.5 Agent-aware scoring；
- RBAC / HA。