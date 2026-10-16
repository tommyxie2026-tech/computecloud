# computecloud v0.4.9 部署与回退指南

本候选维持 Agent-aware Distributed Job Execution Platform / Agent Job Executor 定位。它纳入 Agent HTTP 长请求处理、Worker strict 部署加固和受限 Claude Anthropic Messages Attempt Gateway。通过 CI 不代表真实 Provider、独立主机或公网生产安全验收已经完成。

## 版本与兼容性

- Server SQLite schema 保持 v19，Worker schema 保持 v6；本版本不新增数据库迁移。
- Server 与 Worker 发布 Linux amd64、arm64 归档及多架构容器镜像。部署时使用发布资产 `CONTAINER_IMAGES.txt` 中的不可变 digest，不以 `latest` 作为复现依据。
- strict OCI 模式要求 Server 和 Worker 同时使用 `public_execution_mode: isolated_only`，配置受支持的 Attempt Gateway、容器 Runtime、operator 管理的 egress network、seccomp 与资源限制。条件不满足时应拒绝执行。
- Codex / Claude Attempt Gateway 只支持各自文档声明的受限协议与固定 CLI 合约。Claude 费用边界基于已结算 usage 与管理员价格估算，不是供应商账单硬上限；Claude token hard limit 不支持。
- Worker 确定性软评分默认关闭；Relay 保持 `direct` 默认；Goal 自动 Re-plan 保持关闭。

## 发布产物

从 [v0.4.9 GitHub Release](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.9) 获取 Linux amd64/arm64 归档、`SHA256SUMS`、`CONTAINER_IMAGES.txt` 和镜像 digest。发布前该地址与产物摘要尚不可用；发布后按资产校验归档哈希、镜像平台清单，并核对 `CONTAINER_IMAGES.txt` 的 source SHA 与 Release tag 指向相同提交。

发布流水线与实际产物状态见 [v0.4.9 发布记录](../implementation/v0.4.9-release-status.md)。

## 升级步骤

1. 停止新 Job 提交并排空或取消在途 Attempt；记录正在使用的版本、配置和凭据引用。
2. 停止 Server 与 Worker。对完整 Server/Worker 数据目录及配置做权限受控的离线备份，并在隔离副本完成恢复检查。
3. 确认 Server SQLite `PRAGMA user_version` 为 v19 且 `integrity_check` 返回 `ok`。不要手工修改 schema 版本。
4. 固定 v0.4.9 Server 镜像 digest 并启动；确认健康检查通过后逐台升级 Worker，核对 Worker 注册、证书身份及 schema v6。
5. 以可信 canary 验证 Job 提交、执行、取消、Artifact 读取、用户/项目隔离和 key 撤销。使用 strict 模式时，还需按安全验收章节验证隔离边界。

## 公网入口与安全验收

- 只开放经设计批准的 TLS 服务入口；数据库、指标、管理端口、容器工作目录和 Docker socket 不得暴露。
- 源站访问仅允许受控边缘代理；从外部验证不能绕过边缘访问源站，并核对 TLS、速率限制、请求大小限制和认证头处理。
- 在 Server 和每台 Worker 上显式配置 `public_execution_mode: isolated_only`，执行容器逃逸、宿主凭据/目录访问、私网/metadata/host-gateway 和未批准公网出站的负向测试。
- 验证 API key 过期/撤销及 Worker mTLS 轮换/撤销后旧凭据失效。日志和验收记录不得保存 token、私钥或完整敏感请求体。
- Attempt Gateway 的费用控制不是供应商账单硬上限。面向不可信租户前，必须在供应商账户/组织侧设定并验证硬支出上限；无法验证时，不得为不可信任务提供自动付费凭据。
- CI fixture 不替代真实 Provider、真实 Worker 主机、生产边缘、OCI/egress 负向测试或证书撤销演练。目标环境验收保持独立跟踪。

安全验收步骤见[公网访问安全验收手册](../validation/public-access-security-acceptance.md)。

## 回退

停止新任务并排空或停止所有 Attempt；关闭公网入口并停止 v0.4.9 Server/Worker。恢复升级前完整 Server/Worker 数据快照与原配置，再使用与该快照兼容的旧版本镜像 digest 启动。当前本版本不改变 schema v19/v6，但回退前仍须核对目标旧版支持的 schema 和 SQLite 完整性；禁止通过降低 `PRAGMA user_version` 回退。

恢复快照会回到快照时的 API key 数据和 Worker 证书 fingerprint 配置，快照之后的撤销不会自动保留。保持公网入口关闭，按独立保存的凭据变更记录重新撤销已吊销的 key 和证书，并逐项确认旧凭据被拒绝。若无法完整核对变更记录，则在恢复服务前轮换仍有效的 API key 和 Worker 客户端证书、更新服务端 fingerprint 清单，并验证旧凭据不可用。完成凭据核验后才重新开放入口。记录恢复点之后可能丢失的数据。
