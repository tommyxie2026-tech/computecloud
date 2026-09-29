# UI-01 C1 Observe Read Contract

- 日期：2026-09-30（JST）
- 状态：已实现，待本变更合入 main 后生效
- 上位计划：[v0.4.5 后路线图核对](./v0.4.5-roadmap-reconciliation.md)
- Agent Control 计划：[agent-control-protocol-plan.md](./agent-control-protocol-plan.md)
- 设计边界：[client-control-plane.md](../design/client-control-plane.md)

## 1. 目标

为 ACP-6 / C1 Observe PWA 提供稳定、只读、可恢复的 collection API，不新增第二事实源。

本切片只实现 UI-01：

- 分页 Job collection；
- Worker read projection；
- snapshot watermark；
- Server epoch reset；
- owner/project 隔离；
- Runtime / Control capability 投影；
- 复用现有 Agent Control read CI。

不实现 UI-02 PWA，不开放新的写控制能力。

## 2. API

### GET /v1/jobs

首屏：

```text
GET /v1/jobs?limit=25
```

响应包含：

```text
server_epoch
snapshot_ms
snapshot_id
jobs[]
next.created_at_ms
next.job_id
has_more
```

后续页必须回传：

```text
epoch
snapshot_ms
snapshot_id
before_created_ms
before_id
```

排序固定为：

```text
created_at_ms DESC, job_id DESC
```

snapshot 使用 `created_at_ms + job_id` 双水位，避免同一毫秒新 Job 穿透分页快照。

Job collection 只返回当前 Principal：

```text
owner == principal.owner
project in principal.projects
```

不返回冻结 Spec、Credential、Secret 或 Runtime 私有状态。

### GET /v1/workers

响应只投影当前用户可见 Project 对应的 Worker：

```text
worker_id
online
slots
active
last_seen_ms
runtimes[]
  profile
  version
  capabilities
  control_capabilities
```

不返回：

- credentials；
- repositories；
- policy 原文；
- secret；
- Worker token / identity material。

Worker collection 是实时投影；`observed_at_ms` 表示观察时间，不宣称是历史快照。

## 3. Epoch / 恢复语义

`server_epoch` 是客户端缓存的恢复边界。

客户端规则：

1. 首次 Bootstrap/List 获取 epoch。
2. 后续分页必须回传 epoch。
3. Server restart、backup restore 后 Server epoch 改变。
4. 旧 epoch 请求返回 `409 SNAPSHOT_EPOCH_CHANGED`。
5. 客户端必须丢弃 collection cursor/snapshot，重新 Bootstrap + 首屏。
6. per-Job durable event replay 仍由既有 Job seq / SSE 契约负责。

因此不需要创建独立客户端事件总线或 client-side durable truth。

## 4. 实现

新增：

- `internal/server/control_collection.go`
- `internal/server/control_collection_test.go`

更新：

- `internal/server/http.go`
- `scripts/ci_agent_control_read.py`

没有 schema migration。

## 5. 测试

已覆盖：

- stable two-page Job pagination；
- 同 snapshot 后新增 Job 不穿透；
- owner isolation；
- project isolation；
- stale epoch -> reset；
- Worker project scoping；
- Worker online/runtime/control capability projection；
- Worker pagination；
- HTTP route contract。

测试进入现有：

```text
agent-control-read
```

不新增重复 workflow。

## 6. C1 客户端使用顺序

```text
GET /v1/control/bootstrap
        |
        +--> server_epoch
        +--> runtime capability matrix
        |
GET /v1/jobs
        |
        +--> snapshot watermark
        |
select job
        |
        +--> /sessions
        +--> /tasks
        +--> /approvals
        +--> /artifacts
        +--> /events + SSE
        |
GET /v1/workers
```

UI-02 必须消费这些公开 API，不读取 SQLite，不直连 Worker，也不从 PTY 推断权威状态。

## 7. 下一步

UI-02：

- 创建 `clients/control`；
- Expo Web / React Native shared client；
- C1 read-only dashboard；
- Job list/detail；
- Session/Event/Approval attention；
- Worker/Runtime capability；
- Artifact/Diff；
- reconnect + epoch reset；
- `control-client-e2e`：真实 Server + fixture Worker，无模型额度依赖。
