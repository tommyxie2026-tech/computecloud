# 公网生产访问安全审查（2026-10-12）

审查对象：v0.4.7 Server、Worker A/B、当前容器部署包与仓库部署文档。本文是基于静态配置/代码的架构审查，不等同于公网扫描、渗透测试或目标主机核验。

## 结论

当前本机部署包将端口绑定在 `127.0.0.1`，没有直接对公网开放。若按生产文档把 Server 发布到公网，主要入口为 gRPC `7443/TCP` 和 HTTPS `7444/TCP`；两个端口都应有防火墙/云安全组来源限制。Worker A/B 不发布入站端口，只向 Server 发起连接。不要把 Server 两个端口无差别开放给整个互联网，也不要把带有真实 Codex 凭据的 Worker 当作可安全承载任意不可信公网用户的多租户执行环境。

## 端口与数据路径

| 组件 | 端口 | 当前/文档行为 | 建议 |
| --- | --- | --- | --- |
| Server gRPC | 容器内 `7443/TCP`；本机 compose 映射宿主 `127.0.0.1:17443` | Worker 注册、任务分配、Attempt/Artifact 与 Task CLI；TLS + Bearer Token | 仅允许 Worker A/B 的 VPN/私网地址及确有需要的管理客户端 |
| Server HTTPS API | 容器内 `7444/TCP`；本机 compose 映射宿主 `127.0.0.1:17444` | Job/Control/MCP/Artifact；TLS + Bearer Token | 私网/VPN 或受信反向代理访问；如公网必须开放，启用 WAF/限速/审计和严格认证 |
| Worker A/B | 无平台入站端口 | 主动连接 Server `7443` | 防火墙拒绝 Worker 入站；仅放行必要出站目标 |

当前本机环境变量 `BIND_IP=127.0.0.1`、`GRPC_PORT=17443`、`HTTP_PORT=17444`，所以本机映射本身不是公网监听。生产文档示例则使用容器端口 7443/7444，并明确要求防火墙限制来源。

## 发现

### [高] Worker 凭据与任务输入处在同一信任边界

证据：当前 compose 将宿主 `CODEX_HOME` 挂到 Worker 的 Codex 配置目录，且该挂载不是只读（`container-deployment-20261010/compose.yaml:28-30,46-48`）。Worker 的凭据目录虽只读挂载，但只读并不能阻止进程读取其中的密钥。v0.4.7 Attempt OCI 执行器将任务环境变量通过 env-file 传给执行容器，并将 Attempt workspace 挂入容器（仓库 `internal/agenthttp/server.go:432-462`）。

影响：有权提交任务的用户可以影响 Agent 的输入；若任务内容、仓库文件或依赖诱导执行器泄露环境/配置，可能造成模型凭据或 Worker token 被滥用。即使 Attempt 容器有非 root、只读根文件系统、丢弃 capabilities、禁止提权等加固参数，Worker 自身配置和宿主 Docker 仍属于高价值边界。

处理：公网开放前先限定为可信租户；将每个 Worker 的模型凭据隔离为专用、可轮换且额度受限的凭据，避免复用个人 Codex 登录态；按需向 Attempt 注入短期凭据；限制 Worker 出站网络并阻断云元数据/内网管理面；完成真实主机上的文件逃逸、网络逃逸和容器清理测试。官方 v0.4.7 文档也明确说明生产 Docker 隔离、容器逃逸负向测试仍未完成验收（`docs/deployment/production-v0.4.7.md:10,33-35`）。

### [高] 7443/7444 若直接全网开放，攻击面过大

证据：Server 分别启动 gRPC 与 HTTP listener（`internal/server/server.go:128-142,164`）；HTTP 和 gRPC 跨主机都依赖 TLS 1.2+，应用身份认证使用 Bearer Token，当前生产协议不启用客户端证书认证（`internal/server/http.go:466-490`；`internal/rpcutil/rpc.go:154-165`；`docs/deployment/container-images.md:49-56`）。文档要求 7443 限 Worker 网段、7444 限管理客户端/受信代理（`docs/deployment/container-images.md:83-85`）。

