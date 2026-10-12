# 公网访问安全加固设计

## 目标与背景

computecloud 的 Server 需要从互联网提供服务。安全设计不以隐藏端口为目标，而是确保公网请求具有可识别身份、最小权限、资源边界和可撤销凭据，并降低任务执行环境泄露 Worker/模型凭据或影响宿主机的风险。

当前服务边界为 gRPC/TLS `7443` 和 HTTPS `7444`。现有身份依赖配置文件中的静态 Bearer Token；生产协议当前未启用客户端证书认证。Worker 主动连接 Server，不需要平台入站端口。Attempt OCI 执行器已有非 root、只读 rootfs、丢弃 Linux capabilities、禁止提权等限制，但 v0.4.7 生产容器逃逸、网络隔离和真实 Worker 验收尚未完成。当前本机部署样例还将宿主 Codex 配置挂载给 Worker，不能视为公网不可信任务的安全凭据模型。

## 威胁模型

需要防护的主体和风险：

- 未认证互联网客户端扫描、猜测凭据、消耗连接/CPU/内存/数据库/模型额度，或利用 API 错误配置越权访问 Job、Artifact、Goal 和控制操作。
- 已认证但恶意或凭据被盗的用户，在其项目边界外读写资源，或通过提交/重试/模型调用造成超额消耗。
- 被攻陷、被复制或已退役的 Worker 冒充其他 Worker，继续访问 Server 或接收任务。
- 恶意 Prompt、仓库内容或工具输出诱导 Agent 读取并外传长期模型凭据、Worker 凭据、其他任务文件或宿主资源。
- 公网入口、代理、Docker 控制面、日志、备份或部署流水线导致凭据泄露。

信任假设：公网客户端和任务输入均不可信；Worker 主机、Server 主机和密钥签发/存储系统是不同的信任域；TLS 私钥、Worker 客户端私钥和模型凭据不得相互复用。

## 设计决策

### 1. 双入口和网络边界

7444 继续承载用户 HTTPS API。公网 DNS 指向具备 DDoS 防护、HTTP WAF 和 TLS 管理能力的边缘/API Gateway；Gateway 到 Server 使用 TLS。边缘层负责通用连接和 HTTP 请求防护，Server 仍负责每个请求的认证、授权和资源归属校验。代理只信任明确配置的 Gateway 地址写入的转发头；直连到源站的流量应由网络策略拒绝，避免客户端伪造来源 IP。

7443 继续承载 Worker gRPC，可公网路由，但只接受带有效 Worker 客户端证书的连接。前置负载均衡采用 TCP/TLS passthrough，或使用可验证并安全传递客户端证书身份的受信代理；不得仅凭代理注入的普通 HTTP header 认定 Worker 身份。应用层仍执行 Worker 身份、项目和 RPC 权限检查。Worker 不开放入站服务端口。

保留主机防火墙/云安全组作为纵深防御；它们可按 Worker 固定出口 IP 收紧，但不能作为唯一身份认证机制。Server 的数据目录、数据库、Worker 端口、Docker daemon、metrics/debug/pprof 和管理接口不通过公网 Gateway 暴露。

### 2. 用户/API 身份和授权

当前配置文件静态用户 Token 迁移为 Server 管理的 API Key 生命周期模型，以兼容 CLI、Claude/Codex 自定义 API 客户端和服务调用，不要求每个客户端先完成浏览器登录。每个 key 至少绑定：主体、可访问项目、scope、创建时间、过期时间、状态、可选来源策略和配额策略。支持创建、只展示一次、撤销、轮换、过期清理和审计。存储只保留不可逆摘要与非敏感 key 前缀；明文只在创建时返回。

短期内 API Key 本身必须高熵、支持显式过期、单用户/单服务隔离，并能即时撤销；后续可增加 OIDC/OAuth2 登录或 key exchange，但不作为本阶段上线的前置依赖。Worker key 与用户 key 使用不同类型、不同校验路径，禁止互换。

每个 HTTP/gRPC/MCP/Artifact/Control/Goal 操作都由 Server 执行 endpoint scope、owner、project、resource 状态和操作授权；隐藏 UI 或 Gateway 路由不构成授权。默认拒绝，未知 scope、身份类型或资源归属 fail closed。错误响应不泄露 token 是否有效、其他租户资源是否存在等敏感信息。

### 3. Worker mTLS 身份生命周期

