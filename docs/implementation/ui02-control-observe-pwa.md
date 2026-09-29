# UI-02 C1 Observe PWA

- 日期：2026-09-30（JST）
- 状态：已实现，待本变更合入 main 后生效
- 前置：[UI-01 C1 Read Contract](./ui01-control-read-contract.md)
- 上位计划：[Agent Control Protocol 实施计划](./agent-control-protocol-plan.md)
- 客户端设计：[Control 客户端控制面](../design/client-control-plane.md)

## 1. 目标

UI-02 为 computecloud 提供首个可运行的 C1 Observe 客户端。它只消费公开 Agent Control / Job read API，不读取 SQLite、不直连 Worker、不解析 PTY 作为权威状态，也不开放写控制。

实现目录：

```text
clients/control/
  App.tsx
  src/api.ts
  src/types.ts
  scripts/e2e.mjs
```

采用 Expo Web / React Native shared client，当前交付目标是 Web/PWA 观察面，为后续 iOS/Android 共享实现保留代码结构。

## 2. 已实现能力

- Bootstrap 与 protocol/server epoch 展示；
- Job 分页、snapshot watermark 与 epoch reset；
- Job detail；
- Task / Attempt 投影；
- Session / Runtime capability；
- Approval attention；
- Artifact 列表与下载入口；
- Durable Job events；
- SSE replay；
- Worker / Runtime capability；
- 断线后按 durable read truth 重新加载。

本阶段不实现 submit、cancel、input、approval decision、retry、resume 或 offline mutation queue；这些写能力归 UI-03 / C2 Operate。

## 3. Collection 契约补强

UI-02 E2E 暴露出真实契约缺陷：无 pending approval 时，Server 曾返回 `{\"approvals\":null}`。现固定为 `{\"approvals\":[]}`，并新增回归测试。

## 4. CI

新增：

```text
control-client-check
control-client-e2e
```

`control-client-check` 验证 npm lockfile、TypeScript、Expo Web build 和客户端静态边界。

`control-client-e2e` 使用真实 computecloud Server + fixture Worker + fixture Runtime，覆盖 3 个 terminal Job、分页、Worker projection、Job/Task/Session/Approval/Artifact/Event detail、SSE 连续 replay、terminal watermark、stale epoch reset 和 token 不进入 URL；不调用真实模型。

## 5. 安全与产品边界

- PWA 只读；
- Bearer token 只通过 Authorization header；
- URL 不携带 token；
- Artifact 仍由 Server 鉴权；
- Server epoch 变化时丢弃 collection cursor；
- 客户端不保存 Provider 主密钥；
- 无 Runtime capability 时不推断或伪造交互能力。

## 6. 下一步

UI-03 / C2 Operate：先实现短期 single-writer lease，再按已认证 Runtime capability 分批开放 submit/cancel/input/approval/retry/resume；所有 mutation 必须 operation id + resource version + Attempt/generation，弱网后读取 durable receipt 而不是盲目重放。