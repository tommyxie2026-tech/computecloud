# 公网访问安全加固跟踪

本台账跟踪 [公网访问安全加固设计](../superpowers/specs/2026-10-12-public-access-security-design.md) 的完整落地。入口端口由部署拓扑决定；当前示例采用 HTTPS 443 与 gRPC/TLS 透传 7443，应用上游仅绑定 loopback。“完成”要求代码、CI 和目标环境证据分别满足对应门槛。

## 总体状态

| 阶段 | 状态 | 计划 | 退出条件 |
| --- | --- | --- | --- |
| P0 公网入口与资源限制 | 代码与 CI 验证完成；目标边缘待验收 | P0 实施计划 | 认证主体速率/并发/成本限额由代码和 CI 覆盖；目标公网入口、源站保护、429/过载行为需在部署环境核验 |
| P1 API Key 生命周期与 Worker mTLS | 代码与 CI 验证完成；证书演练待验收 | P1 实施计划 | key 创建/撤销/过期由测试覆盖；逐台 Worker 证书轮换、撤销和回退需有目标环境证据 |
| P2 Attempt 凭据与网络隔离 | strict OCI/Codex/Claude Gateway 代码与 CI 验证完成；目标隔离待验收 | [Worker 部署加固](../superpowers/plans/worker-deployment-hardening.md)、[Claude Attempt Gateway](../superpowers/plans/claude-attempt-gateway.md) | Attempt 凭据限时限域、固定 CLI 协议和预算边界由代码/CI 覆盖；真实 OCI 逃逸、出站拒绝、供应商账单硬上限及目标主机验收仍待完成 |

### 当前安全结论

- v0.4.8 已发布的入口限流、身份配额、API key 生命周期、Worker mTLS 与 strict OCI/Codex Gateway 不等于目标环境公网安全验收。
- PR #150 的 Worker 部署加固和 Claude Attempt Gateway 已合入主线，主线 CI 通过；仍未证明真实供应商、目标 Worker 的文件/进程/网络隔离、证书轮换撤销或账单硬上限。
- 当前证据不能支持接纳任意不可信公网租户；应维持受信用户/受控租户范围，直到对应目标环境 Gate 有证据。

## 跟踪规则

- 依次推进 P0 → P1 → P2；每阶段单独 PR 和验证记录。修复发现的阻塞后再进入下一阶段。
- 每次更新阶段状态时，记录 PR/commit、CI 结果、目标镜像 digest、平台和未完成验收项；不能用 CI fixture 替代公网边缘或真实 OCI 主机验证。
- 仅在所有阶段完成并有目标环境证据后标记“可面向不可信公网租户”；此前的部署范围保持受信任用户/受控租户。
- 任何凭据泄露或 mTLS 撤销演练失败都暂停扩大流量，先吊销相关凭据并复核审计。

## 历史实施记录

