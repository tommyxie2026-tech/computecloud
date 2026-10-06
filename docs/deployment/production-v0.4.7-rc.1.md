# computecloud v0.4.7-rc.1 限定范围预览版部署与回退

本候选版仍是 **Agent-aware Distributed Job Execution Platform / Agent Job Executor**。它是从 v0.4.6 到当前 main 的累积预览，不是 Relay 或 Goal 自动 Re-plan 的生产认证。只有 GitHub prerelease、对应 SHA 的成功发布 CI 和可校验产物齐备，才视为已发布；准备此文档不等于已发布。

## 范围与开关

- 保留现有 Job → Stage → Task → Attempt 与 `direct` 默认执行路径。
- 包含 Prepared Workspace 缓存/warm/有界 GC、readiness **观测**、synthetic Goal/Plan/Graph 与 Artifact 评估、Control 决策/审计及产物文本预览。
- 包含实验性 Relay transport seam、显式操作员 TLS fixture、direct-first fallback 与恢复测试；不提供无人值守配对/默认配置接线，不把 Relay 作为默认或生产连接方式。
- 自动 Re-plan 保持关闭；没有 Guard→新 Plan/Job 原子发布，也没有可强制的 Runtime token/cost 上限。有限 token/cost 策略无法安全执行时继续 fail closed。
- Readiness scoring、Mobile pairing/Push/签名安装包、真实多机生产基线不在本候选版的完成声明中。

## 获取和核验

发布完成后从 GitHub prerelease `v0.4.7-rc.1` 获取 `linux_amd64` 或 `linux_arm64` 归档和 `SHA256SUMS`，执行 `sha256sum --ignore-missing -c SHA256SUMS`、`tar -tzf` 与解包后的 `computecloud version`。同时核对 Release 目标 SHA、发布 CI、`CONTAINER_IMAGES.txt` 中的不可变镜像 digest。候选版容器仅使用版本标签或 digest，**不得更新或部署 `latest`**。未完成上述核验时继续使用 v0.4.6。

## 从 v0.4.6 升级：Server v13 → v16，Worker 仍为 v6

1. 固定旧版二进制、配置、TLS/Token 引用与数据目录位置；暂停新 Job，等待或取消所有在途 Attempt 和 Gateway request。不能在旧版仍执行任务时迁移，也不做跨 schema 混合版本滚动升级。
2. 停止 Server 与所有 Worker。以 v0.4.6 二进制执行 `computecloud backup --data-dir <server-data-dir> --out <outside-data-dir>/server-v13.tar.gz`；该命令离线检查已排空 Attempt 和 SQLite 完整性，并备份整个 Server 数据目录（含 Artifact）。另对每个已停止的 Worker **完整**复制其 data_dir（含 SQLite、Workspace、模板和 Artifact），保留权限与独立副本。为所有备份记录 SHA-256 与保存位置。
3. 在隔离目录先恢复一份备份并核验 `PRAGMA user_version=13`、`PRAGMA integrity_check=ok`、`PRAGMA foreign_key_check` 无记录，确认原备份可读；不要在唯一备份上做升级演练。
4. 安装已核验的候选版 Server/Worker 二进制。先启动 Server，确认迁移到 v16、无异常日志；再逐台启动 Worker，确认 Worker schema 仍为 v6、注册与 capability 正常。默认使用 direct，保持自动 Re-plan 关闭。
5. 再次核验 Server/Worker 数据库完整性和外键，执行受控 single 与 Map/Reduce Job、Artifact 读取、取消、bounded Retry、Control 只读/审批权限及重启恢复检查。确认旧 generation 不能提交结果后再恢复新 Job 投递。

仓库的 `TestReleaseV13ToV16AndSnapshotRestore` 用发布版 v13 schema fixture 检查 v16 迁移、历史 Job 保留和 v13 快照恢复；此自动化测试不替代上述真实部署演练。

## 失败时回退

立即停止投递，停止所有候选版进程并保存故障现场。将升级后的数据目录隔离保存，从**升级前**的 Server 归档和每个 Worker 完整副本恢复到新的空目录，恢复旧版配置及 v0.4.6 二进制。先确认 Server 数据库为 v13、Worker 为 v6、SQLite integrity/foreign-key 检查通过，再启动旧版 Server 和 Worker 并做受控 Job 验证。升级后创建的 Job/决策不会自动进入旧快照，须作为恢复窗口的数据损失记录；不得手改 `PRAGMA user_version`，也不得让 v0.4.6 直接打开 v16 数据库。

## 未完成的验收

两台独立 Linux Worker、固定真实 Runtime/MCP 版本、授权账号额度、24 小时以上运行、故障与备份恢复仍需单独证据。缓存单次准备 P50 仍高于原定的 cold P50 40% 目标；Relay 未做真实 NAT/长任务验收。故本候选版不标记生产认证或稳定 Relay/Goal 闭环。
