# computecloud v0.4.7-rc.2 候选版部署与回退

本版本是 **Agent-aware Distributed Job Execution Platform / Agent Job Executor** 的限定范围 prerelease。它冻结 Goal 治理与 Runtime 预算的代码/CI 边界，不是通用 Workflow Engine、LLM Gateway、Model Serving Platform、GPU Cloud 或 AI Execution OS。

## 范围与开关

- 保留 Job → Stage → Task → Attempt 语义和 `direct` 默认路径。`direct_then_relay` 必须由运维人员显式启用，仍是实验性功能。
- 包含 synthetic Goal/Plan/Graph、Artifact 评估、Control 治理审计、Guard → Plan/Job 原子发布和一次性批准消费。自动 Re-plan 保持关闭。
- 仅 cost-only、全 `claude_http` 的 Goal 可使用 `usd_micros_client_estimate` 边界。有限预算 Goal 仅允许一个在途 Attempt；缺失最终用量后不再继续调度。
- 不支持 Claude token、Codex token/cost 或混合 Provider 有限预算；它们保持 fail closed。客户端估算费用不是供应商账单上限。
- Prepared Workspace、HTTP Runtime/Container Environment、Trigger/Delivery 和 Relay 的真实环境验收未随候选版自动完成。

## 获取和核验

只从 GitHub prerelease `v0.4.7-rc.2` 获取 Linux amd64/arm64 归档、`SHA256SUMS` 和 `CONTAINER_IMAGES.txt`。执行 `sha256sum --ignore-missing -c SHA256SUMS`、`tar -tzf` 和解包后的 `computecloud version`；同时核对 Release 目标 SHA、发布 CI 及 Server/Worker 镜像 digest。候选镜像仅使用版本标签或 digest，不得更新/使用 `latest`。任一核验失败时继续使用 v0.4.6。

## 从 v0.4.6 升级：Server v13 → v16，Worker 仍为 v6

1. 暂停新 Job，等待或取消所有在途 Attempt/Gateway request，固定旧版二进制、配置和 TLS/Token 引用。不进行跨 schema 混合版本滚动升级。
2. 停止 Server 和所有 Worker。使用 v0.4.6 对 Server 执行 `computecloud backup --data-dir <server-data-dir> --out <outside-data-dir>/server-v13.tar.gz`；完整复制每个 Worker data_dir。将备份存到原目录之外，保留权限并记录 SHA-256。
3. 在隔离目录恢复副本，核验 Server `PRAGMA user_version=13`、`integrity_check=ok`、`foreign_key_check` 无记录；不在唯一备份上演练。
4. 安装已核验候选二进制，先启动 Server 并确认迁移到 v16，再逐台启动 Worker 并确认 v6/capability 注册。保持 `direct` 和自动 Re-plan 关闭。
5. 验证数据库完整性、single/MapReduce、Artifact 读取、取消、bounded Retry、Control 权限、重启恢复和旧 generation 拒绝。若启用 `claude_http` 费用边界，使用专用低额度账号，记录客户端版本、冻结额度、最终估算用量和终止原因。

`TestReleaseV13ToV16AndSnapshotRestore` 只是隔离 fixture 回归；不替代上述部署升级/恢复演练。

## 失败时回退

停止新投递和候选版进程，保留故障现场。从升级前 Server 归档和 Worker 完整副本恢复到新空目录，恢复 v0.4.6 二进制/配置，核验 Server v13、Worker v6 及 SQLite 完整性后再启动。不得手工降低 `PRAGMA user_version`，不得让 v0.4.6 直接打开 v16 数据库。升级后新数据不会出现在旧快照中，必须记录为恢复窗口数据损失。

## 未完成的验收

两台独立 Linux Worker、固定真实 Runtime/MCP、授权账号额度、24h+ 长任务、真实 NAT/断网、代表性缓存 P50/P95 与环境级升级/恢复仍需单独证据。因此本候选版不标记稳定版、生产认证、稳定 Relay 或自动 Goal Re-plan 闭环。
