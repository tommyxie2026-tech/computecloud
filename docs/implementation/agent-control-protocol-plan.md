# Agent Control Protocol 实施计划

- 项目：computecloud
- 日期：2026-09-29
- 状态：ACP-0～ACP-5 仓库级实现已完成；ACP-4b Session Resume 已合入 main（ca2264e9d026b6cd2c2c002e41191041f533d185），ACP-5 Gemini Runtime 已合入 main（602bb4d080753b1c43f32ada88521d67222492a2）；main CI 36598835158（run #1120）全功能 Gate、verify、package、container-image 与 container-publish PASS
- 当前主线：v0.4.5 Prepared Workspace / Safe Control 为稳定发布基线；ACP-4b Resume + ACP-5 Gemini 已完成；ACP-6 C1 已实现 UI-01 只读 collection contract 与 UI-02 Observe PWA；下一切片为 UI-03 / C2 Operate
- 设计：[Agent Control Protocol 与 Runtime Adapter](../design/agent-control-protocol.md)
- 客户端设计：[Control 客户端控制面](../design/client-control-plane.md)
- 调研：[Agent 客户端控制端方案调研](../research/agent-control-client-landscape-2026.md)
- 长期路线：[长期路线图](./long-term-roadmap.md)
- CI 合并顺序恢复：[2026-09-29 CI Merge Order Recovery](./ci-merge-order-recovery-2026-09-29.md)

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
| ACP-3 | v0.4.x | 安全写控制 | **已完成**：durable ledger、idempotency/fencing、structured Worker control dispatch、terminal ACK、Worker at-most-once ledger、UNKNOWN fail-closed、HTTP input/interrupt write endpoints |
| ACP-4 | v0.4.x | Approval + Resume | **已完成**：ACP-4a durable Approval 已发布于 v0.4.5；ACP-4b current-Attempt Session Resume 已合入 main，独立 `agent-control-resume` Gate PASS |
| ACP-5 | v0.4.x/v0.5 | 第三 Runtime | **已完成（仓库级）**：Gemini CLI Provider、provider-neutral Job/Task runtime_profile、stream-json parser、transport-neutral execution、safe approval policy、legacy engine alias fencing；PR #66 / main CI PASS |
| ACP-6 | C1/C2：后续 v0.4.x；C3：v0.5.x | Control PWA/Mobile 接入 | **进行中**：C1 UI-01 read contract + UI-02 Observe PWA 已实现；下一步 UI-03 / C2 Operate，按已认证能力开放；C3 Mobile |
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

## 7. ACP-4 — Approval 与 Session Resume

### 7.1 Approval — ACP-4a 已合并

已实现：
- Server schema v12 `approval_requests`；
- Runtime `approval.requested` -> durable request；
- `approval_id + request_version` 版本身份；
- 新版本自动 supersede 旧 PENDING；
- expiry 只处理尚未被 decision 占用的 PENDING；
- Job version + Attempt generation + approval request_version 三层 fencing；
- decision durable reservation，避免 ACK 延迟与 expiry 竞态；
- structured Worker `ControlCommand(action=approval)`；
- `SessionControlProvider.Approve()`；
- Worker at-most-once control ledger；
- terminal ACK 后才转 ACCEPTED/REJECTED；
- `GET /v1/jobs/{job}/approvals`；
- `POST /v1/jobs/{job}/approvals/{approval}`；
- 独立 `agent-control-approval` CI Gate。

完整状态：

```text
PENDING
 -> ACCEPTED
 -> REJECTED
 -> EXPIRED
 -> SUPERSEDED
```

审批必须 audit。

### 7.2 Resume — ACP-4b 已完成

Resume 与 Approval 分离实施。旧 PR #50/#56 已关闭未合并；最终实现从 v0.4.5 main 重建并以 ca2264e9d026b6cd2c2c002e41191041f533d185 合入。Resume 只允许 current-Attempt transport/session rebind，复用 Prepared Workspace 的 Runtime/Tool/Environment fingerprint compatibility，不重开 released Attempt、不隐式创建 retry。`agent-control-resume` 已进入 main CI 并通过。

实现目标：

`POST /v1/jobs/{job}/sessions/{session}/resume`

要求：
- explicit SessionRef；
- capability= session_resume；
- workspace/environment compatible；
- current generation policy；
- 不使用隐式 last session。

### 7.3 测试

- [ ] approval version conflict。
- [ ] old Attempt approval。
- [ ] expired approval。
- [ ] Worker restart then resume。
- [ ] Workspace changed -> resume rejected。
- [ ] Environment unavailable -> explicit failure。

## 8. ACP-5 — 第三个 Runtime 验证 — 已完成

最终选择：**Gemini CLI**。

已实现：

