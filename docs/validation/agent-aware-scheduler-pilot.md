# Agent-aware Scheduler 双 Worker 受控试运行

本手册用于比较 `prepared_workspace_score_enabled=false` 与显式开启时的调度结果。它验证指定环境中的候选分布、时延和开销，不证明所有部署上的普遍性能收益。运行前完成[双机预检](dual-host-preflight.md)，并把 v0.4.9 Provider、Relay、安全、OCI/egress、缓存和账单验收继续作为独立 Gate 跟踪。

## 范围与安全边界

- 两个 Worker 必须是独立 Worker 身份，并按目标环境要求确认主机/VM 独立性；Server 和 Worker 的独立性单独记录。
- 评分只改变通过项目、Runtime、凭据、模板、安全、租户并发、Worker 租约及容量硬过滤后的候选排序。试运行不更改这些约束，也不放宽 Worker 信任边界。
- 试运行期间只使用预先批准的测试项目、凭据、仓库和任务。启用配置不得用于绕过生产发布或公网准入 Gate。
- 证据不得包含 prompt、对话正文、凭据/API key、绝对私有路径、原始仓库 URL 或其他项目的候选数据。保存的任务标识应使用受控引用或脱敏值。

## 运行前冻结记录

在开始 OFF 阶段前填写并由操作者确认本表。阈值和样本量必须预先确定，不能看到结果后再修改。

| 字段 | 预先填写 |
| --- | --- |
| Run ID / 操作者 |  |
| 源代码完整 SHA |  |
| Server 镜像 digest / 版本 |  |
| Worker A 镜像 digest / 版本 / Worker ID |  |
| Worker B 镜像 digest / 版本 / Worker ID |  |
| Server 配置文件版本引用（不含文件内容/秘密） |  |
| 两 Worker 的 Runtime 名称及精确版本 |  |
| 测试项目、仓库和任务集合的脱敏引用 |  |
| OFF / ON 每阶段任务数与采样规则 |  |
| 任务 mix、顺序、重试策略、超时和并发度 |  |
| 可接受的 Worker 分布/公平性范围 |  |
| 冷/热准备和完整 Job 时延的比较方法及回归阈值 |  |
| 失败率、重试率和调度开销的通过阈值 |  |
| 最小证据完整度及停止条件 |  |
| 回滚负责人、服务恢复方式和验证人 |  |

若尚未冻结任务数、阈值或回滚负责人，只能做预检，不得开始对照或把结果标为通过。

## 前置检查

1. 为 Server 与两个 Worker 收集同一源 SHA 的 preflight 记录；确认二 Worker ID 不同，Runtime 版本与镜像 digest 符合冻结记录，并保存 preflight verifier 输出。
2. 确认两个 Worker 都能执行相同任务集合，所需凭据、仓库授权和模板版本相同；确认 slots、项目并发和 credential 并发不会使一阶段被另一阶段的残留 Attempt 干扰。
3. 确认当前 Server 配置 `prepared_workspace_score_enabled: false`。保留受控的配置版本引用和 Server 启动/部署记录，不复制配置或秘密到证据包。
4. 验证两 Worker 持续上报 readiness，且有效观察窗口内信号处于新鲜状态；检查 scheduler observation 能显示评分开关和有界候选信息。信号缺失时仍可调度，但该次对照不能证明 readiness 信号有效。
5. 先用少量非计量冒烟任务确认目标环境可提交和完成任务。冒烟任务不计入 OFF/ON 样本。

## 对照执行

### A. OFF 基线

1. 保持评分关闭，确认 Server 当前实际加载的配置值为 false。
2. 按冻结的任务列表和顺序提交 OFF 样本；记录 Job/Task 的脱敏引用、Attempt 数、两个 Worker 的选择、完成状态和时间戳。
3. 收集每次 scheduler observation，确认 `scoring_enabled=false`，调度符合 least-active 与既有 tie rotation 行为。
4. 等待本阶段所有任务终态，释放或清理测试资源；按预先冻结的间隔重置/稳定缓存状态。不得把 OFF 阶段未完成的 Attempt 带入 ON 阶段。

### B. ON 受控阶段

