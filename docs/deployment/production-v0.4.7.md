# computecloud v0.4.7 稳定版部署指南

本指南适用于 Agent-aware Distributed Job Execution Platform / Agent Job Executor。v0.4.7 已正式发布，归档、`SHA256SUMS`、`CONTAINER_IMAGES.txt` 和 GHCR 镜像 digest 可从 [v0.4.7 Release](https://github.com/tommyxie2026-tech/computecloud/releases/tag/v0.4.7) 获取，并核对归档校验和、Release tag 与源代码 SHA。发布记录列有已核验的归档 SHA-256 和 Server/Worker 多架构镜像 digest；部署时建议固定 digest 而不是使用可变标签。

## 版本边界

- Server schema：v18；从 v0.4.6 的 v13 升级时执行 v13→v18 迁移。Worker schema：v6。
- Worker 软评分默认关闭；只有显式启用 `prepared_workspace_score_enabled` 才参与排序。硬能力/环境资格过滤仍先执行。
- `direct` 仍为默认传输；`direct_then_relay` 是显式 opt-in。Goal 自动 Re-plan 保持关闭。
- 真实 Runtime、独立双机升级恢复、生产 Docker 隔离、代表性缓存和 NAT Relay 尚未完成生产验收。CI 成功不代表这些环境验证通过。

## 已发布产物校验

- Linux amd64 归档 SHA-256：`83e3077bfa2592d58a9cfc883b669d1f19dae083c608d65d0bef9a7a0f276176`
- Linux arm64 归档 SHA-256：`0e86f3a029410ded94958152bf833433960a728ab255de06a6e3ba364f605c32`
- Server multi-arch digest：`sha256:477488d772c5f1a442ae39fe59a391ef06d6f06409f22fae9bfa3a5e0040ee58`
- Worker multi-arch digest：`sha256:8cea4e5efacbcbf3fc814f475f3a3ac36d64cdf6f1758bf0dd5141ac209fd311`

完整 CI 与产物信息见[发布记录](../implementation/v0.4.7-release-status.md)。

## 从 v0.4.6 升级

1. 暂停新 Job，等待或取消在途 Attempt，记录版本、配置和凭据引用。不要跨 schema 混合版本滚动升级。
2. 停止 Server 与所有 Worker。在原数据目录之外创建离线备份，并为 Server/Worker 数据目录保留权限与 SHA-256。先在备份副本演练恢复。
3. 在隔离副本验证旧 Server `PRAGMA user_version=13`、`integrity_check=ok`，并确认备份可读取。
4. 使用已核验的 v0.4.7 Server 启动并完成 v18 迁移；确认健康后再启动 Worker，核验注册、能力与 v6 状态。
5. 验证 Job 提交/执行、取消、Artifact 读取、权限隔离、重启恢复和旧 generation 拒绝。未单独评估前保持 Worker 软评分关闭、Relay 使用 `direct`、Goal 自动 Re-plan 关闭。

## 回退

停止新投递和候选进程，从升级前的完整快照恢复 Server 与 Worker 数据，再恢复 v0.4.6 二进制和配置。确认 Server schema v13、Worker schema v6 与 SQLite 完整性后再启动。禁止手工降低 `PRAGMA user_version`，也不要让 v0.4.6 打开 v18 数据库。升级后写入的新数据不在旧快照内，需记录恢复窗口的数据损失。

## 发版后验收

独立 Linux Worker、真实 Runtime/MCP、容器逃逸负向测试、24 小时长任务、真实 NAT Relay、代表性缓存 P50/P95 与目标环境升级/恢复仍需按 [v0.4.7 稳定版台账](../implementation/v0.4.7-stable-tracker.md) 提交脱敏证据。它们保持待验收，不影响本次 CI 门禁放行。
