# computecloud v0.4.7 稳定版部署指南

本指南适用于 Agent-aware Distributed Job Execution Platform / Agent Job Executor。v0.4.7 已正式发布，归档、`SHA256SUMS`、`CONTAINER_IMAGES.txt` 和 GHCR 镜像 digest 可从 [v0.4.7 Release](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.7) 获取，并核对归档校验和、Release tag 与源代码 SHA。发布记录列有已核验的归档 SHA-256 和 Server/Worker 多架构镜像 digest；部署时建议固定 digest 而不是使用可变标签。

> 公网部署安全状态：本指南补充了边缘拓扑和服务端资源限制，但仓库没有云安全组、WAF 或真实源站网络状态的证据。不要把本仓库 CI 通过等同于目标环境安全验收；受控用户可按下方契约部署，面向不可信公网租户前必须完成外部验收。

## 版本边界

- Server schema：v18；从 v0.4.6 的 v13 升级时执行 v13→v18 迁移。Worker schema：v6。
- Worker 软评分默认关闭；只有显式启用 `prepared_workspace_score_enabled` 才参与排序。硬能力/环境资格过滤仍先执行。
- `direct` 仍为默认传输；`direct_then_relay` 是显式 opt-in。Goal 自动 Re-plan 保持关闭。
- 真实 Runtime、独立双机升级恢复、生产 Docker 隔离、代表性缓存和 NAT Relay 尚未完成生产验收。CI 成功不代表这些环境验证通过。

## 已发布产物校验

- Linux amd64 归档 SHA-256：`83e3077bfa2592d58a9cfc883b669d1f19dae083c608d65d0bef9a7a0f276176`
- Linux arm64 归档 SHA-256：`0e86f3a029410ded94958152bf833433960a728ab255de06a6e3ba364f605c32`
- Server multi-arch digest：`sha256:477488d772c5f1a442ae39fe59a391ef06d6f06409f22fae9bfa3a5e0040ee58`
- Worker multi-arch digest：`sha256:8cea4e5efacbcbf3fc814f475f3a3ac36d64cdf6f1758bf0dd5141ac209fd311`

完整 CI 与产物信息见[发布记录](../implementation/v0.4.7-release-status.md)。

## 从 v0.4.6 升级

1. 暂停新 Job，等待或取消在途 Attempt，记录版本、配置和凭据引用。不要跨 schema 混合版本滚动升级。
2. 停止 Server 与所有 Worker。在原数据目录之外创建离线备份，并为 Server/Worker 数据目录保留权限与 SHA-256。先在备份副本演练恢复。
3. 在隔离副本验证旧 Server `PRAGMA user_version=13`、`integrity_check=ok`，并确认备份可读取。
4. 使用已核验的 v0.4.7 Server 启动并完成 v18 迁移；确认健康后再启动 Worker，核验注册、能力与 v6 状态。
5. 验证 Job 提交/执行、取消、Artifact 读取、权限隔离、重启恢复和旧 generation 拒绝。未单独评估前保持 Worker 软评分关闭、Relay 使用 `direct`、Goal 自动 Re-plan 关闭。

## 回退

停止新投递和候选进程，从升级前的完整快照恢复 Server 与 Worker 数据，再恢复 v0.4.6 二进制和配置。确认 Server schema v13、Worker schema v6 与 SQLite 完整性后再启动。禁止手工降低 `PRAGMA user_version`，也不要让 v0.4.6 打开 v18 数据库。升级后写入的新数据不在旧快照内，需记录恢复窗口的数据损失。

## 发版后验收

独立 Linux Worker、真实 Runtime/MCP、容器逃逸负向测试、24 小时长任务、真实 NAT Relay、代表性缓存 P50/P95 与目标环境升级/恢复仍需按 [v0.4.7 稳定版台账](../implementation/v0.4.7-stable-tracker.md) 提交脱敏证据。它们保持待验收，不影响本次 CI 门禁放行。

## 公网入口安全契约