影响：TLS 保护链路，但不能替代来源网络限制；被盗 Bearer Token 可在有效期内被重放。公开入口还增加扫描、暴力请求和资源耗尽风险。

处理：首选 VPN/私网；若必须公网访问，仅开放 7444 给受信入口，7443 只允许 Worker 固定出口地址；配置速率/连接/请求并发限制、告警与 token 轮换。Worker 不要暴露公网入站端口。

### [中] Bearer Token 是静态持有者凭据，泄露后可被冒用

证据：Server 将 token 的 SHA-256 用作身份索引，收到 Bearer 值后按摘要查身份（`internal/rpcutil/rpc.go:29-50,60-68`）；TLS 客户端把 Bearer Token 附加到 RPC metadata（`internal/rpcutil/rpc.go:168-202`）。这避免明文传输，但不是 mTLS，也没有绑定设备密钥。

影响：用户 token 误入日志、终端历史、镜像层、备份或 Worker 上的任务环境后，攻击者可获得对应身份权限；Worker token 泄露允许冒充该 Worker。

处理：每个 Worker、用户/服务分别使用不同 token；只授予所需 scope/project；密钥文件使用最小权限并通过 Secret Manager 注入；禁止写入命令行、镜像、CI artifact、任务 prompt 和日志；建立吊销/轮换流程。已发布或可能外泄的 token 应立即轮换。

### [中] Worker 出站网络与主机 Codex 配置挂载需要收紧

证据：当前 compose 为 Worker 添加 `host.docker.internal:host-gateway`，并挂载宿主 Codex 配置读写（`container-deployment-20261010/compose.yaml:23,28-30,41,46-48`）。部署文档建议 Worker 只需出站访问 Server，但该建议不是容器网络层的强制策略（`docs/deployment/container-images.md:194-204`）。

影响：被 Worker 进程或 Agent 任务滥用时，可能访问宿主服务、云元数据、私网服务并外传读取到的配置/凭据。

处理：生产 Worker 使用独立、专用 OS 身份和专用 Codex HOME；避免挂载个人主目录，配置/凭据只读挂载；通过防火墙/代理设置出站 allowlist，阻断 `host.docker.internal`、云元数据和管理网段；不挂 Docker socket。

## 已确认的正向控制

- HTTP 有请求体大小限制、请求/头部/空闲超时和 TLS 最低版本（`internal/server/http.go:84-99,466-490`）。
- gRPC 对每条 RPC 调用执行身份拦截并限制消息大小为 8 MiB（`internal/server/server.go:164`）。
- Worker 不要求 Server 入站连接，容器镜像以非 root 用户运行（`Dockerfile:43-47,67-72`）。
- Attempt OCI 参数包含非 root 用户、只读 rootfs、`cap-drop=ALL`、`no-new-privileges`、PID 上限和受限 tmpfs（`internal/agenthttp/server.go:455-460`）。这些是有价值的纵深防御，但不能取代真实目标主机逃逸/网络隔离验收。

## 上公网前的最低门槛

1. 确认 7443/7444 的公网暴露确有必要；否则只用 VPN/私网。云安全组和宿主防火墙都按来源地址限制端口。
2. 不将当前本机 compose 的个人 Codex 配置挂载方式直接复制到生产；改用专用凭据、最小权限、只读挂载和定期轮换。
3. 未完成 v0.4.7 真实 OCI 主机逃逸/网络负向测试前，只允许可信用户/租户提交任务。
4. 做一次外部端口核验、token 撤销/轮换演练、备份权限检查和告警验证；不得将测试 token/私钥放进报告或工单。

## 证据边界

本审查未读取或复述任何 token/私钥内容；未从目标 Ubuntu/Mac 主机执行公网扫描；因此不能确认当前云安全组、宿主防火墙、真实 Worker 出站规则或主机文件权限。结论针对已检查的仓库代码、部署文档和本机 compose 配置。