每台 Worker 具有唯一证书身份和唯一 Worker ID。Server 验证客户端证书链、有效期、用途、吊销状态及证书主体与配置 Worker ID 的绑定；然后再验证该 Worker 的应用身份和项目权限。Worker 私钥只读挂载到对应 Worker，不进入 Server、镜像、任务 Attempt、工作区或日志。

提供 CA/证书生命周期能力：签发、轮换、撤销、吊销传播、过期告警和 Worker 重新注册。丢失/离线 Worker 可被单独吊销，不需要更换其他 Worker 或用户凭据。轮换期允许短暂双证书，过期后拒绝连接。TLS server 证书维持主机名校验和自动续期。

### 4. 资源消耗和滥用控制

在 Gateway 和 Server 两层实现限制。Gateway 限制连接速率、HTTP 请求速率、头部/请求体、慢速连接和来源异常；Server 对认证主体实施细粒度额度，包括并发 Job、排队 Job、提交/重试速率、最大任务时长、Artifact 空间、模型并发、token/cost 上限和每日/每月预算。关键配额以主体/项目维度实施，不能只按 IP 限制，以适应 NAT 和分布式客户端。

7443 对连接数、并发 RPC、流持续时间、接收/发送消息、注册频率及每 Worker 可运行 Attempt 数设置上限。对超限响应使用可重试语义和 `Retry-After`（适用时）；在过载时优先拒绝新任务，不能无界积压。健康检查端点只返回必要信息，并由 Gateway 配置探活访问策略。

默认关闭匿名 API。若未来确需公开只读信息，应另设独立只读路由和明确的数据白名单，不复用带任务、Artifact 或模型能力的凭据。

### 5. Worker、Attempt 与凭据隔离

Worker 使用专用 OS 身份、专用 HOME 和专用模型账号，不挂载操作人员个人 Codex HOME。基础 Worker token、mTLS 私钥和模型凭据按需只读提供给 Worker；Attempt 容器不得获得 Worker token、mTLS 私钥、Server 配置、其他任务工作区或 Docker socket。

模型调用使用独立的凭据代理/受控 Gateway。每个 Attempt 只获得完成该任务所需的短期、窄权限凭据或由代理代为转发；凭据和上游响应中的密钥永不写入 Artifact、事件、错误日志或任务输出。若短期模型凭据暂不可用，单租户部署可使用独立专用账号和强隔离 Worker 作为过渡，但不得宣称支持不可信多租户。

生产 Attempt 使用专用 OCI/VM 环境：非 root、只读根文件系统、capability 全丢弃、no-new-privileges、seccomp/AppArmor/SELinux（目标系统支持时）、PID/CPU/内存/磁盘上限、每任务独立工作目录和清理确认。默认禁止访问宿主网关、云元数据、私网管理网段和任意横向网络；按产品需要对模型/API、Git、包源使用出站域名 allowlist。Docker daemon 只允许受信任控制器访问，优先使用独立 sandbox 主机或隔离运行时；不将 socket 暴露给公网/Worker Agent/Attempt。

### 6. 密钥、审计和运维

生产密钥来自 Secret Manager 或权限受限的挂载文件，不进入镜像层、源代码、shell 命令参数、compose 展开后的诊断输出和 CI artifact。文件和目录权限采用最小可读范围；Server TLS 私钥只在 Server 可读，Worker 只持有自己的客户端密钥与 Server CA。

安全审计记录主体 ID、凭据 ID/前缀、来源、目标资源、操作、决策、request/job/attempt ID、限额结果和时间，不记录 Authorization header、token 明文、模型密钥、完整 Prompt、敏感附件或未经脱敏的上游响应。日志有访问控制和保留期，安全事件产生可告警信号。

建立密钥泄露响应流程：禁用 key/证书、阻断相关 Worker、轮换模型凭据、检查审计和网络出口、保存必要证据、恢复服务。定期演练用户 key 撤销、Worker 证书吊销、TLS 续期和备份恢复。

## 分阶段交付

### Phase 0：公网安全基线（不破坏现有客户端）

- 发布经验证的 Gateway/防火墙拓扑和源站仅接受可信入口流量的配置。
- 7444、7443 的 TLS 配置、超时、体积和并发上限可观测；限制匿名流量和暴露的运维端点。
- 定义每主体/项目 quota 配置、审计事件和告警；补上公网入口滥用测试。
- 改进部署文档，明确当前个人 Codex 配置挂载只适合受信任本地测试。