- 对互联网公开的入口仅为 TCP 7443（gRPC/TLS passthrough）和 HTTPS 7444（L7 Gateway/WAF）。不要公开数据库、指标、管理端口、容器工作目录或 Docker socket。
- 源站防火墙只允许边缘 LB/WAF 地址访问 7443/7444；Worker/用户访问公开 LB 地址。必须从外部确认无法直连源站绕过边缘规则。
- 7443 的 Server TLS 端到端保留；7444 的 Gateway 保留 Bearer `Authorization`、禁止缓存、限制体积/并发/速率。Server 负责身份、scope、project、Job 与模型配额。
- 认证用户默认限额为每 owner/project 8 个活跃 Job、32 个排队 Job、4 个模型请求并发；Worker 默认 128 个 session 和 stream。可在 Server YAML 的用户 `limits` 与顶层 `worker_admission` 中调整，多个用户身份共享 `owner` 必须配置一致限额。
- 超额响应 429 或 gRPC `ResourceExhausted`；调用方应退避，禁止无界重试。健康探测限于固定轻量路径，7443 不得使用 HTTP/明文探测。

策略清单检查：`python3 scripts/check_public_edge_config.py`。该检查只验证仓库策略样例，不能证明云侧实际规则；目标环境仍须记录公开端口扫描、源站绕过拒绝、TLS 和限额响应证据。

## 后续安全加固（未包含在 v0.4.7 发布产物中）

当前安全加固工作树包含 v19 API key 元数据、managed key 管理 API、Worker mTLS 与公网上限控制。它不是已发布 v0.4.7 的功能；不要把 v0.4.7 镜像与 v19 数据目录混用。v0.4.7 的 Server schema 仍为 v18，工作树升级 v18→v19 前要停止旧服务、排空 Attempt、做并验证备份。若工作树回退到 v0.4.7，必须恢复完整 v18 Server 数据快照和旧配置。

