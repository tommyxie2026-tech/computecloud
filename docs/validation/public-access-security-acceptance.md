# 公网 Agent Job 安全验收手册

本手册用于在候选版本开放不可信公网任务前，按顺序验证公网入口、API key、Worker mTLS、Attempt OCI 隔离、出站网络和凭据/预算边界。适用范围是 Agent-aware Distributed Job Execution Platform / Agent Job Executor。

**CI 通过不等于公网生产验收通过。** 每条证据必须来自实际目标环境，并绑定同一个候选 Git SHA、Server/Worker/Agent Runtime 镜像 digest 和平台。任一必需项失败或无法验证时，不开放不可信公网租户。

## 验收前准备

1. 确认使用同一个候选构建的 Server、Worker 和 Agent Runtime 镜像，记录完整 Git SHA、每个镜像的 `@sha256:...` digest 及平台（`linux/amd64` 或 `linux/arm64`）。不要使用可变标签。
2. 选择隔离的 staging 项目和测试用户。准备短期、最小 scope 的测试 API key，并通过权限为 0600 的文件提供给客户端；不要把 key 放进命令行、shell history、CI 输出或报告。
3. 准备两台独立 Linux Worker 或实际生产 Worker。给每台 Worker 使用独立的 mTLS 证书和 Worker ID；用专门的测试 CA/测试 key 做轮换、撤销，不能复制 Server 私钥到 Worker。
4. 在目标网络中准备运营方控制的 egress Docker network、明确允许的模型 Gateway 地址、metadata/host-gateway/private 的专用拒绝探测地址，以及一个可控的未批准公网 HTTPS 地址。拒绝探测必须只访问这些明确配置的地址，不扫描网段。
5. 配置模型供应商账户/组织可验证的硬支出上限与告警；保留控制台或供应商 API 的脱敏证据。Runtime 按 usage 估算的费用上限不能替代供应商账单硬限额。
6. 先运行仓库策略样例检查和 Worker controller 配置检查：

```sh
python3 scripts/check_public_edge_config.py
python3 scripts/security/validate_worker_runtime_config.py --container <agent-runtime-controller-container>
```

本地策略样例检查不证明云安全组/WAF 的真实状态。Controller 检查输出通过也不证明 Attempt 容器实际上无法逃逸，后续负向测试仍是必需项。

## 验收顺序

### 1. 公网入口、TLS 和限流

从不在 Server/Worker 所在网络的外部主机执行端口/TLS检查，并从公网尝试直接访问源站 IP。预期仅有规定的公开服务入口；管理、指标、数据库和 Docker API 不对互联网开放。源站只接受 LB/WAF 地址，直接源站请求必须失败。

在专用 staging 服务身份上运行有限的 HTTP smoke 检查：

```sh
python3 scripts/security/public_ingress_smoke.py \
  --base-url https://<public-host> \
  --token-file <mode-0600-test-token-file>
```

预期未认证请求被拒绝、超大请求返回 413、限流在有界探测次数内返回 429。该脚本的可选 `--queue-job-file` 会真实提交一次 Job；只有 staging 环境且明确接受创建并清理该 Job 时才同时加 `--confirm-queue-test`。不要在生产环境用提交 Job 的方式压满队列。

验证 TLS 证书链/主机名、7443 gRPC TLS 端到端行为、7444 HTTPS Gateway 的 Authorization 转发与缓存禁用。保存外部探测工具名称/版本、时间、目标域名、实际开放端口及脱敏结果；不要将 bearer header 放入报告。

### 2. API key 与 Worker mTLS 生命周期

以最小权限创建测试 key，验证其项目和 scope 限制；确认过期、撤销后立即拒绝，已撤销 key 不会因数据库/配置恢复而重新激活。

对 Worker A、Worker B 分别执行证书签发、注册、连接、轮换、旧指纹撤销和回退演练。逐项记录 Worker ID、证书 URI SAN、证书 SHA-256 指纹、有效期、动作时间和结果；不记录证书私钥、key 内容或完整 bearer token。确认错误 SAN、撤销指纹、无证书客户端均按策略失败。

若真实 CA、私钥分发和撤销机制不能在目标部署上验证，此项仍未通过；CI 中生成的临时证书不能代替双机演练。

### 3. Attempt 身份、凭据和限额

在 `public_execution_mode: isolated_only` 下提交一次受支持的 Codex HTTP Attempt。确认调度只选择声明 container 能力且配置 Attempt Gateway 的 Worker；Worker 在准备工作区前再次拒绝 process execution、不安全 credential mode 和非授权 runtime。

检查 Attempt 可读文件和挂载：没有 Worker API token、Worker mTLS 私钥、个人 `~/.codex`/`~/.claude` 状态、Docker socket、Server TLS 私钥或其他 Attempt 工作区。验证 Attempt Gateway 只接受当前 Attempt/lease、路由摘要和允许模型；取消/释放/过期 lease 后重放必须失败。

运行一次预算边界测试：请求达到 token 或估算费用上限后后续调用拒绝；缺少或无法结算 usage 时 fail closed。记录 Gateway 返回类别、Attempt ID 和用量汇总，不保留提示词、请求正文、Authorization header 或原始模型响应。另行核对上游供应商硬支出上限已启用。

Claude CLI 当前虽有本地 mock 证明其可使用自定义 Anthropic endpoint，但当前 Server Gateway 并未提供 Anthropic Messages 协议适配，因此 strict 模式应拒绝该路径；不得将 CLI mock 当成生产支持证据。

### 4. OCI 文件与进程隔离

绑定唯一 Attempt ID，在真实候选镜像和目标 OCI runtime 上执行以下负向测试：