- `gemini_cli` Runtime Provider；
- Gemini stream-json -> 标准 session/message/tool/runtime event；
- transport-neutral local CLI Prepare/Start/Inspect/Stop；
- 显式 `gemini_approval_mode=plan|auto_edit`，禁止隐式高权限模式；
- Job/Task `runtime_profile` provider-neutral；
- `engine=codex|claude` 仅保留为 legacy alias，并与对应 Runtime profile fencing；
- Gemini 只广告已认证 stream/cancel control capability；
- Job JSON Schema 已允许不带 legacy engine 的第三 Runtime；
- Parser / Provider / Config / Job schema / alias fencing tests 已进入全量 verify。

验收结论：

> 第三个 Runtime 接入未修改 Scheduler 核心状态机，也未新增 runtime-specific Control API。

原 `feature/acp5-gemini-runtime` 基于旧 main 落后 44 个提交，产生大面积非功能性 CI 红灯；已通过最新-main 刷新分支 PR #66 替代。

## 9. ACP-6 — Control PWA / Mobile

### 9.1 C1 Observe PWA

UI-01 已实现分页 Job list、Worker read、稳定 snapshot watermark、Server epoch reset 与 owner/project 隔离，详见 [UI-01 C1 Read Contract](ui01-control-read-contract.md)。UI-02 已交付 Expo Web / React Native shared Observe client 与真实 Server + fixture Worker E2E，详见 [UI-02 C1 Observe PWA](ui02-control-observe-pwa.md)。下一步进入 UI-03 / C2 Operate；Client owner 独立认领，WS-E review 集成，不自动扩张 WS-E ownership。

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

仅开放已通过 ACP-3/4 且原生 Runtime 对应能力已认证的操作。C2 开放多设备写入前实现短期单写者 lease；C4 只扩展企业策略。以下是目标清单，retry/resume 等不能仅凭通用控制协议存在就标为已交付：

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
- Enterprise Write Lease policy（基础单写者 lease 在 C2）；
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

## 15.1 CI 历史失败关闭状态

ACP-4b 重建期间 runs 1030–1075 的失败已按 merge 顺序复核并关闭：

- 旧分支 fixture/provider 不完整 -> superseded；
- SessionRef helper 未按原子顺序重放 -> superseded；
- ci_agent_control_resume.py 尚未进入中间 commit -> superseded；
- 单次 long-run timing failure -> 由后续更新 main 多次全矩阵 PASS 关闭。

当前 Release Truth：main@3a446b8c818fcaecf8e2b5d84e32d097b9d41b28，workflow 36598835158 全绿。后续不能根据这些历史红灯回滚 ACP-4b，也不能把多个编译失败 Gate 误判为多个业务缺陷。

## 16. 当前最小下一步

建议紧接当前工作执行：

1. ACP-0～ACP-5 不再重复实现；后续变更进入维护/兼容矩阵。
2. ACP-6 C1 Observe PWA 已完成 UI-01/UI-02；下一步 UI-03 / C2 Operate。
3. UI-03 只开放已通过 durable receipt、fencing 与原生 Runtime capability certification 的写控制，并先实现短期 single-writer lease。
4. 增加真实 Runtime 兼容矩阵：Codex / Claude / Gemini 的固定版本、协议事件、failure mode 与 capability certification。
5. Production Baseline 继续补真实多机、网络故障、24h+、upgrade/rollback；fixture CI 不替代真实环境证据。

CI 合并纪律继续保持：contract/schema -> canonical fixture -> shared lifecycle fixture -> 专用 Gate -> verify -> package/container -> main。

## 17. WS-E 认领与进度（2026-09-29）

认领：**WS-E Agent Control Approval / Integration / CI**。事实基线为
`main da086cd749e2ffa93a5a5b9d7012799505764e9f`，依据并行开发计划第 7 节、ADR-008
及本协议设计；保持单 Go Server、SQLite 和既有 durable execution truth。
并行期间已无冲突同步 `main 343e32d` 的 WS-A 增量；本分支相对 main 仍仅修改 WS-E 的 7 个文件。

### 实施计划与代码事实

| 项目 | 核对结果 / 本次目标 |
| --- | --- |
| Current State | ACP-A Runtime projection、ACP-B decision ledger、ACP-C capability fencing 已存在；重复成功 Approval ACK 会对已结束审批再次转移并报冲突；首次 ACK 缺少当前 Attempt 复核 |
| Target State | 同结果终态 ACK 幂等；冲突结果拒绝；旧 Attempt/generation 或 released Attempt 的首次 ACK 不修改 approval、receipt、command 或 event |
| Files / Packages | `internal/server/control_dispatch.go`、`internal/server/control_approval_test.go`、`internal/worker/control_*test.go`、既有审批 Gate、本实施文档与协议说明 |
| Contract Changes | 无新增公共字段/错误码；既有错误为 `CAPABILITY_UNSUPPORTED`，并行计划中的 `CAPABILITY_UNAVAILABLE` 不应视为另一个已实现契约 |
| Schema Changes | 无；Server v12 / Worker v6；开工时 README 的 v11 已由共享文档 owner 在 `35c3360` 修正，本分支不修改 README 或 migration |
| Tests | payload unit、HTTP projection/decision/retry、并发重复/冲突 ACK、stale/released ACK、Server SQLite 重开、Worker ledger 重开及 UNKNOWN 恢复、两种 builtin Runtime 的 Server/Worker unsupported matrix |
| CI Gate | 复用 `agent-control-approval`，新增 `-race`；复用 V12 schema、V1 升级/drain、migration rollback、Worker v6 migration 测试；已在 main CI/package dependency 内，无新 workflow |
| Risks | `control_dispatch.go` 与 Resume 当前 PR #60（原 #50）有交集，合并需复核；过期代次 ACK 拒绝后不自动重新执行 Runtime 副作用 |
| Merge Dependencies | 本增量只依赖已合并 ACP-4a；ACP-D bridge 依赖 WS-D C4 Goal Approval API/持久化契约 |

