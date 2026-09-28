# Agent Control Protocol 实施计划

- 项目：computecloud
- 日期：2026-09-28
- 状态：实施中；ACP-0/ACP-1/ACP-2/ACP-3 已完成并进入 main；ACP-4 Approval + active-Attempt Session Resume 已实现，待 PR CI/合并
- 当前主线：v0.4.4 EnvironmentProvider Execution 已完成
- 设计：[Agent Control Protocol 与 Runtime Adapter](../design/agent-control-protocol.md)
- 客户端设计：[Control 客户端控制面](../design/client-control-plane.md)
- 调研：[Agent 客户端控制端方案调研](../research/agent-control-client-landscape-2026.md)
- 长期路线：[长期路线图](./long-term-roadmap.md)

## 1. 实施原则

该计划是 **v0.4.x–v0.6.x 的横向能力线**，不替代 Prepared Workspace / Agent-aware Scheduler 主线。

原则：

1. 先协议与 contract，后 UI。
2. 先只读事件面，后写控制。
3. 先两个 Runtime 共用，再引入第三个 Runtime 验证抽象。
4. 所有写动作先做幂等和 fencing，再开放客户端。
5. Control Client 不直接连接 Runtime。
6. Runtime Adapter 不直接拥有 Job 状态。
7. 每个阶段必须新增 CI Gate 和负向测试。

## 2. 阶段总览

| 阶段 | 对齐主线 | 目标 | 产物 |
| --- | --- | --- | --- |
| ACP-0 | 当前 | Schema/contract 基线 | **已完成**：control.v1alpha1、事件 schema、Provider extension、独立 CI |
| ACP-1 | v0.4.x | 只读 Agent Control 面 | **已完成**：bootstrap、Session projection、durable replay、SSE、capability |
| ACP-2 | v0.4.x | Runtime Adapter 统一 | **已完成**：Codex + Claude certified control descriptor / contract tests；main CI 36375760435 PASS |
| ACP-3 | v0.4.x | 安全写控制 | **已完成**：ACP-3a durable ledger/idempotency/fencing；ACP-3b structured Worker control envelope、SessionControlProvider、terminal ACK、Worker at-most-once ledger、UNKNOWN fail-closed；main commit `d30c45e97ffc1452e599b9477143df6f882acc1e` |
| ACP-4 | v0.4.x | Approval + Resume | **已实现待合并**：durable versioned approval、expiry/supersede/decision lock、explicit SessionRef、active-Attempt resume、Workspace/Environment compatibility、HTTP write API、独立 CI Gate |
| ACP-5 | v0.4.x/v0.5 | 第三 Runtime | Gemini CLI 或 OpenCode，验证无名称分支 |
| ACP-6 | v0.5.x | Control PWA/Mobile 接入 | C1/C2/C3 |
| ACP-7 | v0.6.x | 企业治理 | device/RBAC/audit/E2EE optional |

## 3. ACP-0 — 协议基线

### 3.1 代码

新增建议：

```text
api/control/v1alpha1/
  control.schema.json
  event.schema.json
  approval.schema.json
  capabilities.schema.json

internal/control/
  model.go
  service.go
  errors.go

internal/runtime/
  provider.go
  capability.go
  events.go
```

### 3.2 工作项

- [x] 定义 protocol version negotiation。
- [x] 定义 AgentSession schema。
- [x] 定义 RuntimeCapability enum。
- [x] 定义 Control Event envelope。
- [x] 定义 ApprovalRequest。
- [x] 定义 ControlOperation receipt。
- [x] 定义标准错误码。
- [x] JSON Schema validation tests。
- [x] Go model 与 JSON schema round-trip tests。

### 3.3 CI

新增：

`agent-control-schema`

门槛：
- schema lint；
- example validation；
- backward-compatible optional field test；
- generated Go model diff check（如采用生成）。

### 3.4 DoD

