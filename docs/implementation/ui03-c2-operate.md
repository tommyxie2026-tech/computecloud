# UI-03a C2 Operate — Single-Writer Control

- 项目：computecloud
- 日期：2026-09-30
- 状态：实现完成，待 CI/PR 验证
- 对齐：[Agent Control Protocol 实施计划](agent-control-protocol-plan.md)
- 前置：[UI-02 C1 Observe PWA](ui02-control-observe-pwa.md)

## 1. 目标

UI-03a 把 C1 Observe 客户端升级为安全的 C2 Operate 基线，但不扩大 Job Executor 领域模型。

本切片仅开放已经由 ACP-3/ACP-4 证明过的控制能力：

- Job cancel；
- Session input；
- Session resume；
- Approval accept/reject。

以下能力不在 UI-03a：

- Job submit；
- manual retry；
- generic terminal；
- offline write queue；
- mobile push/action；
- enterprise RBAC/device policy。

## 2. Single-Writer Lease

C2 在开放多设备写入前增加 Job 级短租约：

```text
principal + job
      |
      v
control_write_leases
      |
      +-- holder_id
      +-- token_hash
      +-- expires_at
      +-- updated
```

规则：

1. 每个 Job 同时最多一个 write lease。
2. Lease TTL 固定 30 秒。
3. token 只在 acquire 响应返回，Server 只保存 hash。
4. Client 每 10 秒 renew。
5. renew/release 必须同时匹配 principal、job、holder、token。
6. lease 过期后其他 holder 可以接管。
7. lease 失效后 UI 立即降级为只读。
8. 不持久化 bearer token 或 write lease token。
9. 不支持离线写操作重放。
10. C4 只扩展企业设备/RBAC/审计策略，不重新定义基础 single-writer 语义。

## 3. HTTP Contract

```text
POST   /v1/jobs/{id}/control-lease
PUT    /v1/jobs/{id}/control-lease
DELETE /v1/jobs/{id}/control-lease

POST /v1/jobs/{id}/control/cancel
POST /v1/jobs/{id}/sessions/{session}/inputs
POST /v1/jobs/{id}/sessions/{session}/resume
POST /v1/jobs/{id}/approvals/{approval}
```

所有 C2 write 请求携带：

```http
X-Control-Lease: <opaque-token>
```

错误语义：

```text
WRITE_LEASE_HELD      -> 409
WRITE_LEASE_REQUIRED  -> 409
WRITE_LEASE_INVALID   -> 409
```

传统 `POST /v1/jobs/{id}/cancel` 保持兼容，供既有 CLI/API 使用；Control PWA 使用新的 lease-protected cancel 路径。

## 4. Capability Gate

UI 不根据 Runtime 名称决定按钮。

```text
interactive_input -> Send input
session_resume    -> Resume
approval PENDING  -> Accept / Reject
```

没有认证 capability 时不显示对应操作。

Codex / Claude / Gemini 是否出现交互按钮，只由 Worker 上报的 certified ControlDescriptor 决定。

## 5. Bootstrap

`GET /v1/control/bootstrap` 的 `read_only` 不再固定：

- 有 `jobs:control` scope -> `read_only=false`
- 无 `jobs:control` -> `read_only=true`

只读 principal 无法 Take control。

## 6. 数据库

Server schema v13：

```text
control_write_leases
  job_id PK
  principal_id
  holder_id
  token_hash
  expires_at
  updated
```

Lease 是临时协调状态，不是新的 Job execution truth。

Job / Task / Attempt / Approval / ControlOperation 仍然保持原有事实源。

## 7. E2E

真实 Server + fixture Worker 验证：

1. 创建 3 个终态 Job，继续验证 C1 Observe。
2. 创建第 4 个长任务 Job。
3. 不带 lease 调用 control cancel -> `WRITE_LEASE_REQUIRED`。
4. device-a 获取 write lease。
5. device-b 获取同 Job lease -> `WRITE_LEASE_HELD`。
6. device-a renew 成功。
7. device-a 使用 lease cancel Job。
8. release lease。
9. Job 最终进入 CANCELED。

不产生真实模型调用。

## 8. CI

复用并升级：

- `control-client-check`：从 C1 read-only 静态检查升级为 C2 lease-aware contract 检查。
- `control-client-e2e`：增加 write lease + cancel E2E。
- `verify`：覆盖 schema v13 和 Server lease unit tests。
- `package`：继续依赖上述 Gate。

## 9. UI 行为

Job Detail：

- Take control；
- Release control；
- Cancel job。

Session：

- 有 `interactive_input` 才显示输入框；
- 有 `session_resume` 才显示 Resume。

Approval：

- 只有 PENDING + active write lease 时显示 Approve/Reject。

Footer 显示：

> C2 Operate · Writes require a short-lived single-writer lease · Bearer and lease tokens stay memory-only.

## 10. UI-03b

后续独立切片：

- Job Submit；
- manual Retry；
- 对应 idempotency/retry policy UI；
- submission/retry E2E；
- 不能复用 write lease 绕过 Retry Safety。