### 已实施增量

- 在 ACK 事务中先检查终态回执：相同规范化结果直接返回，冲突结果为 `OPERATION_CONFLICT`。
- 首次 ACK 落库前检查当前 Task/Attempt/generation、Worker 归属和 released 标记，失败为 `ATTEMPT_FENCED`。
- ACK 不比较旧 Job resource version：命令飞行期间 Job 版本可以合法前进，执行代次仍必须一致。
- 复用既有 ledger 与审计事件，不新增缓存事实源、自动 retry、Runtime 名称分支或外部基础设施。

### 验收证据

当前结论：PR #51 已合入；[v0.4.5 发布 CI 36575512360](https://github.com/tommyxie2026-tech/computecloud/actions/runs/36575512360) 全矩阵通过。下列首轮失败保留为历史记录，不再作为当前 main 阻塞。Goal bridge 仍未完成，原生 CLI 认证与真实故障注入仍独立验收。

本地 Go 1.27.1：

```sh
python3 scripts/ci_agent_control_approval.py --output /tmp/computecloud-wse-ci/report.json
```

结果：**PASS**，16 个顶层测试（含子用例），`-race`，无真实模型调用。
覆盖 Server/Worker restart、幂等、stale-generation 和既有 migration。
这些 restart 测试关闭/重开真实 SQLite 并重建对象；独立进程 kill/断网故障注入仍属于后续验收，不能等同为已覆盖。

集成证据：

- [PR #51](https://github.com/tommyxie2026-tech/computecloud/pull/51) 首轮 [CI 36496960465](https://github.com/tommyxie2026-tech/computecloud/actions/runs/36496960465) 的 `agent-control-approval`、dispatch、fencing、negative、schema 均通过。
- 首轮 vet 报告新增测试复制 protobuf 内部锁；已改为显式构造 ACK，本地 `go vet ./...` 通过。
- 同步 `343e32d` 后本地审批 Gate 再次通过（含 race）。
- 完整本地相关包回归 **FAIL**：Server 的进程执行用例出现 `CLEANUP_UNCONFIRMED` / timeout；Worker recovery 出现 `/proc/<pid>/stat` 不存在。后者已在未修改的 `main 343e32d` 工作树单独复现；Control / Store 包通过。不能以专用 Gate 通过代替完整回归通过。
- **历史集成阻塞（WS-A，发布前已解决）**：只读 prepared template 的测试清理失败：`TempDir RemoveAll cleanup: .../.git/objects/...: permission denied`。
  同一问题已在 [main CI 36496434574](https://github.com/tommyxie2026-tech/computecloud/actions/runs/36496434574/job/109176980044)
  与 PR CI 重现，影响 workspace / runtime / environment / read 等 Gate。当时本分支未修改 WS-A 文件或跳过 Gate；后续同步 WS-A 修复并通过完整发布验收。

### Workstream 状态

| 项目 | 状态 | 说明 |
| --- | --- | --- |
| ACP-A / ACP-B / ACP-C Runtime Approval | DONE（基线） | 已在 main，不重复建设 |
| ACK 幂等 / generation 防护 / 双 Runtime 负向矩阵 | DONE（v0.4.5） | PR #51 已合入，完整发布 CI 已通过 |
| ACP-D Goal Governance bridge | BLOCKED | main 只有 Re-plan Guard 的 `NEEDS_APPROVAL`，尚无 WS-D durable Goal Approval API |
| ACP-E 全矩阵 | PARTIAL | Runtime Approval 已验证；Goal approval durable case 等待 WS-D |
| Runtime Control main 集成 | DONE（v0.4.5） | 旧 WS-A / fixture 阻塞已修复；不包含 Goal bridge 或 Resume |
| WS-E 整体 | PARTIAL | 不把上述依赖标为完成，不扩展到 PWA/Resume/Goal 决策实现 |

### 后续合并依赖

WS-D 需先交付 C4 的 Goal approval read/decision API、actor/reason/audit、
plan revision / graph generation fencing 和幂等语义；如 C5 envelope 需要新增字段，
先单独提出 **CONTRACT CHANGE REQUIRED**，说明 WS-D/WS-E 影响、向后兼容和 migration，
经过约定的 contract review 后再实施 bridge。Goal 决策仍调用 Governance API，禁止转换成 Runtime `Approve()`。
