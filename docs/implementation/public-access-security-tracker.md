# 公网访问安全加固跟踪

本台账跟踪 [公网访问安全加固设计](../superpowers/specs/2026-10-12-public-access-security-design.md) 的完整落地。公网服务仍保留 7443/gRPC 与 7444/HTTPS；“完成”要求代码、CI 和目标环境证据分别满足对应门槛。

## 总体状态

| 阶段 | 状态 | 计划 | 退出条件 |
| --- | --- | --- | --- |
| P0 公网入口与资源限制 | 进行中 | [实施计划](../superpowers/plans/2026-10-12-public-ingress-resource-controls.md) | 认证主体速率/并发/成本限额生效；公网入口拓扑、源站保护、429/过载行为通过验收 |
| P1 API Key 生命周期与 Worker mTLS | 待开始 | [实施计划](../superpowers/plans/2026-10-12-api-keys-and-worker-mtls.md) | key 创建/撤销/过期有效；A/B 独立证书验证、逐台轮换、撤销和回退完成 |
| P2 Attempt 凭据与网络隔离 | 待开始 | [实施计划](../superpowers/plans/2026-10-12-attempt-credential-and-egress-isolation.md) | 公网严格模式拒绝 process；Attempt 凭据限时限域；目标 Worker 逃逸/出站负向测试通过 |

## 跟踪规则

- 依次推进 P0 → P1 → P2；每阶段单独 PR 和验证记录。修复发现的阻塞后再进入下一阶段。
- 每次更新阶段状态时，记录 PR/commit、CI 结果、目标镜像 digest、平台和未完成验收项；不能用 CI fixture 替代公网边缘或真实 OCI 主机验证。
- 仅在所有阶段完成并有目标环境证据后标记“可面向不可信公网租户”；此前的部署范围保持受信任用户/受控租户。
- 任何凭据泄露或 mTLS 撤销演练失败都暂停扩大流量，先吊销相关凭据并复核审计。

## 当前记录（2026-10-12）

- 已完成：安全审查报告与架构设计规格；规格提交 `53cb5a1`。
- 已完成：P0/P1/P2 三份逐任务实施计划；P0 Task 1 已提交 `79cc1df`（认证 HTTP 请求限流）。
- 已完成：P0 Task 2 身份/项目作业配额与模型请求并发上限，commit `0fcb5c5`。
- 已完成：P0 Task 3 Worker gRPC session/stream 入场限制，commit `2e875cb`。
- 已完成待提交：P0 Task 4 公网边缘策略清单、部署约束文档和自动检查；P0 Task 5 安全 smoke 工具已实现并通过 mock 用例。
- 尚未完成：目标环境公网端口扫描、源站绕过拒绝、WAF/安全组配置、真实 429/队列饱和验收；没有目标 endpoint、云边缘配置或测试凭据，不能据 CI 宣称公网环境已安全。
- 尚未开始：P1 API Key 生命周期/Worker mTLS；P2 Attempt 凭据与网络隔离。
- 当前验证证据：静态代码/配置审查。尚无公网安全组/WAF 状态、真实 Worker 客户端证书、逃逸负向测试或外部端口核验结果。