- 协议能表达 Codex/Claude 的公共控制语义；
- 不出现 runtime-specific 字段进入公共顶层模型；
- Review 通过后冻结为 `control.v1alpha1`。

## 4. ACP-1 — 只读 Agent Control 面

### 4.1 Server

实现：

```text
GET /v1/control/bootstrap
GET /v1/jobs/{id}/sessions
GET /v1/jobs/{id}/sessions/{session}
GET /v1/jobs/{id}/events/stream
GET /v1/jobs/{id}/approvals
```

### 4.2 Store

ACP-1 实际采用更轻量的投影实现：**不新增 agent_sessions 事实表**。Session 由当前 Task/Attempt/generation/runtime_session_ref 投影；Runtime event 在 Worker event 被接受/去重后镜像到既有 durable Job event replay。Approval 独立持久化延后到 ACP-4。

这样避免在只读阶段创建第二事实源，也继续复用现有 Job event 序号，不重复建设消息系统。

### 4.3 测试

- [ ] Server restart 后 cursor replay。
- [ ] duplicate event。
- [ ] cursor expired。
- [ ] dataset/epoch changed。
- [ ] 1000+ 事件分页/重连。
- [ ] Runtime capability 正确投影。

### 4.4 CI

`agent-control-event-replay`

### 4.5 DoD

PWA/CLI 能只读显示：
- 当前 Session；
- Runtime/version；
- capability；
- structured event；
- pending approval；
- Artifact/Diff 链接。

## 5. ACP-2 — Runtime Adapter Contract

### 5.1 Refactor

把 Codex/Claude 现有启动/输出/清理逻辑收敛到统一 Provider。

必须包含：

```text
Descriptor
Prepare
Start
Inspect
Subscribe
Cancel
CollectArtifacts
Cleanup
```

可选：

```text
Resume
Input
Approve
Interrupt
```

### 5.2 Codex Adapter

- [x] RuntimeRef 可持久化。
- [x] 结构化事件转换。
- [x] Cancel/Cleanup 明确区分。
- [x] Restart + Inspect。
- [x] Capability 表与 Control descriptor 交叉校验。

### 5.3 Claude Adapter

同样 contract，不允许复制另一套 Server 分支。

### 5.4 CI

`runtime-adapter-contract`

测试矩阵：

| Test | Codex | Claude |
| --- | --- | --- |
| Start | 必须 | 必须 |
| Event | 必须 | 必须 |
| Inspect | 必须 | 必须 |
| Cancel | 必须 | 必须 |
| Cleanup | 必须 | 必须 |
| Resume | 按 capability | 按 capability |
| Input | 按 capability | 按 capability |
| Approval | 按 capability | 按 capability |

### 5.5 DoD

新增 Runtime 不需要修改 Job Controller/Scheduler 状态机。

## 6. ACP-3 — 安全写控制

### 6.1 Store

新增：`control_operations`（schema v8，已实现）

当前已完成：
- `PRIMARY KEY(principal_id, operation_id)`；
- request hash；
- expected Attempt / generation / resource version；
- durable `ACCEPTED -> DISPATCHED -> COMPLETED/REJECTED` receipt；
- released Attempt fail-closed；
- duplicate same request replay / different request conflict；
- Worker protobuf `ControlCommand` 与 Assignment 分离；
- principal-scoped operation identity；
- Worker 本地 control execution ledger；
- `EXECUTING` 崩溃窗口恢复为 `UNKNOWN / EXECUTION_UNVERIFIABLE`，禁止自动重放副作用；
- `SessionControlProvider` capability check 与 fail-closed；
- structured final ACK -> durable receipt + `control.completed/control.rejected` event。

约束:
- unique(principal_id, operation_id)；
- request_hash；
- expected attempt/generation；
- durable receipt。

### 6.2 API

```text
POST /v1/jobs/{job}/sessions/{session}/inputs
POST /v1/jobs/{job}/sessions/{session}/interrupt
POST /v1/jobs/{job}/cancel
```

### 6.3 安全条件

