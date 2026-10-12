# 公网访问安全加固跟踪

本台账跟踪 [公网访问安全加固设计](../superpowers/specs/2026-10-12-public-access-security-design.md) 的完整落地。公网服务仍保留 7443/gRPC 与 7444/HTTPS；“完成”要求代码、CI 和目标环境证据分别满足对应门槛。

## 总体状态

| 阶段 | 状态 | 计划 | 退出条件 |
| --- | --- | --- | --- |
| P0 公网入口与资源限制 | 进行中 | [实施计划](../superpowers/plans/2026-10-12-public-ingress-resource-controls.md) | 认证主体速率/并发/成本限额生效；公网入口拓扑、源站保护、429/过载行为通过验收 |
| P1 API Key 生命周期与 Worker mTLS | 本地实现完成，待目标环境验收 | [实施计划](../superpowers/plans/2026-10-12-api-keys-and-worker-mtls.md) | key 创建/撤销/过期有效；A/B 独立证书验证、逐台轮换、撤销和回退完成 |
| P2 Attempt 凭据与网络隔离 | 进行中 | [实施计划](../superpowers/plans/2026-10-12-attempt-credential-and-egress-isolation.md) | 公网严格模式拒绝 process；Attempt 凭据限时限域；目标 Worker 逃逸/出站负向测试通过；Attempt token 上限代码完成；美元成本上限与目标环境验收仍待完成 |

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
- 已完成：P0 Task 4 公网边缘策略清单、部署约束文档和自动检查，commit `3adb3d3`。
- 已完成：P0 Task 5 安全 smoke 工具与 mock 用例，commit `4763b84`。
- 已完成：P1 API Key 元数据、管理 API、离线初始化、安全文件权限、Worker mTLS 身份校验和证书指纹轮换代码，commit `fb6e059`、`f5709fc`、`cfb5403`、`efddd71`。
- 已完成：P2 第一段严格执行准入，commit `44ce52b`（Server scheduler capability fence + Worker 工作区前复验，只接受 container + Attempt Gateway capable Runtime）。
- 已完成：P2 OCI 控制器安全子项，commit `17f0a5e`（私有状态目录中的 Attempt env 文件、0600 权限、启动时 stale 文件清理；strict mode named egress network/seccomp + CPU/内存/PID 限额）及 egress 验收工具初版。
- 已完成：Codex HTTP Attempt Gateway 接入初版，让 OCI Runtime 只接收 Attempt token；Claude 保持 strict mode fail closed。Adapter/Server/Worker/OCI 相关测试通过；仍待 pinned CLI 版本/真实 Gateway 合约验收。
- 已完成：Worker Runtime Controller Docker-inspect 校验和明示目标的 egress 验收脚本及 mock 测试，commit `09b97a0`；真实 Worker 主机和网络防火墙验收未跑。
- 已完成：P2 公网租户准入/回退证据清单与统一 evidence JSON validator 初版，commit `4e1ae6e`；validator 需要目标主机实测数据才能通过。
- 验证：`go test ./...`、`go test -race ./internal/server ./internal/rpcutil`、9 个 Python 边缘策略/smoke 单测、`py_compile` 与 `git diff --check` 均通过。尚无 GitHub CI 运行记录。
- 尚未完成：目标环境公网端口扫描、源站绕过拒绝、WAF/安全组配置、真实 429/队列饱和验收；没有目标 endpoint、云边缘配置或测试凭据，不能据 CI 宣称公网环境已安全。
- 尚未完成：P1 双机证书签发、A/B 证书轮换/撤销实测及目标环境验证。
- 尚未完成：P2 pinned Codex CLI 认证协议实测、Claude Gateway 支持（当前 fail closed）、OCI 逃逸负向测试、目标 Worker 出站负向验收。
- 已实现（待 CI/目标环境验收）：strict `isolated_only` 下每个 Codex HTTP Attempt 必须配置 `max_attempt_tokens`；Gateway 依据已结算 usage 累计、请求串行化、未知用量 fail closed，并夹紧 `max_output_tokens`。保守按请求字节数预留输入 token；compact endpoint 不支持。该上限以 Attempt 为单位，重试的新 Attempt 有独立额度。
- 尚未完成：美元成本硬上限。token 上限不能约束供应商计价或账单；不可信公网模型调用仍须配置可验证的上游支出硬预算，或暂限可信租户。Claude Gateway/token 预算仍 fail closed。
- 已提交：strict Attempt token cap 与文档，commit `1c7e7e0`。本地 `go test ./...`、预算路径 race 测试、17 项 Python 安全/边缘策略测试、策略样例检查、Python compile 与 `git diff --check` 通过。全量 Server race 首次运行中一个既有端到端用例 `TestGatewayWorkerInvocationAndSecretRedaction` 因 `ARTIFACT_ERROR` 失败；普通复测通过，新增预算路径 race 测试通过。
- 尚待验证：GitHub CI、锁定 Codex CLI/真实上游的协议与用量合同，以及公网安全组/WAF、真实 Worker 客户端证书、逃逸负向测试和外部端口核验。当前无目标环境证据。