- 从容器读取/写入 Attempt workspace 之外的专用 sentinel 文件，必须被拒绝，宿主 sentinel 内容与权限不变。
- 尝试以 task 参数请求宿主 process、额外 bind、Docker socket、host network、privileged、Linux capability、device、镜像覆盖或宿主路径 traversal，必须被拒绝；不得静默回退到宿主 CLI。
- 验证容器为非 root、只读 rootfs、no-new-privileges、drop capabilities、seccomp 和 CPU/memory/PID 限额实际生效。
- 取消、完成、超时、Worker 重启恢复后检查 Attempt 的临时凭据文件和容器都已清理；释放幂等，旧 generation 不得重放。

保留只含镜像 digest、平台、Attempt ID、检查结果和脱敏错误分类的证据。无法在目标 OCI runtime 上实施文件/进程负向测试时，该门禁不通过。

### 5. 出站网络拒绝测试

在实际 Worker 上确认 controller 使用 operator 创建的 egress 网络及预期固定镜像：

```sh
python3 scripts/security/worker_egress_acceptance.py \
  --network <operator-egress-network> \
  --image <agent-runtime-image>@sha256:<64-hex-digest> \
  --platform linux/amd64 \
  --worker-config-digest sha256:<64-hex-config-digest> \
  --attempt-id <sanitized-attempt-id> \
  --allowed-model-url https://<approved-gateway>/v1/ \
  --metadata-url http://<operator-metadata-test-address>/ \
  --host-gateway-url http://<worker-host-gateway-test-address>/ \
  --private-test-url http://<operator-private-test-address>/ \
  --unapproved-public-url https://<operator-unapproved-public-test-host>/ \
  --output dist/public-security/worker-egress.json
```

每台 Worker 和每个平台都要独立运行，并使用独立 Attempt ID。`--worker-config-digest` 应为已审核、脱敏部署配置清单的 SHA-256；不要把密钥文件纳入该清单。若 `docker` 命令不在 PATH 中，可显式传 `--docker <docker-cli-path>`。脚本会启动仅用于网络探测的容器：批准模型 Gateway 端点必须可达；metadata、Worker host gateway、专用私网目标和未批准公网目标都必须在网络层不可达。单纯 HTTP 401/403 仍表示 TCP/HTTP 可达，不符合“网络拒绝”预期。脚本报告只含目标标签、HTTP 状态、digest、平台和探测结果，不含 URL/token；它只记录目标机网络探测，不代表静态容器配置已通过。

此测试探测的是指定网络上的临时 probe container，不能单独证明生产 Attempt 所有 DNS、IPv6、代理、重定向和主机防火墙路径都受控。结合 OCI 运行时实际 Attempt 检查、宿主出站防火墙规则和网络侧日志审阅。任何被拒绝的目标意外可达都应停止放量并修复网络策略。

### 6. 汇总证据与放量决策

将目标环境观察汇总为 `dist/public-security/evidence.json`。最小结构如下；所有布尔值须来自实际执行记录，不可由 CI fixture 或配置声明自动填充：

```json
{
  "candidate_sha": "<40或64位完整SHA>",
  "server_image_digest": "sha256:<64位digest>",
  "worker_image_digest": "sha256:<64位digest>",
  "agent_runtime_image_digest": "sha256:<64位digest>",
  "worker_config_digest": "sha256:<64位脱敏配置清单摘要>",
  "platform": "linux/amd64",
  "worker_id": "<stable-worker-id>",
  "attempt_id": "<sanitized-attempt-id>",
  "secrets_included": false,
  "static_config_checks": {
    "worker_container_config_passed": true,
    "runtime_controller_config_passed": true
  },
  "target_host_checks": {
    "target_host_verified": true,
    "container_isolation": true,
    "host_cli_fallback_denied": true,
    "workspace_escape_denied": true,
    "metadata_egress_denied": true,
    "host_gateway_egress_denied": true,
    "private_egress_denied": true,
    "unapproved_public_egress_denied": true,
    "attempt_gateway_allowed": true,
    "cleanup_confirmed": true,
    "revocation_confirmed": true,
    "origin_bypass_denied": true
  }
}
```

按每台 Worker、每个平台和候选部署分别保留证据。`static_config_checks` 来自 Docker inspect validator；`target_host_checks` 必须来自对应目标主机上的实际负向测试和网络探测。静态检查通过不能填充或推断任何目标机测试结果。`target_host_verified` 是证据审阅者对测试确实来自目标机的声明，结构 validator 不能证明其真实性。必填 Worker ID 使用部署配置中的稳定 ID，避免跨 Worker 复用证据。不要将私钥、API key、证书内容、Authorization 值、提示词、原始模型响应、完整请求 URL 或个人配置写入 JSON/工单。

运行结构门禁：

```sh
python3 scripts/security/validate_public_security_evidence.py \
  dist/public-security/evidence.json
```

此门禁检查报告结构、digest/SHA 格式、必需结果及明显凭据形态；它不验证结果真实性，也不接受取代原始目标环境证据。审阅者还须验证供应商账单硬上限、外部端口/源站直连、Worker A/B 证书撤销，以及两个 Worker 上的 OCI/egress 负向测试。

全部门禁通过后，先仅对低配额 canary 开放，观察限流、Attempt 清理、凭据撤销和供应商账单告警。任一必需项失败时停止不可信任务准入，保留已撤销 key/证书的撤销状态；只允许在网络隔离的可信租户模式恢复服务。更新安全验收台账并由发布责任人审核证据后，才可移除草稿状态并进入发布流程。