每个请求：

1. auth；
2. project scope；
3. operation id；
4. resource version；
5. attempt id；
6. generation；
7. capability；
8. durable accept；
9. async Worker execution。

### 6.4 负向测试

- [x] old generation control -> ATTEMPT_FENCED。
- [x] duplicate same op -> same receipt。
- [x] duplicate different payload -> OPERATION_CONFLICT。
- [x] unsupported input -> CAPABILITY_UNSUPPORTED。
- [x] response lost + retry -> no duplicate side effect；已覆盖 Worker replay 与 Server offline durable receipt replay。
- [ ] offline client stale cancel -> rejected。

### 6.5 CI

`agent-control-fencing`
`agent-control-negative`（ACP-3a 已接入）
`agent-control-dispatch`（ACP-3b 已接入）
`agent-control-approval-resume`（ACP-4 已接入）

## 7. ACP-4 — Approval 与 Session Resume

### 7.1 Approval

实现完整状态：

```text
PENDING
 -> ACCEPTED
 -> REJECTED
 -> EXPIRED
 -> SUPERSEDED
```

审批必须 audit。

### 7.2 Resume

实现：

`POST /v1/jobs/{job}/sessions/{session}/resume`

要求：
- explicit SessionRef；
- capability= session_resume；
- workspace/environment compatible；
- current generation policy；
- 不使用隐式 last session。

### 7.3 测试

- [x] approval version conflict。
- [x] old Attempt / stale generation approval。
- [x] expired approval。
- [ ] Worker restart then resume：**deferred**。当前 Worker restart recovery 会终结旧 Attempt；跨 Worker epoch resume 需先调整 recovery / Prepared Workspace 语义，ACP-4 不伪造支持。
- [x] Workspace incompatible/unavailable -> resume rejected。
- [x] Environment unavailable/UNKNOWN -> explicit fail-closed。

## 8. ACP-5 — 第三个 Runtime 验证

候选：

1. Gemini CLI；
2. OpenCode。

选择标准：
- 协议公开/可自动化；
- 可在 CI 安装固定版本；
- 至少支持 stream + cancel；
- 最好支持 session resume/tool event。

验收问题：

> 接入第三个 Runtime 是否只新增 Adapter + descriptor + tests？

如果需要：
- 修改 Scheduler 名称判断；
- 修改 Job 状态机；
- 新增 runtime-specific Control API；

则返回设计阶段重构。

## 9. ACP-6 — Control PWA / Mobile

### 9.1 C1 Observe PWA

先做：

- Job list；
- Session；
- events；
- approval attention；
- artifact；
- diff；
- worker/runtime capability。

不做写控制。

### 9.2 C2 Operate PWA

仅开放已通过 ACP-3/4 的操作：

- submit；
- cancel；
- input；
- approval；
- retry；
- resume。

### 9.3 C3 Mobile Beta

React Native + Expo：

- iOS；
- Android；
- QR device pairing；
- Push；
- deep link；
- secure credential storage。

Mobile 仍通过同一 Agent Control API，不新增 mobile-only 业务协议。

## 10. ACP-7 — Governance / Remote

对齐 v0.6：

- Device Identity；
- OIDC/SSO；
- RBAC；
- Audit；
- Write Lease；
- Push minimization；
- Optional Blind E2EE Relay；
- Trust Domain。

Relay 必须通过：
- ciphertext-only inspection；
- replay test；
- downgrade rejection；
- key rotation；
- reconnect。

## 11. 与主路线的排期关系

建议执行顺序：

```text
v0.4.4 done
   |
   +--> 主线：Prepared Workspace
   |
   +--> ACP-0 Schema/Contract
             |
             v
         ACP-1 Read Plane
             |
             v
         ACP-2 Adapter Contract
             |
   主线 Session/Approval foundation ready
             |
             v
         ACP-3/ACP-4 Write Plane
             |
             v
         ACP-5 Third Runtime
             |
v0.5 Scheduler ----> ACP-6 Mobile
             |
v0.6 Governance --> ACP-7
```

