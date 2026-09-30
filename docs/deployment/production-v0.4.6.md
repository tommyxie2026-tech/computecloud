# computecloud v0.4.6 阶段性稳定版部署与升级

本版保持 Agent-aware Distributed Job Execution Platform / Agent Job Executor：单 Go Server、SQLite、Worker 主动连接，以及 Job → Stage → Task → Attempt 的 durable execution truth。

**发布是否成立以 GitHub Release `v0.4.6` 及其成功 CI 为准。** 文档或 release request 合并本身不表示产物已发布。不通过 CI 时继续使用上一正式版，不手动上传未经验证的二进制。

## 1. 发布范围与边界

相对上一正式版 v0.3.2，本版累计包含：

- Workspace lifecycle、长任务 lease/deadline 和公平调度基础；
- Runtime API v2、Tool/Environment capability 与 process EnvironmentProvider execution；
- Prepared Workspace Core：固定仓库/commit 的模板、内容摘要验证、独立可写 Attempt materialization、重启检查与 corruption fail-closed；
- Agent Control read/SSE、持久 command/receipt、审批生命周期、ACK 幂等与 generation fencing；
- Goal computing 基础和 RPG-1～3 的 Re-plan Guard。

以下能力尚未作为完整交付：RPG-4 Goal Governance、Goal Approval bridge、Workspace GC/warm pool/性能优化、readiness scoring、Control PWA/Mobile、ACP-4b Resume。Codex/Claude 当前仍只广告已认证的 control capability；不支持的审批/交互操作返回 `CAPABILITY_UNSUPPORTED`。Fixture 审批通过不代表原生 CLI 已支持。

本版没有通用容器/VM sandbox、HA Server 或企业 RBAC；process Environment 仍要求受信 Worker 主机。本次 CI 使用 fixture，不消耗真实模型额度，不等价于真实账户生产负载验收。

## 2. 获取与校验

正式发布地址：<https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.6>。
按主机架构设置 `arch=amd64` 或 `arch=arm64`：

```sh
version=0.4.5
arch=amd64
base=https://github.com/tommyxie2026-tech/computecloud/releases/download/v${version}
curl --fail --location --remote-name "${base}/computecloud_${version}_linux_${arch}.tar.gz"
curl --fail --location --remote-name "${base}/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf "computecloud_${version}_linux_${arch}.tar.gz"
"./computecloud_${version}_linux_${arch}/computecloud" version
```

预期版本为 `computecloud 0.4.5 ...`。校验或版本不匹配时停止安装。
发布包包含 Linux 二进制、README、CHANGELOG、当前 `DEPLOYMENT.md`、基础配置说明 `DEPLOYMENT-v0.2.md` 与 examples。

首次部署的账户、TLS、Token、端口、systemd 和基础 YAML 沿用仓库 `docs/deployment/production-v0.2.md`（包内 `DEPLOYMENT-v0.2.md`）；其中旧版本号和旧 schema 不适用于本版，使用本文的版本和升级流程。

## 3. 从旧版本升级

目标 Server schema **v12**、Worker schema **v6**。C2/UI-04a 本轮不增加 schema version；旧版升级仍会执行已有 migration，必须先备份。

1. 停止投递新任务，用旧版本完成/取消在途任务，确认 Server `SELECT count(*) FROM attempts WHERE released=0;` 为 **0**；未知 cleanup 先按旧版恢复流程处理，不直接释放 Attempt。
2. 停止全部 Worker 和 Server；不进行跨 schema 混合版本滚动升级。
3. 离线备份 Server 和每个 Worker 的**完整 data_dir**（包括数据库、Artifact、Workspace/模板），同时保留旧二进制、配置、Token 与版本记录。Server 可额外执行 `computecloud backup --data-dir <server-dir> --out <new-backup-file>`；该命令不是 Worker 备份接口。
4. 安装已核验的同版本二进制，先启动 Server，确认 migration 成功，再逐台启动 Worker。Server 的旧 schema 若仍有 active Attempt 会拒绝升级。
5. 服务停止或只读检查窗口内检查数据库：

```sql
PRAGMA user_version;
PRAGMA integrity_check;
PRAGMA foreign_key_check;
```

Server 预期 12、Worker 预期 6；integrity 为 `ok`，foreign_key_check 无记录。不要手工降低 schema version。

6. 检查 Worker 注册和 capability、提交一项受控任务、验证 Artifact、取消与重试边界；确认后再恢复常规投递。新版本进程运行正常不替代实际 CLI/账号的安装验收。

## 4. Prepared Workspace 运维

- 模板由配置授权的本地 repository 和固定 commit 生成，materialization 仍属于独立 Attempt；不能共享可写 generation 路径。
- 模板摘要不匹配时 fail closed，不以任意残留目录作为有效模板。模板不是数据库之外的第二套任务事实源。
- 目录可由所属账户回收，内容完整性靠摘要检查；不是针对同 UID 恶意进程的隔离边界。
- 自动 template GC、warm pool 与容量策略尚未交付。监测 Worker data_dir 磁盘增长，维护清理前必须 drain/停止对应 Worker，并遵守现有 Workspace ownership/recovery 规则。

## 5. 审批与恢复

Server 的 approval/operation ledger 是事实源。相同 operation 请求返回既有 receipt；终态 ACK 重复到达不重复改变审批或追加完成事件。旧 generation 的首次结果不能提交到当前状态。

Worker 在副作用结果不明确时返回 `EXECUTION_UNVERIFIABLE`，不自动重放审批。Runtime Approval 与 Goal Governance Approval 是不同领域；本版不通过 Runtime Adapter 批准 Goal Re-plan/Budget/Constraint。

## 6. 回退

停止新流量并停止全部进程，保留故障现场副本，恢复升级前完整 data_dir、旧配置及旧二进制后再启动。旧二进制不得直接打开已升级的数据库；不手改 `PRAGMA user_version`。回退会丢失备份点之后未另行保留的数据，因此应在恢复正式流量之前完成升级验收。

## 7. CI → Package → Release

1. Release PR 设置 Makefile `VERSION=0.4.5`，提交 `release/v0.4.6.json`，更新本指南和 CHANGELOG。
2. PR 运行完整 CI；WS-E 修复 PR #51 与发布 PR 都通过后才合并。
3. main 对**实际合并 SHA**重新运行全部 Gate，包含 Prepared Workspace contract/recovery 和 Agent Control approval；任一失败都阻止 package。
4. package 为 Linux amd64/arm64 构建归档、校验架构/版本、生成并验证 `SHA256SUMS`。
5. release 下载该次 CI 已验证的归档，创建正式 `v0.4.6` Release；已有 tag/release 不覆盖。
6. 发布后核对 tag/目标 SHA、CI conclusion、两个归档与 `SHA256SUMS`。所有步骤成功才标记 RELEASED。
