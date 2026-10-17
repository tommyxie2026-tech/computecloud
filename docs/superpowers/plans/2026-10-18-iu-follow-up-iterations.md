# IU 后续迭代路线与实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在后续迭代中补齐 IU 的持久有效性、授权记忆复用、增量计算和可验证效果，逐阶段交付，不提前打开未验收能力。

**Architecture:** IU 继续作为现有 Task / Attempt 上的算元契约。Server 持有执行、准入、有效性和业务评估事实；Worker 执行冻结输入；aicloud 提供模型 Registry / Evaluation / Router。所有复用必须有授权和依赖证据，全量重算保留为回退路径。

**Tech Stack:** Go 1.27.2+、SQLite、现有 gRPC/protobuf、JSON Schema、Python CI 报告；本轮规划不新增必需外部服务。

**Spec:** [首轮完整设计目标](../specs/2026-10-17-iu-computation-model-design.md)、[当前运行契约](../../contracts/iu-graph-v1.md)、[实际交付范围](../../implementation/iu-computation-model.md)。

**状态：规划，未开始后续功能实现。** 文档日期为 2026-10-18。代码基线为 main `b38e4c408310464bd1a74d4e82362736bb405d55`；[PR #156](https://github.com/tommyxie2026-tech/computecloud/pull/156) 交付实验图，[PR #157](https://github.com/tommyxie2026-tech/computecloud/pull/157) 补齐认证门禁与缓存修正。[对应 main CI](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39041998004) 44 项运行检查成功。该证据不表示本计划中的待办已实现，也不表示已发布新版本。

## Global Constraints

- 当前图默认关闭；旧 single / map_reduce、Task / Attempt fencing、取消、deadline、Guard 与硬调度过滤继续有效。不建立第二套执行引擎。
- 当前上限：图请求 256 KiB、32 节点、128 边、并行度 8；编译上下文 64 KiB UTF-8 字节。delta 64 KiB、32 条记录、单 payload 8 KiB、每条最多 64 个 source refs。扩大限制必须单独评审和压测。
- Server 当前 schema v20、Worker v6。后续迁移版本以实施时 main 的下一个可用版本为准，不预占版本号；已冻结图、历史记录及回执不原地改写。
- 身份与授权包含 project、goal_id、graph_generation、plan_revision；内容摘要不是授权。授权撤销必须在事务提交点重查。
- validity 与 validation 正交；VALID 不等于 verified。模型只能产生 unchecked 候选。记忆保存来源、结论、约束和验证证据，不要求记录模型隐藏思维链。
- 原文可重建不等于语义完整。token ID 不跨 tokenizer 直接拼接；未知 usage/token/费用保持 unknown/null。未支持的 token/费用硬预算继续拒绝。
- 每个阶段分别提交可审阅 PR；CI 通过后才回填完成状态。保持全量重算回退和默认关闭；本计划不承诺发布日期或新的版本号。

## Review Focus

1. 失效与激活/准入/PASS 并发：同一事务顺序必须决定是否接受，旧输入不能在事件生效后继续被判为有效（任务 1）。
2. 跨代导入时授权被撤销、依赖缺失或 ID 冲突：不能只检验 view hash，也不能静默遗漏依赖（任务 2）。
3. 复用后重启/GC：不得伪造新 Attempt，原始 Artifact 与证据在引用有效期内不能被删除（任务 3）。
4. tokenizer、模型或序列化包装变化：必须重新计数和绑定实际输入，不能沿用旧计数或截断规则（任务 4）。
5. 验证器超时、真实模型无输出或 usage 缺失：保持 UNKNOWN/未测量，不漂白为 PASS 或零成本（任务 5/6）。

## 阶段与优先级

| 顺序 | 阶段 | 本轮状态 | 依赖与退出门槛 |
| --- | --- | --- | --- |
| 已完成基线 | IU-0 有限图、IU-1 可重复 CI | 实验性已交付 | 继续保留 18 场景、26 项 IU 必测和独立 Server/双 Worker 验收；认证门禁也必须实际运行 |
| 第 1 迭代 | IU-2 持久失效事件 | 未实现，最高优先级 | 事件持久化、两跳失效、事务重查、重启重放及权限/CAS 通过 |
| 第 2 迭代 | IU-3 授权跨代 Memory import | 未实现 | 依赖 IU-2；同一 Goal 跨代的依赖闭包、授权、版本与 provenance 通过 |
| 第 3 迭代 | IU-4 按依赖增量重算 | 未实现 | 依赖 IU-2/3；先与全量重算业务结果一致，再评估节省的实际调用与成本 |
| 可并行准备 | IU-5A 上下文/token 治理 | 未实现 | 不依赖增量重算；冻结 tokenizer/模板、实际输入核对、超限和回源路径通过 |
| 第 2～4 迭代 | IU-5B 独立记忆验证 | 未实现 | 可先设计；接入跨代时依赖 IU-2/3；验证作用域、证据与失效联动通过 |
| 全程取证，后续接入 | IU-5C 真实模型评测与 aicloud | 未实现 | 小样本单节点/全量 IU 可先做；增量对照等 IU-4；远程接入另开契约 PR |
| 最后推广 | IU-6 受控部署与发布 | 未完成 | 前序合同通过，并完成独立主机、恢复/升级、隔离和回滚演练 |

IU-5A/B/C 是原路线 IU-5 的细分，不改变 IU-2→IU-3→IU-4 的安全依赖。后续阶段涉及独立子系统时，各自生成阶段设计和逐任务计划，经评审后再实现；下面明确阶段交付、接口边界和验收，不把拟议接口当作已有 API。

## 现有代码与扩展落点

| 职责 | 已有文件 | 后续扩展 |
| --- | --- | --- |
| 图/来源/上下文 | `internal/graph/spec.go`、`context.go`、`execution.go`、`api/graph/v1/graph.schema.json` | 新合同严格版本化；保留冻结 schema 与原文 hash |
| 纯记忆计算 | `internal/graph/memory.go` | 复用 `Invalidate`、`MergeViews`、`ApplyDelta`；纯投影不代替持久服务 |
| 执行事务 | `internal/server/graph_execution.go`、`graph_memory.go`、`graph_evaluation.go`、`graph_replan.go` | 激活、准入、评估和新代发布统一检验有效性版本 |
| 存储 | `internal/store/graph_migration.go`、`migrations.go` | 追加事件、进度、授权、导入和复用 provenance；迁移遵循现有模式 |
| Worker 与 CLI | `internal/worker/graph_execution.go`、`cmd/computecloud` | 版本化能力协商、只读查询及拒绝原因；不增加旁路执行事实 |
| 验收与实验 | `scripts/ci_iu_graph.py`、`scripts/ci_task_flow.py`、`internal/iuexperiment`、`cmd/iu-experiment` | 扩充强制检查、故障矩阵和原始报告；真实模型结果单独保存 |

## Task 1 — IU-2：持久失效事件与事务闭环

**Files:** 新建 `internal/graph/validity.go`、`internal/server/graph_invalidation.go` 及对应测试；修改存储迁移、上述四个 Server 图事务文件、运行契约与 CLI 查询。

**拟议接口：** `graph.InvalidationEvent` 包含事件 ID、目标 Goal/generation、来源或 record 引用、reason、证据 hash、expected_revision；`Server.InvalidateGraph(ctx context.Context, jobID string, in graph.InvalidationEvent) error` 是业务入口。持久追加事件内容，应用进度另存；幂等键同内容重放成功，不同内容拒绝。新写接口沿用 principal/project 鉴权，阶段契约必须明确专用权限，不复用提交者自授权。

- [ ] 写阶段设计 `docs/superpowers/specs/iu-durable-invalidation.md`，冻结事件命名空间、授权、CAS、迁移和线性化点。首版拒绝修改已终结 Goal 的执行结论；迟到反证仅保留审计依据，不隐式重开 Goal。
- [ ] 先写 `TestGraphInvalidationSurvivesRestart`、`TestInvalidationBlocksLateAdmissionAndPass`、`TestInvalidationRejectsUnauthorizedOrStaleRevision`：覆盖至少两跳派生、重复/冲突事件、激活后事件、提交前撤权、旧代事件及事务回滚；在未实现时观察失败。
- [ ] 实现追加事件及可恢复应用；激活、Artifact 准入、业务评估、新代发布在各自提交事务中检查有效性 revision。已运行节点的冻结 Context 不改写，迟到产物按有效性复查。
- [ ] 运行 `go test -race ./internal/graph ./internal/store ./internal/server -run 'Test.*Invalidation' -count=1`；CI 增加事件提交前后、应用中断和 PASS 竞争的进程/DB 故障证据，要求测试实际 PASS。
- [ ] 提交独立 PR，并更新契约/运维说明。退出条件：重启不漏事件、不重复应用；事件先提交时旧证据不能 PASS；未受支持状态明确拒绝。

## Task 2 — IU-3：授权跨代导入与依赖闭包

**Files:** 新建 `internal/graph/import.go`、`internal/server/graph_import.go` 及测试；修改迁移、`graph_memory.go`、`graph_replan.go` 和版本化输入合同。

**接口边界：** 拟议 `ImportRequest` 指定目标 Goal/generation、expected_revision、授权主体和精确来源 `(project, goal, generation, view_sha256, record_id)`。返回不可变导入快照及 receipt。首版只允许同项目、同 Goal 的旧代到新代；跨 Goal/项目共享另案处理。原 record 身份和来源保留，目标 ID 用稳定映射区分命名空间，禁止按同名覆盖。

- [ ] 写 `docs/superpowers/specs/iu-authorized-memory-import.md`，冻结依赖闭包、目标 ID 映射、Schema/算元/来源版本匹配和授权撤销语义；禁止由 import 自动产生 verified 状态。
- [ ] 先写 `TestImportRejectsInvalidOrIncompleteClosure`、`TestImportRechecksRevokedGrantAtCommit`、`TestImportPreservesOriginAndNamespace`，覆盖 INVALID/NEEDS_REVALIDATION、缺失来源、冲突、跨 Goal、同名 ID 和失效竞争；观察失败。
- [ ] 实现事务内授权、revision 与闭包重查，固定 read-view、导入回执及 Artifact pin；旧历史保持不变。新代默认仍全量重算，只有显式导入请求才走复用路径。
- [ ] 运行 `go test -race ./internal/graph ./internal/store ./internal/server -run 'Test.*Import' -count=1`；增加导入提交前后重启、同键不同内容及 GC 竞争的 CI 必测项。
- [ ] 提交独立 PR。退出条件：折扣从 1000 修订到 2000 后，旧 11000 不能被导入为有效结论；合法且未失效的订单原始观察可按授权复用。

## Task 3 — IU-4：依赖增量重算

**Files:** 新建 `internal/graph/reuse.go`、`internal/server/graph_reuse.go` 及测试；修改图控制/新代发布/迁移、`internal/iuexperiment` 和订单示例。

**接口边界：** 拟议 `ReuseDecision` 输出 REUSE / RECOMPUTE / REJECT、reason、源节点和完整 provenance。key 必须覆盖实际输入、规则、read-view、算元/Schema、Runtime/model、工具/环境/工作区与策略版本。来源完整性不足或不可重放节点一律不复用。

- [ ] 写 `docs/superpowers/specs/iu-incremental-recompute.md`，定义受影响依赖闭包与新的节点复用事实；不得把复用伪装成新的 Worker Attempt 或把旧 Task 改成新代 Task。需要新 wire/state 时采用独立版本与 capability，旧 v1 冻结合同不偷改。
- [ ] 先写 `TestIncrementalEqualsFullRecompute`、`TestRuleChangeInvalidatesDependentNodes`、`TestReuseSurvivesRestartWithoutNewAttempt`、`TestReusePinsOriginArtifacts`；验证规则/模型/工具变更、条件分支、UNKNOWN、取消及缺失依赖。
- [ ] 接入 IU-2/3 的有效性和授权检查；全量重算作为明确回退。只复用能证明依赖未变的结果，最终 Goal 仍须对当前代输出独立验收。
- [ ] 对同一数据集运行全量与增量两臂：原订单 11000、改规则 10000、重复订单、不同客户、两跳反证。业务输出/验证结果必须一致；同时报告实际新 Task/Attempt/模型调用数和复用来源，不能只比耗时。
- [ ] 提交独立 PR。退出条件：结果一致、受影响节点没有漏算、未受影响节点确实减少执行；若无收益保留实验性并记录失败范围。

## Task 4 — IU-5A：token、语义上下文与回源

**Files:** 修改 `internal/graph/context.go`、`internal/worker/graph_execution.go` 和对应测试；新增 tokenizer Provider 契约及 `docs/superpowers/specs/iu-context-token-budget.md`。usage 对接复用[执行级 Token 计划](../../implementation/execution-token-observability-roadmap.md)，不创建独立计费账本。

**接口边界：** 拟议 `CountInput(ctx, modelID, tokenizerVersion, templateVersion, serializedInput) (count, coverage, error)`；声明计数覆盖的实际包装。Usage 是实际用量，Context 计数是输入预算，二者分别记录。自动回源由上层提出新的受限执行请求，不允许 Worker 私下访问未授权内容。

- [ ] 先冻结来源分区与上下文选择的区别、跨片段指代/全局规则依赖、回源协议和最大次数。保留原始 byte spans、role/order、原文 hash；摘要不是原文替代品。
- [ ] 先写 `TestTokenizerChangeRequiresRecount`、`TestRequiredContextCannotBeTruncated`、`TestCrossChunkReferenceNeedsEvidence`、`TestModelSwitchDoesNotReuseTokenIDs`；覆盖中文/emoji、重叠区间、附件摘要、计数器不可用与实际 Adapter 包装变化。
- [ ] 对附件/二进制单独设计不可变快照、授权读取、大小限制、来源引用和 GC pin；文本转换产物记录转换器版本，不把二进制当作可跨模型拼接的 token 序列。未支持的附件类型明确拒绝。
- [ ] 实现固定版本计数器和实际输入核对；无可靠计数器时继续 unknown 与字节硬上限。不能容纳必需信息时返回不足，进入有界回源/重新拆分/模型升级决策，禁止静默删规则。
- [ ] 验证原文重建完全一致、全局规则被保留、预算内实际输入与计数对象一致；精确 token 硬预算只对通过 contract test 的 Adapter 开放。
- [ ] 单独 PR 交付。退出条件：机制可审计、缺失语义显式未决；不宣称任意拆分均语义无损。

## Task 5 — IU-5B：独立 record 验证与概率契约

**Files:** 新增 `internal/graph/verification.go`、`internal/server/graph_verification.go` 及测试；接入 `graph_memory.go`、`graph_evaluation.go`、IU-2 事件和证据存储引用。

**接口边界：** 拟议 `VerificationReceipt` 绑定精确命题/record hash、来源/规则版本、验证器 profile/version、证据摘要、作用域和 PASS/FAIL/UNKNOWN。verified 只表示该作用域的验证结论，不等于全局真理。模型经验成功率来自外部 Evaluation 版本，不能写成 Hoare 确定性证明。

- [ ] 先冻结阶段设计 `docs/superpowers/specs/iu-record-verification.md`，明确验证主体、证据可取回性、信任/签名策略和撤销路径。首版使用管理员注册的只读验证器，不执行模型拼接命令。
- [ ] 先写 `TestVerificationCannotPromoteUncoveredRecords`、`TestVerificationRejectsStaleEvidence`、`TestVerifierTimeoutRemainsUnknown`：覆盖伪造回执、整批漂白、规则更新、跨代及权限撤销。
- [ ] 实现不可变验证回执和受限投影；验证只覆盖明确命题，证据失效通过 IU-2 传播。验证器不可用保持 UNKNOWN，不将执行成功自动等同业务 PASS。
- [ ] 用同任务的有/无独立验证对照测误接纳率、漏检率、额外调用/延迟及重试次数；保留失败样本，概率指标不提升为绝对保证。
- [ ] 提交独立 PR。退出条件：权限、命题作用域和失效联动通过，模型自身不能授予 verified。

## Task 6 — IU-5C：真实模型基线与 aicloud 接入

**Files:** 扩展 `internal/iuexperiment`、`cmd/iu-experiment`、真实实验说明；新增版本化 aicloud 适配器设计，不把 Registry/Router 实现迁入 computecloud。

- [ ] 冻结首批样本和模型/CLI/Adapter/提示词/采样设置、调用/时间/费用预算；先跑 20 个分层小样本用于流程验证，不据此声称统计显著或普遍收益。
- [ ] 对照组至少包含同模型单节点、同模型全量 IU、IU 去掉记忆/验证的消融组；IU-4 完成后加入增量 IU。较大模型可作独立基线，不能混用模型后将收益全部归因于拆分。
- [ ] 记录任务完成率、有效输出分母、原始字段保留、独立业务通过率、错误接纳、调用数、端到端 P50/P95、实际 usage 与费用来源；CLI/Provider 启动失败单独列出，不从失败样本中筛出“准确率”。
- [ ] 扩大正式评测前预注册非劣界限、成本目标、样本量和置信区间；费用口径包含失败、重试、检索和验证。达不到目标就记录适用边界或否定收益假设，不倒改目标。
- [ ] 冻结 `docs/superpowers/specs/iu-aicloud-adapter.md`：绑定 Registry/Evaluation/Router 的版本化决策结果，computecloud 冻结执行绑定并保留决策来源。覆盖路由不可用、模型下线、配置漂移、限流及预算不足；不绕过硬调度约束或执行中静默换模型。
- [ ] 实际执行经授权的实验并保存完整报告；接口契约通过后再分 PR 接入远程 aicloud。无服务访问或模型配置时仅交付设计与模拟，真实验收保持未完成。

## Task 7 — IU-6：跨主机、发布与副作用边界

**Files:** 扩展 `scripts/ci_task_flow.py`、`scripts/ci_iu_graph.py`；新增阶段操作手册，与[多节点验收计划](../../validation/multi-node-poc.md)及备份/升级说明关联。

- [ ] 冻结两台独立 Worker 主机的身份、版本、Runtime 与实验数据，覆盖真实网络、Server/Worker 崩溃、重复/迟到事件、离线后恢复、升级/回滚及目标长任务；同一 Runner 的两个进程不替代独立主机验收。
- [ ] 故障注入覆盖失效提交、导入、复用、Artifact 接受、准入和业务评估边界，核对 Task/Attempt/generation、receipt、Artifact pin 和审计记录，无半提交、重复执行或过期 PASS。
- [ ] 只读/纯计算先验收。工作区 patch DAG 另写冲突、基线 CAS、测试与合并合同；外部写入需先定义 effect ID、幂等键、outbox/补偿或人工批准，不能由恢复/重试推定外部 exactly-once。
- [ ] 升级前备份，关闭新图提交后已接受任务仍恢复/排空；不支持的协议或迁移明确拒绝。经目标环境验收后，才单独决定扩大开关、限制或发布范围。
- [ ] CI 必测名单缺失/SKIP 即失败；保存 SHA、环境、命令、原始样本及报告。认证、全量回归和打包/镜像门禁全部实际通过后再标记阶段完成。

## 执行与回填规则

- 本文的所有待办在实际交付前保持未勾选；新增阶段设计、测试和代码分别标明状态，不把文档通过当作功能验收。
- 每项实施遵循“失败测试 → 最小实现 → 定向验证 → 回归/CI → 独立审查 → 提交”的顺序，具体实现 PR 按子系统拆分。
- 每阶段回填[长期路线图](../../implementation/long-term-roadmap.md#iu-计算模型迭代台账)、[实现范围](../../implementation/iu-computation-model.md)、README 及证据链接；注明未通过、未测量和回退边界。
- 第一轮只启动 Task 1（IU-2）。Task 4 的 tokenizer 契约与 Task 6 的样本准备可并行设计；不得越过 IU-2/3 直接开放跨代复用或增量重算。
- 任意循环、通用 Workflow Engine、模型内部 token/KV 状态切分、通用长期记忆平台及不可回滚写操作不作为默认后续交付；确有需求时先独立立项验证。