ACP-0/1 可以与 Prepared Workspace 并行；
ACP-3/4 不得超前于 Runtime 原生能力。

## 12. Issue 拆分建议

### Epic A — Control Protocol

- A1 Schema/version；
- A2 Event envelope；
- A3 Session projection；
- A4 Bootstrap/capability；
- A5 Event replay。

### Epic B — Runtime Adapter

- B1 Provider interface；
- B2 Codex adapter；
- B3 Claude adapter；
- B4 contract test harness；
- B5 third runtime。

### Epic C — Safe Control

- C1 operation receipt；
- C2 generation fencing；
- C3 input；
- C4 interrupt/cancel；
- C5 approval；
- C6 resume。

### Epic D — Client

- D1 PWA read；
- D2 PWA operate；
- D3 mobile shell；
- D4 device pairing；
- D5 push。

### Epic E — Governance

- E1 RBAC；
- E2 audit；
- E3 write lease；
- E4 E2EE relay；
- E5 compatibility matrix。

## 13. 测试层级

```text
L1 Unit
  schema / state / hash / fencing

L2 Fixture Integration
  fake runtime provider / event replay

L3 Multi-process Fault Injection
  server restart / worker restart / disconnect / stale client

L4 Real Runtime
  fixed Codex / Claude / third runtime

L5 Client E2E
  browser/mobile -> server -> worker -> runtime
```

关键故障必须覆盖：

- Server 在 control intent commit 后崩溃；
- Worker 执行后 ack 前断线；
- Client 收到 202 前断线；
- Attempt retry 后旧 App 恢复；
- approval 在 decision 前被 supersede；
- session resume 时 Workspace 已回收。

## 14. CI Gate 合并策略

PR 只有通过相关 Gate 才可合并：

```text
agent-control-schema
agent-control-event-replay
runtime-adapter-contract
agent-control-fencing
agent-control-negative
control-client-check
real-runtime-control-smoke   # 可按环境/夜间运行
```

Package Gate 在 ACP-2 后至少依赖：

```text
runtime-adapter-contract
agent-control-fencing
```

## 15. 指标与成功标准

技术：

- control write duplicate side effect = 0；
- stale generation mutation = 0；
- event replay gap = 0；
- Runtime adapter contract pass = 100% certified runtime；
- Server restart 后可恢复/明确 UNVERIFIABLE；
- 第三个 Runtime 接入不修改 Scheduler core。

产品：

- 手机/浏览器可观察所有运行中 Agent Job；
- 用户能准确看到“等待审批/输入”；
- 可以安全恢复 Session；
- 客户端切换不影响 Worker 执行；
- Runtime 切换不要求改客户端。

## 16. 当前最小下一步

建议紧接当前工作执行：

1. 完成 ACP-4 PR CI 并合并；Package Gate 必须依赖 `agent-control-approval-resume`。
2. ACP-3c 已随本轮收口：HTTP input / interrupt / approval / resume 全部复用 `acceptAndDispatchControlOperation`，不存在旁路写状态机。
3. 保留 Worker-restart Session Resume 为显式 deferred item；在 Prepared Workspace / recovery contract 支持跨 epoch 恢复前不得宣称支持。
4. 进入 ACP-5：接入 Gemini CLI 或 OpenCode 作为第三 Runtime，验证 Adapter/Scheduler/Control API 无 Runtime 名称分支。
5. 增加 multi-process fault injection：Server intent commit 后重启、Worker Provider side-effect 后 ACK 前断线、旧 generation Client 恢复。
6. Codex/Claude 继续不暴露 Input/Approval/Resume/Interrupt，直到各自原生能力 contract 通过；fixture 只用于验证抽象。
7. Prepared Workspace 主线继续推进，不与 Control write plane 混成同一状态机。

这一顺序可以最小化返工，同时确保 Control App 不先于服务端语义成熟。
