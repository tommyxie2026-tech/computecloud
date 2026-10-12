# computecloud v0.4.8 部署与回退指南

本版是 Agent-aware Distributed Job Execution Platform / Agent Job Executor，新增公网入口限流、认证身份配额、可撤销 API key、Worker mTLS，以及严格 OCI 执行隔离和 Codex HTTP Attempt token/估算费用预算。它不提供任意不可信公网租户的生产安全认证；面向此类租户开放前，必须完成本文列出的目标环境安全验收。

## 版本与兼容性

- Server SQLite schema 为 v19，Worker schema 为 v6。Server 从 v18 升级时会执行 v18→v19 迁移；升级前先停止投递、排空 Attempt，并对完整数据目录做离线备份和恢复演练。
- Server 与 Worker 镜像都发布 `linux/amd64`、`linux/arm64`。生产部署固定 Release 中的 digest，不使用 `latest` 作为可复现版本标识。
- strict OCI 模式需要 Server 与 Worker 同时配置 `public_execution_mode: isolated_only`，并配置受支持的 Attempt Gateway、容器 Runtime、operator 管理的 egress network、seccomp 与资源限制。缺少任一条件时任务应 fail closed。
- 当前 strict 模式只支持带完整 usage 的 Codex HTTP / OpenAI Responses 兼容路径。Claude Gateway 尚未实现；不要将其作为 strict 模式下已支持的执行路径。
- Worker 软评分默认关闭；Relay 保持 `direct` 默认；Goal 自动 Re-plan 保持关闭。

## 发布产物

发布成功后，从 [v0.4.8 GitHub Release](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.8) 获取 Linux amd64/arm64 归档、`SHA256SUMS`、`CONTAINER_IMAGES.txt` 和镜像 digest。校验归档哈希，并确认 `CONTAINER_IMAGES.txt` 的 source SHA 与 Release tag 对应的提交一致。Server 与 Worker 镜像均应包含 amd64、arm64 manifest；部署时使用文件中记录的 digest。

## 升级步骤

1. 确认升级窗口，停止新 Job 提交并排空或取消在途 Attempt；记录当前版本、配置、Worker ID 和凭据引用。
2. 停止 Server 与 Worker。对 Server/Worker 数据目录和配置做权限受控的离线备份；记录校验和，并在隔离副本完成恢复检查。
3. 确认 Server 旧库 `PRAGMA user_version` 为 v18，SQLite `integrity_check` 返回 `ok`。不要手工修改 `user_version`。
4. 固定 v0.4.8 Server 镜像 digest 并启动，确认迁移完成后 schema 为 v19、健康检查通过；随后逐台升级 Worker，确认注册、证书身份和 schema v6。
5. 先以可信 canary 验证 Job 提交、执行、取消、Artifact 读取、用户/项目隔离、key 撤销和 Worker 证书撤销。严格公网任务还需验证 OCI 隔离和出站网络策略，再逐步开放流量。

## 公网入口与安全验收

- 对外入口应仅为预期的 TCP 7443（gRPC/TLS passthrough）和 HTTPS 7444（L7 Gateway/WAF）；数据库、指标、管理端口、容器工作目录和 Docker socket 不得暴露。
- 源站防火墙仅允许边缘 LB/WAF 地址访问服务入口。必须从外网验证不能绕过边缘直连源站，验证 TLS、速率限制、请求大小限制及认证头未被代理移除或缓存。
- 在 Server 和每台 Worker 上显式配置 `public_execution_mode: isolated_only`，执行容器逃逸、宿主凭据/目录访问、私网/metadata/host-gateway/未批准公网出站等负向测试，并确认失败发生在网络或容器隔离层。
- 验证 API key 过期/撤销和 Worker mTLS 轮换/撤销后旧凭据立即失效。日志与验收记录不得包含 token、私钥或完整敏感请求体。
- Attempt Gateway 的费用边界依赖管理员单价和上游 usage，可能与供应商账单不一致；它不是供应商硬账单上限。面向不可信租户前，必须在模型供应商账户或组织侧配置并验证硬支出上限。无法验证时，不得向不可信任务提供自动付费凭据。
- Claude Gateway 当前 fail closed。CI 和模拟验收不能替代生产边缘、真实 Worker 证书、OCI/egress 负向测试或供应商账单上限证据。

公网安全 evidence JSON schema 和验收步骤见[公网访问安全验收手册](../validation/public-access-security-acceptance.md)。

## 回退

停止新任务并排空或停止所有 Attempt；停止 v0.4.8 进程。恢复升级前完整 Server/Worker 数据快照与原配置，再固定回退版本的镜像 digest 启动。v0.4.7 Server 不能打开 v19 数据库；不得通过降低 `PRAGMA user_version` 回退。回退后核对旧 schema 和 SQLite 完整性，并确认已撤销的 API key、Worker 证书指纹不会因快照恢复而重新启用。升级后新写入数据不在旧快照内，记录恢复窗口及其数据损失。