### Phase 1：凭据生命周期和 Worker mTLS

- 引入有过期/撤销/轮换能力的用户 API Key 管理；迁移旧配置 Token 时支持双读过渡并给出迁移工具，最终可关闭静态模式。
- 引入 per-Worker mTLS 证书和吊销机制；为 TLS/应用身份不匹配、撤销、过期和轮换设计可测错误。
- 先在非生产 Worker 环境运行双认证，再按 Worker 滚动迁移；确认所有 Worker 切换后关闭旧 Worker token-only 模式。

### Phase 2：运行时凭据和沙箱闭环

- Worker 专用身份/配置；Attempt 不读取个人持久 Codex 登录态。
- 凭据代理或短时任务凭据；严禁凭据出现在事件、Artifact 和日志。
- 出站 allowlist、元数据阻断和网络隔离；strict Attempt 容器不得继承 Worker 宿主的代理变量或 `SSL_CERT_FILE`/`SSL_CERT_DIR` 信任根覆盖，避免代理绕过宿主出站策略或宿主环境改写 Attempt 的 TLS 信任边界。
- strict 模式启动时必须将配置的 egress network 解析为 Docker 网络名，拒绝 Docker 内置网络及网络 ID 别名。
- 运行真实主机上的文件/网络逃逸、控制器重启、清理和额度测试。
- 仅在 Phase 2 的目标环境验收完成后，才考虑接受不可信公网多租户任务。

各阶段应拆成独立 PR/实现计划，避免一个安全大 PR 同时改协议、数据库、部署和执行器。

## 验收标准

1. 公网扫描只发现预期的 7443/7444（以及明确公布的标准 HTTPS 入口）；metrics、debug、数据库和 Docker daemon 不可从公网访问；直连源站绕过 Gateway 被拒绝。
2. 7444 无凭据、过期、撤销、错误 scope、跨项目资源 ID 均失败；合法最小权限调用成功；HTTP/gRPC/MCP/Artifact/Control/Goal 路径覆盖授权回归测试。
3. 7443 无客户端证书、过期/撤销/错误 CA 证书或错误 Worker 绑定均失败；Worker A 证书无法冒充 Worker B；有效 Worker 仍可通过 TLS 连接。
4. 用户、项目、Worker 和模型额度超限可以稳定拒绝且不会继续产生新任务/上游费用；过载不会导致无界队列、连接或内存增长。
5. Canary 密钥测试证明 token、私钥、模型凭据不会出现在日志、事件、Artifact、镜像和 Attempt 可读目录；泄露演练可单独撤销一项凭据。
6. 真实目标 Worker 上文件逃逸、到拒绝网络/元数据地址的访问、控制器重启后的恢复和容器清理均有可复现记录；测试证据标明镜像 digest、commit、平台和测试结果。
7. 旧客户端与旧 Worker 的兼容窗口有截止日期；升级与回退过程不会意外恢复已撤销的凭据或重新启用弱认证模式。

## 不在本设计范围

- 改变 Agent Job Executor 的产品定位或引入通用 Workflow/Model Serving 能力。
- 在本规格阶段直接实现代码、替换现有认证协议或发布新版本。
- 宣称公网 DDoS 可完全由应用自身防护；上游 DDoS 清洗和云网络能力由部署环境提供。
- 将静态扫描或 CI 通过当作真实生产容器隔离、凭据保护和双机网络边界的替代证明。

## 风险与开放实现决策

- 用户 API Key 的持久化生命周期需要数据库 schema 迁移、单次展示 UX、管理员 bootstrap 和灾难恢复策略；实现计划需单独定义管理命令/API 与 break-glass 管理员流程。
- mTLS 证书吊销选择在线状态查询还是短期证书加快速轮换，需要根据公网 Worker 数量和 CA 能力确定；撤销生效目标应以分钟计并可演练。
- mTLS 可在 Server 进程终止，或在可信 TCP 代理终止并传递受验证身份。首选 Server 直接验证，若因托管 LB 限制终止于代理，必须在受保护链路内证明客户端身份且阻止绕过代理直连。
- 短期模型凭据代理需验证 Codex/Claude 具体 Runtime 的认证方式；不能支持的 Runtime 必须使用隔离的专用账号，不得悄然回退到宿主个人凭据。
- 公网 WAF 对 gRPC 的支持和限制能力因云供应商而异；7443 的 L4 DDoS 防护、连接限额和协议层限制需在所选供应商的实际环境验收。