以下条目保留实现过程的提交与 CI 证据，仅作历史追溯；如与上方“当前安全结论”或状态表冲突，以当前摘要为准。

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
- 已实现（待 CI/目标环境验收）：strict `isolated_only` 下每个 Codex HTTP Attempt 必须配置 `max_attempt_tokens`、`max_attempt_cost_microusd` 与输入/输出每百万 token 单价；Gateway 依据已结算 usage 累计 token 与估算费用、请求串行化、未知用量 fail closed，并按两类余额夹紧 `max_output_tokens`。输入估算按请求体字节数保守预留，费用每次调用向上取整；compact endpoint 不支持。每个新 Attempt（包括重试）有独立额度。
- 尚未完成：供应商实际账单硬上限。Gateway 估算依赖管理员价格配置和供应商 usage，不能约束供应商账单、定价变化或单次在途请求超额；不可信公网模型调用仍须配置可验证的上游支出硬预算，或暂限可信租户。Claude Gateway/token 预算仍 fail closed。
- 已提交：strict Attempt token 预算 `d98bce1`；token 与估算费用双预算 `3778928`。本地全量 `go test ./...`、`go vet ./...`、新增预算路径 race 测试、17 项 Python 安全/边缘策略测试、策略样例检查、Python compile 与 `git diff --check` 通过。全量 Server race 曾有既有端到端用例 `TestGatewayWorkerInvocationAndSecretRedaction` 报 `ARTIFACT_ERROR`；该用例普通复测通过，新增预算路径 race 测试通过。
- GitHub：PR [#144](https://github.com/tommyxie2026-tech/computecloud/pull/144) 已合并（状态已复核）；合并前 head `1bb2321` 的 CI run [38202403273](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38202403273) 全部通过（30/30 jobs）。前序 run 暴露 retry-flow 与 control-client-e2e 在快速 CI runner 上触发 HTTP 429；提交 `1bb2321` 为两套临时测试 Server 配置独立设置高限额。结合 `0ac6287`、`cffaa13` 对 task-flow/容量探针夹具的隔离及 `c28a2f4` 的 schema 期望更新，CI 夹具不再依赖生产默认限流或旧 schema；生产默认限流未放宽。
- 最新安全修复提交 `3dad8dd` 的全量 race job 曾暴露已有 Relay 压力测试 `TestRelayControlProgressDuringBulkTransfer` 未到达 Bulk handler；普通全量 Go tests 已通过。提交 `7a728a0` 为该用例增加请求/写入提前结束的即时错误报告；本机单独 `-race -count=10` 通过，后续完整 PR CI run [38205898936](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38205898936) 在 head `7a728a0` 全部通过（30/30 jobs，含 Linux/macOS verify）。
- 安全复审发现 strict OCI 配置曾允许内置 Docker `host` 网络，使 Attempt 共享 Worker 主机网络。strict 模式现拒绝 `bridge`、`host`、`none`，并在启动时解析 Docker 实际网络名，拒绝用内置网络 ID 冒充命名网络；新增回归用例复现并覆盖了该问题，PR CI run [38206576950](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38206576950) 全部通过（30/30 jobs，含 Linux/macOS verify）。此配置检查不能替代 Worker 主机的实际 egress/firewall 负向测试。
- 继续复审发现 Worker 宿主的代理变量及 `SSL_CERT_FILE`/`SSL_CERT_DIR` 会继承进 Attempt 容器，即使 strict mode 的用户自定义 Runtime 环境为空也可能绕过出站策略或改变 TLS 信任根。strict mode 现清除这些宿主覆盖并增加保留 trusted mode 既有代理行为的测试；Linux/macOS 完整 race/flow 验证已由上述 CI run 通过，本机完整 Worker 包测试仍受沙箱 `operation not permitted` 限制。
- 安全工具链检查发现 CI 缺少 Go 漏洞调用链扫描。已添加固定 `govulncheck v1.1.4` 的 `make security-vuln` 目标，并设为现有 CI `make vet` 的前置项，因此 Linux/macOS verify 及依赖 verify 的发布打包会被扫描结果门控。PR CI run [38209428235](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38209428235) 首次扫描发现 Go 1.27.1 的 11 条可达标准库漏洞链及 `golang.org/x/net v0.57.0` 可达漏洞链；Go 官方将 `1.27.2` 列为对应标准库安全修复版本，`x/net` 报告列出的修复版本为 `v0.60.0`。升级到 `x/net v0.60.0` 后，第二次扫描因 Go 模块图需要 `go mod tidy` 更新而未进入漏洞分析；已补齐 `x/net` 新版声明的间接模块并重跑 CI。本机无法访问 `proxy.golang.org`，修复是否完整须以升级后扫描为准。
- 尚待验证：固定 Codex CLI/真实上游的协议与用量合同，以及公网安全组/WAF、真实 Worker 客户端证书轮换/撤销、逃逸负向测试和外部端口核验。目标部署现有双 Worker 在线与分别成功执行记录，但这些不替代上述安全验收。
- 安全扫描闭环：PR head `c4e4849` 的 CI run [38210717417](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38210717417) 全部 30/30 jobs 通过，包含 Linux/macOS 全量验证；`go mod tidy -diff` 通过，`govulncheck v1.1.4` 输出 `No vulnerabilities found.`。模块图根因已按 Go 1.27 tidy 结果修正：`x/net v0.60.0` 为直接依赖，`x/sync` 更新至 `v0.23.0`，并刷新 `x/mod`/`x/tools` 校验和。该结果覆盖 Go 可达依赖漏洞扫描，不替代公网边缘、真实 Worker 主机、凭据生命周期及上游账单硬限额验收。

- 最新验证更新：新增 Attempt token 三项拒绝回归（模型不匹配、路由 digest 改变、租约过期，均断言请求未触达上游）。CI 初跑发现 govulncheck v1.1.4 依赖的 x/tools v0.29.0 在 Go 1.27.2 语法分析时 panic；已将扫描工具固定升级至 v1.2.0（x/tools v0.44.0），不是跳过或软化扫描。PR head `f70fd1a` 的 run [38213020899](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38213020899) 30/30 jobs 成功，Linux 与 macOS 完整 verify 均通过；verify 日志确认 `govulncheck v1.2.0` 输出 `No vulnerabilities found.`，后续 `go vet`、全量测试和 race 测试通过。该证据只证明代码/依赖 CI 门禁，不覆盖尚未完成的公网入口、双 Worker 证书、真实 CLI/OCI 对抗及供应商账单硬限额验收。

- OCI 参数边界验证：扩展 `TestRunLifecycleIdempotencyAndRestartFence`，以恶意 Runtime argv 注入 `--privileged`、`--network=host`、任意宿主 bind mount 和 Docker socket mount 字符串；断言这些值始终位于镜像之后，只作为 Runtime 参数，且 Docker 镜像前的固定隔离参数不包含提权/额外挂载选项。PR head `8573673` 的 CI run [38215422886](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38215422886) 30/30 jobs 全部通过，含 Linux/macOS 完整 verify。此为参数构造回归测试，不替代在目标 Docker/OCI 主机上尝试文件系统/网络逃逸和出站拒绝的实测。

- Agent Runtime 本地 Unix HTTP 服务 header 上限：该监听器已设置 `ReadHeaderTimeout` 和 4 MiB body cap，之前未显式限制 header 总字节。现配置 `MaxHeaderBytes=16 KiB` 并加入配置回归测试。PR head `e88ec2d` 的 CI run [38217113343](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38217113343) 30/30 jobs 成功，Linux 全量 verify 包含 `govulncheck`、vet、全量测试、race、smoke 与 capacity checks；该本地 Unix socket 端点的限制不替代公网 Server 入口及 Worker 主机隔离验收。

- 复审发现未知 `cc_live_` Bearer 值可在认证后限流之前触发 SQLite key 查询。已增加独立的直连来源 IP 有界限流，在普通 HTTPS、模型 Gateway、Agent conversation 入口的 resolver 之前拒绝过量查询；转发头不参与来源键。目标 NAT/代理后的合法客户端共享来源预算，边缘层仍需限流。回归测试覆盖 token 轮换与伪造 `X-Forwarded-For`；PR head `57e75ab` 的 CI run [38219776567](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38219776567) 30/30 jobs 成功，含 Linux/macOS 完整 verify。

- HTTP/gRPC 认证前 key 查找防护：Server unary/stream 拦截器和 HTTPS/Gateway/Agent conversation 入口复用按直连 peer IP 建立的有界预算；未知 `cc_live_` 不会在限流前触发 resolver，超限返回 HTTP 429 或 gRPC `ResourceExhausted`/`retry-after` trailer。HTTP unary/gRPC unary 与 streaming resolver 调用计数回归和本机 race 测试通过。PR head `296adb5` 的 CI run [38223430583](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38223430583) 30/30 jobs 成功，含 Linux/macOS 完整 verify。

- 来源限流器压力路径优化：复审发现每次请求都遍历来源桶清理过期项，容量满时还会排序全部条目淘汰最旧项。提交 `8418d0b` 改为 map + LRU 链表；单次请求过期清理最多处理 64 项，容量淘汰为 O(1)，并新增有界清理和最近使用顺序回归测试。相关普通与 race 测试通过。PR head `8418d0b` 的 CI run [38252321597](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38252321597) 44 个 job 中 34 成功、10 个按 PR 条件跳过；Linux/macOS 完整 verify 成功。该修复降低高基数来源流量下限流器自身的 CPU 放大风险，但不代表公网压力/DoS 验收通过。

- PR [#144](https://github.com/tommyxie2026-tech/computecloud/pull/144) 已合并；安全发布门禁仍包括真实公网端口与源站绕过核验、边缘 WAF/安全组实配、Worker A/B mTLS 轮换及撤销演练、目标 OCI 文件/进程隔离和出站网络负向测试，以及供应商可验证的账单硬上限。上述项目须绑定同一候选 SHA 和镜像摘要的目标环境证据；CI 不能替代这些证据。

- 目标环境验收证据现在要求填写安全格式的 `worker_id`，并在每台 Worker 的独立报告中记录，降低 A/B 结果混用风险；validator 明确只校验字段格式，不证明目标主机身份。提交 `ea39aca` 的缺失字段和路径注入拒绝测试、23 项 Python 验收工具测试、边缘策略样例检查及脚本编译通过。PR head `ea39aca` 的 CI run [38255474804](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38255474804) 34 个 job 成功、10 个按条件跳过，Linux/macOS 全量 verify 均成功。

- Claude Attempt Gateway 本地实现更新：提交 `ccff51a` 加入独立 Anthropic Messages 入口、Server 端 Key 替换、SSE/usage/budget fail-closed 结算；提交 `d2e09fa` 将 `claude_http` 固定版本能力接入 Job Schema、Server 调度、Worker 派发前复核与 Attempt Token 环境注入。配置与客户端文档只含占位值。adapter/config/agenthttp/job 测试、Worker（跳过需要进程信号权限的恢复用例）、Anthropic Gateway 定向测试和 Gateway race 测试通过；本机 loopback 合约脚本确认请求路径、Bearer auth、版本头、Beta Header 与请求字段。固定镜像 CI 契约检测到 CLI 新增的 mid-conversation-system Beta Header 未列入 allowlist；已补充 Gateway 白名单、回归测试和合约断言，重跑结果待 CI。真实供应商、目标 Worker、出站隔离和账单硬限额仍未验收，不能据此扩大公网租户。

- 双 Worker 目标部署复核：受控部署中的 Worker A 与 Worker B 均在线，使用同一已发布 Worker 镜像摘要；Server 任务记录中两台 Worker 均有成功完成的任务。因此可确认该部署基线的跨主机注册与任务执行验证通过。本轮为只读状态与历史记录复核，没有重新提交任务；不据此宣称负载均衡公平、证书轮换/撤销、主机凭据/文件隔离、出站拒绝、双机升级/恢复或本分支 Claude Attempt Gateway 已通过。文档不记录真实主机名、域名、IP、路径、任务 ID、提示内容、用量或密钥。
