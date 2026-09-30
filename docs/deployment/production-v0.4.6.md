# computecloud v0.4.6 阶段性稳定版部署与升级

本版保持 Agent-aware Distributed Job Execution Platform / Agent Job Executor：单 Go Server、SQLite、Worker 主动连接，以及 Job → Stage → Task → Attempt 的 durable execution truth。

**发布是否成立以 GitHub Release v0.4.6 及其成功 CI 为准。** 文档或 release request 合并本身不表示产物已发布；不通过 CI 时继续使用上一正式版，不手动上传未经验证的二进制。

## 1. 发布范围与边界

相对上一正式版 **v0.4.5**，本版收敛并发布：

- C2 Job Submit：提交幂等、Job 查询和 bounded Manual Retry；
- UI-04a Mobile Control Foundation：安全连接 profile、iOS/Android identity、computecloud: 深链和 SecureStore；
- v0.4.5 已有的 Workspace、Runtime/Tool/Environment、Control ledger、generation fencing、容器和发布能力。

移动端是 companion control client，不是 Worker。v0.4.6 没有签名 IPA/APK/AAB；CI 的 Expo iOS/Android export 仅用于契约验证。移动端部署、CI artifact 位置和正式原生发布缺口见 [移动端 Control 部署指南](mobile-control.md)。

以下仍未完成：Relay/P2P 运行时代码、Goal Governance 闭环、移动 Push/pairing、原生签名商店发布、HA Server、企业 RBAC。Fixture 通过不等价于真实 Codex/Claude 账户生产验收。

## 2. 获取与校验

正式发布地址：<https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.6>。

~~~sh
version=0.4.6
arch=amd64 # 或 arm64
base=https://github.com/tommyxie2026-tech/computecloud/releases/download/v${version}
curl --fail --location --remote-name "${base}/computecloud_${version}_linux_${arch}.tar.gz"
curl --fail --location --remote-name "${base}/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf "computecloud_${version}_linux_${arch}.tar.gz"
"./computecloud_${version}_linux_${arch}/computecloud" version
~~~

预期版本为 computecloud 0.4.6 ...；校验或版本不匹配时停止安装。压缩包包含 Linux 二进制、README、CHANGELOG、当前 DEPLOYMENT.md、MOBILE-DEPLOYMENT.md、基础配置说明和 examples。

首次部署的账户、TLS、Token、端口、systemd 和基础 YAML 沿用仓库 [v0.2 正式部署指南](production-v0.2.md)；其中旧版本号和旧 schema 不适用于本版，使用本文的版本和升级流程。

## 3. 从旧版本升级

目标 Server schema **v12**、Worker schema **v6**。v0.4.6 本身不增加 SQLite migration；从更早版本升级仍会执行已有 migration，必须先备份。

1. 停止投递新任务，完成/取消在途任务，确认没有 active Attempt。
2. 停止全部 Worker 和 Server；不进行跨 schema 混合版本滚动升级。
3. 离线备份 Server 和每个 Worker 的完整 data_dir（数据库、Artifact、Workspace/模板），保留旧二进制、配置、Token 和版本记录。
4. 安装已核验的同版本二进制，先启动 Server，确认 migration 成功，再逐台启动 Worker。
5. 在只读检查窗口执行：

~~~sql
PRAGMA user_version;
PRAGMA integrity_check;
PRAGMA foreign_key_check;
~~~

预期 Server 为 12、Worker 为 6；integrity 为 ok，foreign_key_check 无记录。不要手工降低 schema version。
6. 检查 Worker 注册和 capability，提交受控任务并验证 Artifact、取消、审批与 fenced retry，再恢复常规投递。

## 4. 容器与加密网络

容器 IP+端口、TLS SAN、Bearer Token、防火墙和回退规则见 [多架构容器镜像部署与发布](container-images.md)。跨主机必须使用 TLS；7443 为 gRPC，7444 为 HTTPS/Job/Control API。移动端只访问 HTTPS 7444，不连接 Worker 端口。

## 5. Prepared Workspace 与控制面运维

模板摘要不匹配时 fail closed；每个 Attempt 保持独立可写 workspace。Server 的 operation/approval ledger 是控制事实源；重复 operation 幂等，旧 generation 结果不能提交当前状态。自动 template GC、warm pool、Push 和 Relay 尚未交付。

## 6. 回退

停止新流量和全部进程，保留故障现场副本，恢复升级前完整 data_dir、旧配置和旧二进制后再启动。旧二进制不得直接打开已升级数据库；不手改 PRAGMA user_version。

## 7. CI → Package → Release

1. Release PR 设置 Makefile VERSION=0.4.6，提交 release/v0.4.6.json。
2. PR 和实际合并 SHA 的 main CI 必须通过，包括 control-mobile-check、Prepared Workspace、Control、package 和 container Gate。
3. package 构建 Linux amd64/arm64，校验架构/版本并生成 SHA256SUMS。
4. release 创建正式 v0.4.6，已有 tag/release 不覆盖。
5. 发布后核对 tag/目标 SHA、CI conclusion、两个归档、SHA256SUMS、CONTAINER_IMAGES.txt，并在 Actions 中保存 mobile-check artifact 证据。全部成功才标记 RELEASED。