首次 key 通过离线 `computecloud keys bootstrap` 建立，秘密只写入权限为 0600 的新文件；管理 API 后续负责创建、列出和撤销子 key。创建 key 时仅可授予管理 key 已持有的项目、scope 与 credential，最长有效期 90 天。更完整的 key 生命周期命令、Worker A/B fingerprint 轮换、mTLS `optional`→`required` 迁移及回退步骤见[容器部署安全章节](container-images.md#api-key-生命周期与-worker-mtls-安全加固工作树)。

在真实 A/B 证书、Server 管理入口与目标主机配置到位前，P1 不可判定为生产验收完成。CI 用例验证证书链、URI SAN、fingerprint 撤销和用户无证书兼容性，但不能证明生产 CA 签发策略、证书私钥分发权限或真实轮换回退操作。

### 不可信公网任务的准入与回退门禁

P0/P1/P2 的工作树实现及回归测试不能替代目标环境证据。在双机 Linux/macOS Worker 或目标 Linux Worker 上开放不可信租户前，必须逐项记录：

| 验收证据 | 必需记录 | 放行条件 |
| --- | --- | --- |
| 代码与产物 | candidate SHA、Server/Worker/Agent Runtime 镜像 digest、平台 | 三者来自同一候选构建，镜像按 digest 部署 |
| 身份与密钥 | API key 创建/撤销结果，Worker ID、证书 SAN、SHA-256 指纹轮换记录 | 不含私钥/token；旧 key/指纹撤销后立即失效 |
| OCI 隔离 | Attempt ID、Workspace escape 负向结果、容器退出/清理结果 | 没有宿主进程回退；无宿主凭据文件、Docker socket 或其他 Attempt 工作区挂载 |
| 出站网络 | Attempt ID、实际镜像 digest、平台、模型端点可达；metadata、host gateway、私网测试点、未批准公网目标均不可达 | 每个拒绝目标都在网络层失败，结果 JSON 不含 URL 或 secret |
| 公网边缘 | 公网端口、TLS、WAF/限流、源站直连测试 | 只开放规定入口且无法绕过 LB/WAF |

strict execution 需要 Server 和每台 Worker 同时设置 `public_execution_mode: isolated_only`，并由 `codex_http`、container Environment 与 Attempt Gateway 路由组成受支持路径；Claude 等未验证 Gateway 认证的运行时会被拒绝。Agent Runtime controller 需用 `--public-strict`、operator 创建的 egress network、seccomp 文件和资源限额启动。先在隔离网络执行 `validate_worker_runtime_config.py` 与 `worker_egress_acceptance.py`，再审阅脱敏记录；不能只凭配置项或 CI 标记通过。

strict Server 必须为每个 `CredentialRoutes` 配置 HTTP 路由、`max_attempt_tokens`、`max_attempt_cost_microusd` 和输入/输出单价，且不能授予客户端 `models:invoke`。示例（替换 URL、模型和密钥文件路径）：

```yaml
server:
  public_execution_mode: isolated_only
  credentials:
    codex-account: 1
  model_gateway:
    enabled: true
    public_base_url: https://api.example.com/v1
    max_request_bytes: 1048576
    max_inflight: 8
    connect_timeout_seconds: 10
    header_timeout_seconds: 30
    stream_idle_timeout_seconds: 60
    request_timeout_seconds: 1800
    routes:
      codex-public:
        backend: http
        base_url: https://model-provider.example/v1
        api_key_file: /run/secrets/model-provider-key
        allowed_models: [REPLACE_WITH_CODEX_MODEL]
        max_inflight: 4
        max_attempt_tokens: 100000
        max_attempt_cost_microusd: 500000
        input_cost_per_million_tokens_microusd: 2500000
        output_cost_per_million_tokens_microusd: 10000000
    credential_routes:
      codex-account: codex-public
```

`max_attempt_tokens` 和 `max_attempt_cost_microusd` 按每个 Attempt 的上游用量累计。费用单位是微美元；输入/输出单价按每百万 token 的微美元填写。示例的额度为 $0.50，输入单价为 $2.50/百万 token，输出单价为 $10/百万 token。每条路由的单价必须不低于该路由所有允许模型的最高价格；Server 按请求逐项向上取整，并用请求体字节数保守估算输入 token，再依据 token 与费用剩余额度夹紧 `max_output_tokens`。请求并发串行化；上游用量未知或缺失时，该 Attempt 后续模型请求会 fail closed。只接受遵守 `max_output_tokens` 且能返回完整 token usage 的 OpenAI Responses 兼容上游；严格模式暂不支持无输出上限的 compact 端点。

**供应商账单限制仍是独立门禁：** Gateway 费用边界使用管理员配置的单价和供应商 usage，不等同供应商实际扣费。单价配置错误、供应商定价变化、一次在途请求、供应商不遵守 `max_output_tokens` 或账单调整均可能导致实际费用超过边界。面向不可信租户前，仍必须在上游模型账户/组织设置可验证的支出上限，并保留告警/停用证据；若无法配置上游硬限额，禁止开放自动付费模型凭据给不可信公网任务。

完成上述所有目标主机验证后，将真实观测写入统一 evidence JSON，并运行以下门禁。样例 fixture 和 CI 结果不能设置 `target_host: true`，也无法替代要求的各项网络负向结果：

```sh
python3 scripts/security/validate_public_security_evidence.py \
  dist/public-security/evidence.json
```

建议分阶段开放：先单租户可信测试并确认租户/项目隔离；启用 Worker mTLS 和 strict execution；对一个低配额 canary 租户验证正常任务、撤销、取消、预算和隔离失败；审核完整证据后再扩大流量。任一凭据撤销、网络拒绝、容器清理或 Attempt 绑定检查失败，立即停止新任务并暂停公网租户准入。

回退时先关闭新租户投递、排空或停止 Attempt，再按兼容的数据快照回滚 Server/Worker 镜像和数据库。任何已吊销 API key 或证书指纹不得随数据恢复而重新启用。只有可信租户被网络策略明确隔离时，才可临时使用 `trusted_only`；否则保持服务停止，直到 strict 路径恢复并重验。

本机或目标环境可运行安全 smoke 检查；必须显式传入 HTTPS endpoint 和保护好的测试 Token 文件，不会默认访问外网：

~~~sh
python3 scripts/security/public_ingress_smoke.py \
  --base-url https://api.example.com \
  --token-file ./secrets/test-user.token
~~~

若要检查队列饱和响应，先在隔离环境准备小型有效 Job JSON，并明确确认脚本会尝试提交一次真实 Job：

~~~sh
python3 scripts/security/public_ingress_smoke.py \
  --base-url https://staging.example.com \
  --token-file ./secrets/test-user.token \
  --queue-job-file ./fixtures/valid-job.json \
  --confirm-queue-test
~~~

脚本只输出脱敏的通过/失败状态；队列探测若返回已接受，表示目标队列没有饱和，且该探测 Job 已创建，应在隔离环境清理它。不要在生产环境主动填满队列。
