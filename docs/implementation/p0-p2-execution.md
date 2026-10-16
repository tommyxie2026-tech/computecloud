# P0–P2 实施与验收台账

本台账区分实现、自动化验证、合并/发布和目标环境验收。代码与 CI 通过不能替代真实主机、Provider、生产网络或性能证据。

## 当前基线

- 稳定版：v0.4.8，发布记录见 [v0.4.8 release status](v0.4.8-release-status.md)。
- 主线后续已合入 PR #148（长请求处理）和 PR #150（Worker 部署加固、Claude Attempt Gateway）；主线 push CI run [38815497011](https://github.com/tommyxie2026-tech/computecloud/actions/runs/38815497011) 成功。上述改进尚未纳入 v0.4.8 已发布产物。
- Server schema v19 / Worker schema v6；升级必须先备份，禁止降低 SQLite `user_version`。
- 发版规则：精确候选 SHA 的所有必需 CI 通过且没有未解决的严重正确性问题即可授权稳定版发布；目标环境验收继续单独跟踪，不得宣称已完成。

| 优先级 | 范围 | 代码/CI 状态 | 仍需验收 |
| --- | --- | --- | --- |
| P0 | v0.4.9 收尾与版本一致性 | PR #148/#150 已合并；README、路线图、台账与候选发布文档已对齐，主线 CI 通过 | 精确候选提交的全量 CI、package、多架构镜像和 release evidence |
| P1 | Runtime / Environment / Trigger | HTTP Runtime、Container EnvironmentProvider、CI Trigger/Delivery 已有代码与 CI 路径 | 固定版本真实 Runtime、服务重启/恢复、真实 CI→Server 幂等和来源证明、OCI/网络隔离负向测试 |
| P1 | Goal / RPG 与受限预算 | Synthetic Goal、原子 Plan/Job 发布、一次性审批消费及受限 `claude_http` 估算费用控制已有代码与 CI | 固定版本真实 Runtime、独立主机恢复及真实 Provider 合约；自动 Re-plan 继续关闭，真实账单上限不由应用侧估算替代 |
| P1 | Workspace 缓存 | provider fixture 和 CI 缓存 P50 目标通过，准备缓存代码与观测已合入 | 代表性 Linux Worker/真实 Job 的冷/热准备 P50/P95 与完整 Job 时延 |
| P1 | Agent-aware readiness scoring | 可选、确定性评分与解释性 Gate 已存在，默认关闭；硬过滤优先 | 代表性负载下的性能基线和受控启用验收；不得绕过能力、安全、权限或并发限制 |
| P1 | 公网安全 | API key 生命周期、Worker mTLS、strict OCI 与 Codex/Claude Attempt Gateway 代码和 CI 已实现 | 公网边缘/源站防护、证书轮换撤销、真实 OCI/egress 负向测试、固定 CLI/Provider 验收和供应商硬账单上限 |
| P2 | 实验性 Relay | Transport seam、direct-first/显式 relay fallback、控制流背压、恢复 Gate、指标与运维说明已具备；`direct` 仍是默认 | 真实 NAT、断网、Relay 重启、长任务及目标网络故障验收；未通过前不宣称生产默认可用 |
| P2 | Control / Mobile | Control 观察、提交/有限重试和 Artifact 审阅已交付；Mobile foundation 已有 | Mobile pairing/push、平台签名和设备验收归 v0.5.x 独立切片 |

## v0.4.9 执行顺序

1. 对齐 README、长期路线图、release status、安全跟踪和历史实施台账，明确哪些只是历史记录。
2. 核验 Claude Attempt Gateway 与 Worker strict 边界的定向、安全负向、恢复和漏洞扫描 Gate；只修复可复现缺口。
3. 验证 v0.4.8 数据升级兼容性、备份/恢复测试和 amd64/arm64 package/container Gate。
4. 冻结精确候选 SHA，运行所有必需 main/release CI，生成脱敏 release evidence 后再请求正式发布。

详见 [v0.4.9 稳定收尾迭代计划](../superpowers/plans/v0.4.9-release-iteration.md)。真实主机、真实 Provider、NAT Relay、证书撤销、OCI escape、供应商账单硬上限和代表性缓存性能仍按目标环境记录为 `目标环境待验收` 或 `延期`，不由 CI 关闭。

## v0.5.0 后续主线

聚焦 Agent-aware Scheduler：硬能力/安全/凭据/环境约束保持先行，确定性软评分默认关闭，并记录候选过滤原因、主要评分因素、选中 Worker 和回退原因。Mobile pairing/push、Execution Usage MVP、Private Worker Pool 和跨节点 Artifact 各自切片，不作为 v0.4.9 发版条件。