1. 将 Server 配置中的 `prepared_workspace_score_enabled` 显式设为 `true`，保留修改审计。该配置由 Server 启动时读取；按部署流程重启/替换 Server 实例，并确认恢复后健康状态正常。
2. 等待两个 Worker 重新连接并产生新鲜 readiness；确认 Worker 身份、Runtime、slots 和 OFF 阶段一致。清理或等待旧 Attempt 完成后再开始采样。
3. 提交与 OFF 阶段同任务 mix、同顺序/随机规则、同并发、凭据和环境的 ON 样本。记录相同字段。
4. 对每次 scheduler observation 确认 `scoring_enabled=true`、`score_version=readiness-v1`；硬过滤候选不得出现 score，合格候选的分数/选择理由必须可解释。
5. 任一硬过滤异常、跨项目信息暴露或分数覆盖资格的迹象都立即停止 ON 阶段并按回滚步骤关闭评分。

## 每次任务需收集的脱敏证据

- Run ID、阶段（OFF/ON）、源 SHA、镜像 digest、配置版本引用。
- 脱敏 Job/Task/Attempt 引用、提交/派发/完成时间戳、终态、错误类别、重试次数。
- 所选 Worker ID，以及两个 Worker 的任务计数/份额；候选过滤原因和 Worker 选择理由。
- `scoring_enabled`、score version、候选 score 分项或 `UNKNOWN`/`STALE_OR_INVALID` 状态；只保留当前测试项目所需范围。
- 冷/热 Workspace 准备时延、端到端 Job 时延、调度器派发开销；清楚标明指标定义和采样边界。
- 每阶段样本总数、成功数、失败数、重试数、超时数、缺失证据数和配置变更记录。
- 宿主 CPU/内存/磁盘和网络负载的聚合采样（如本次比较使用），以及人为干预、Worker 重启、模型 Provider 限流等混杂因素。

不要收集 prompt/response、credential 内容、完整配置、环境变量 dump、私有路径或原始仓库 URL。证据文件在报告中使用相对文件名或受控存储引用，并按项目权限保存。

## 结果判定与停止条件

对 OFF/ON 阶段按冻结的阈值计算并报告：

- Worker 任务份额、最大偏斜和公平性；与预先定义的范围比较。
- 冷/热准备 P50/P95、完整 Job 延迟分布和变化；说明样本量及缺失值。
- 失败率、重试率、超时率和每类错误变化。
- 调度开销、Server/Worker 资源使用和证据完整率。
- 评分关闭与开启时的过滤原因、unknown/stale 信号比例和选择解释。

只有全部冻结阈值通过、证据完整且没有硬过滤异常时，才能记录“该受控环境对照通过”。出现持续单 Worker 垄断、达到预定回归阈值、硬过滤/隐私异常、ON 阶段评分证据缺失或样本不可比时立即停止；结果标记 `FAIL` 或 `INCONCLUSIVE`，不能挑选性删除失败样本。单次试运行不构成普遍性能提升或生产安全认证结论。

## 回滚

1. 将 `prepared_workspace_score_enabled` 设回 `false`，记录变更人和配置版本。
2. 按部署流程重启/替换 Server；确认 Server 健康并确认实际加载值为 false。
3. 等待两个 Worker 重新连接，提交一条预批准的冒烟任务，确认 scheduler observation 显示评分关闭并按 least-active/tie rotation 工作。
4. 若不能确认配置已关闭或硬过滤行为正常，停止新任务派发，按既定 Server 故障处置流程恢复；不通过修改 Worker capability 或放宽安全要求规避问题。
5. 保存 OFF/ON/回滚证据与失败分析，将功能保持默认关闭，直到问题修复并重新冻结/执行对照。

## 不由本试运行关闭的生产 Gate

本试运行不证明 Provider 真实调用正确、双机升级/恢复、公网源站安全、证书轮换撤销、OCI/egress 隔离、Relay/NAT 可用性、缓存性能目标、供应商真实账单硬上限或不可信租户安全。上述项目继续依据 [P0–P2 台账](../implementation/p0-p2-execution.md) 及各自验收材料独立管理。
