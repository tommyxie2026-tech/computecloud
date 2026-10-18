# IU Semantic Coverage and Bounded Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 IU 执行图上实现可检查的约束覆盖，以及可信 FAIL 后有界补证、Guard 发布和独立复验。

**Architecture:** 上层负责覆盖声明和受限计划构建；Server 保存可信回执并复查发布条件；现有 Goal/Plan/Task/Attempt/Guard 仍是唯一执行事实。新增 v5 图契约，不修改 v1～v4 的合同。首轮只对版本化结构化订单提供确定性覆盖检查，自然语言完整性保持 UNKNOWN。

**Tech Stack:** 现有 Go、JSON Schema、SQLite 迁移/事务、Server HTTP、Worker Runtime、Python CI gate；不增加第三方依赖。

**Spec:** [2026-10-18-iu-semantic-recovery-design.md](../specs/2026-10-18-iu-semantic-recovery-design.md)。用户已在规格提交 `6df3b4a` 后确认继续；本计划已由用户确认 Native 执行；Task 1～6 已有提交和定向验证，Task 7 的完整 CI 与独立审查仍待取证。

## Global Constraints

- 基线 main `156867bb0fa3d73a9c3217e816b758f2200b4528`；单分支 `feat/iu-semantic-recovery`、单集成 PR，不再建立一组堆叠 IU PR。
- 图请求 256 KiB、32 节点、128 边、并行度 8；Context 64 KiB；旧上限与默认 `graphs_enabled=false` 保持。
- catalog 至多 64 obligations，每条 spans/node IDs 各至多 32；source-read 每节点每代 2 次、每次 16 KiB；coverage receipt 每代至多 128。
- 自动修正每 Goal 最多 2 次，取原 max_replans/deadline/attempt/费用合同中的更严格限制；每提案至多 8 个读回执、合计至多 64 KiB，不能更改模型或凭据。
- UNKNOWN 不自动 Re-plan/PASS；不修改旧 Context/Attempt，不自动授权 import/reuse，不重开终态 Goal。
- 来源与输出 Artifact 证据必须可取回并 pin；hash 不替代授权、证据或业务检查。
- 真实 Provider/aicloud/独立主机保持 NOT_RUN；fixture 不证明模型质量、降本或独立主机恢复。
- 本计划中的新类型、文件和测试名是拟议接口，不暗示仓库已经存在这些实现。

## Review Focus

1. **最终金额正确但身份损坏**：逐字段和最终结果独立判分；Task 2/6 测试不能只断言总额。
2. **共享读取后换代或撤权**：发布事务重新核对精确来源和当前权限；Task 4/5 覆盖迟到响应与撤权。
3. **旧发布入口绕过修正限制**：v5 普通 propose-graph 不能跳过受限 diff/计数；Task 4 覆盖直接 HTTP 调用。
4. **提交成功但响应丢失**：已授权同键同内容重放返回相同 Job，不再消耗次数；Task 4/7 覆盖重启与并发。
5. **catalog 自身漏列或未知规则**：只验证已声明 span 不够；Task 2/3/6 从独立 GoalSpec 导出检查集合，缺项/未知不 PASS。

## 共享类型与决定

新包代码仍在 `internal/graph`；Server 专用认证/数据库事实不放入纯函数包。所有哈希采用现有 `graph.Hash` 和确定性 JSON 规范化；catalog 摘要计算时清空自身 SHA256 字段。拒绝重复 JSON key、未知字段、非法 UTF-8 与溢出整数。

- `CoverageCatalog{Version, GoalSourceID, GoalSpecHash, Profile, ProfileVersion, Scope, Obligations, Checks, SHA256}`；version=`iu-coverage-v1`。`GoalSourceID` 指向冻结 Sources 中完整的原始验收规格，hash 是其原始字节摘要，禁止仅凭客户端无原件的 hash 通过。
- `CoverageObligation{ID, Sources []Span, NodeIDs []string, CheckID string}`；`CoverageCheckBinding{ID string, Outputs []Output}` 引用已声明 Graph Outputs。每个 check 至少绑定一个输出；graph/node/output/check 身份均精确匹配。check/binding 各最多 32，引用沿用图总上限。
- `CoverageGap{ConstraintID, NodeID, SourceID, Start, End, Reason}`；`CoverageReport{Version, CatalogHash, Complete, Gaps}`。Complete 仅表示声明结构被覆盖。
- `CoverageVerifierProfile{Version, Kind, Scope, TimeoutMS}`；kind=`orders-v1` 或 `unavailable`，timeout 1～1000 ms。`CoverageCheckResult{ID, Verdict, Reason, EvidenceHash}`；`CoverageResult{Verdict, Checks, EvidenceHash}`。
- `RecoveryProposal{Version, ID, GoalID, PriorGeneration, PriorPlanHash, EvaluationID, EvaluationHash, CoverageReceiptID, CoverageReceiptHash, ReadReceiptIDs, ProposedSpec, SHA256}`，version=`iu-recovery-proposal-v1`。审批操作 ID 不属于不可变提案内容，沿用 Guard 一次性批准机制。
- 新 `Spec.Coverage *CoverageCatalog`、`frozenGraph.CoverageVerifier`、`GraphEvaluation.CoverageProof *GraphCoverageProofRef` 必须 `omitempty`，确保旧请求/冻结 hash 不变。v5 使用原 Worker 输入 v2，服务端完成覆盖编译后才激活；无新增 Worker wire 字段时不发明新 Worker capability。

## Task 1: v5 覆盖编译与发布前拒绝

**Files:** 新建 `api/graph/v5/schema.go`、`graph.schema.json`、`internal/graph/coverage.go`、`coverage_test.go`；修改 `internal/graph/spec.go`、`context.go`、`internal/server/graphs.go`、`cmd/computecloud/jobs.go`、`jobs_graph_test.go`。

**Interfaces:** `graph.CheckCoverage(s Spec) (CoverageReport, error)`；`graph.ValidateCoverage(s Spec) error` 在有 gaps 时返回可由 `errors.As` 取回报告的 `*CoverageError`；`graph.CoverageVersion="iu-graph-v5"`。v5 必须有 catalog；其他版本禁止该字段。`Compile` 校验实际 rules/spans 联合覆盖，并把必需来源合并到节点运行时 ContextPolicy 的验证，不默默补入原文。旧 v1～v4 的序列化与摘要不变化。

- [x] 写 `TestCoverageV5RejectsMissingReferencesAndSpans`、`TestCoveragePreflightReportsExactGap`：缺 source/node/check/输出、重复身份和 UTF-8 半字节拒绝；缺 span 的报告定位到具体 obligation/node；合法重叠覆盖通过，原文 hash/role/order 不变。
- [x] 写 `TestCoverageSubmitCreatesNoExecutionOnGap`：实际调用 SubmitGraph，gaps 后 jobs/goals/tasks/attempts 新增数均为 0；客户端 compile-graph 不访问网络，输出结构化报告并返回失败状态。
- [x] 写 `TestCoverageVersionPreservesLegacyPlanHashes`：冻结 v1～v4 已有 fixture/hash，不以运行时重新计算的期望掩盖漂移。运行 `go test ./internal/graph ./internal/server ./cmd/computecloud -run 'TestCoverage' -count=1`，先确认因缺少新接口/行为失败。
- [x] 实现以上接口与 v5 schema；沿用严格 Decode，避免为报告而允许正式提交绕过 Compile。发布前错误不得创建执行；catalog 中 profile/hash 由 Server freeze 与管理员配置再核对。
- [x] 重跑失败测试及 `go test ./internal/graph ./cmd/computecloud -count=1`，通过后提交 `feat(iu): compile versioned semantic coverage obligations`。

## Task 2: 独立订单域 checker，防止清单自证

**Files:** 新建 `internal/graph/coverage_verifier.go`、`coverage_verifier_test.go`、`examples/iu-graph/coverage-v5/` 下版本化 JSON fixtures；修改 `internal/config/jobs.go`，新建 `internal/config/graph_coverage_test.go`。

**Interfaces:** `graph.RunCoverageVerifier(ctx context.Context, p CoverageVerifierProfile, s Spec, values map[string]json.RawMessage) CoverageResult`。values key=`node_id/port`，须由调用者从本代已接纳产物装载；纯函数不做网络、文件或 DB 访问。

**冻结订单输入：** GoalSource 为严格 `iu-orders-goal-v1` JSON，字段包括 currency、unit、order_source_ids、rule_source_id、exception_source_ids、required_checks；unit 仅 `cent`，currency 为固定非空货币码且所有输入一致。订单包含 order_id/customer_id/paid_cents/is_special；规则包含 threshold_cents/discount_cents/max_discounts_per_customer=1；例外仅支持 `special_no_discount`。无例外时 exception_source_ids 为显式空数组。按同客户去重后计门槛；特价订单不参与门槛且不打折，仍计原价；每客户普通订单优惠最多一次，应付不小于 0。非负 int64 精确计算，累加溢出 UNKNOWN。

**固定 check IDs：** `source_identity`、`source_fields`、`customer_grouping`、`threshold_once`、`exception_rules`、`currency_unit`。catalog、独立 GoalSource 与 checker 实际支持的集合必须完全一致；无法建立原件/提取字段/总额的证据映射时 UNKNOWN。GoalSource 中的未知字段、未知规则类型或自然语言未解条件不能忽略。

- [x] 写 `TestCoverageCheckerRejectsOmittedExceptionAndWrongCatalog`、`TestCoverageCheckerChecksFieldsDespiteCorrectTotal`：漏 exception check/来源或损坏 customer/order ID 时不能 PASS；两个 6000 的同客户普通订单优惠 1000 后为 11000，客户分开为 12000；一笔特价一笔普通仍为 12000。
- [x] 写 `TestCoverageCheckerUnknownInputAndOverflow`：缺币种/单位、未解引用、重复 ID 冲突、未知规则、重复 key、1.0 充当整数、int64 累加溢出、不可用/取消/超时都 UNKNOWN；错误总额且原始输入完整则 FAIL；相同订单重复不重复计费。
- [x] 先运行 `go test ./internal/graph ./internal/config -run 'TestCoverage' -count=1` 观察失败，再实现只读 checker 与 profile 校验；不要调用现有实验工具的隐藏 oracle 作为运行时输入。
- [x] 重跑测试通过后提交 `feat(iu): verify bounded order coverage from original evidence`。

## Task 3: 持久覆盖回执与 v5 Goal PASS 门禁

**Files:** 新建 `internal/server/graph_coverage.go`、`graph_coverage_test.go`、`internal/store/graph_coverage_migration.go`、`graph_coverage_migration_test.go`；修改 `internal/store/migrations.go`、`db.go`、`iu_release_upgrade_test.go`、`internal/server/graphs.go`、`graph_evaluation.go`、`http.go`。

**Interfaces:** `Server.VerifyGraphCoverage(ctx context.Context, jobID string, in GraphCoverageRequest) (*GraphCoverageReceipt,error)`；`Server.GraphCoverageReceipt(ctx context.Context, jobID, receiptID string) (*GraphCoverageReceipt,error)`；事务内 `validateGraphCoverageProofTx(ctx context.Context,q store.Query,f *frozenGraph,in GraphEvaluation) error`。

`GraphCoverageRequest{ID, ExpectedValidityRevision, OutputHash, Profile, ProfileVersion}` 不接受 verdict。Receipt 包含 spec 规定的全部绑定、检查结果、profile 原定义/hash、来源/产物清单、elapsed_us 和内容摘要。`GraphCoverageProofRef{ReceiptID, SHA256}` 只引用 Server 事实。

HTTP：`POST /v1/jobs/{id}/graph/coverage-verifications` 与 `GET /v1/jobs/{id}/graph/coverage-verifications/{receipt}`。POST 请求上限 4096 bytes，GET/POST 沿用项目/owner 与 managed key 事务复查；POST 同时需 jobs:read/goals:evaluate。必须是终结的成功 Job、当前待评估代，读取精确已接纳输出。卸载 profile 的新请求返回有原因的 UNKNOWN；旧 PASS 证据在 profile 漂移后不能继续适用。

**迁移：** 当前已核实 Server v25/Worker v6；实现前再核对 main，若仍 v25 则追加 serverV26，不改旧迁移 SQL。新建不可变 `iu_coverage_receipts(goal_id,generation,receipt_id,request_hash,request,body,actor,created)`，复合主键及对 iu_graphs 的命名空间外键；同一迁移为 Task 4 建表，避免修改已经执行的迁移。恢复旧快照是回滚方案，不做有损 down migration。

- [x] 写 `TestCoverageReceiptRequiresIndependentAuthorityAndPinsEvidence`、`TestCoveragePassRequiresCurrentCompleteReceipt`：伪造 verdict/hash、漏 check、旧代、错误 output、过期/profile 修订/撤权/新失效 revision 均不能 PASS；普通提交者不能出具回执；失败的提交不遗留 pin；128 条后拒绝新 receipt。
- [x] 写 `TestCoverageReceiptCannotLaunderUnknownOrCorrectTotal`：Task 2 的原字段损坏不能被最终总额或原 record_proofs 漂白；UNKNOWN 不变成 PASS。系统执行失败的内部 FAIL 仍可记录，不要求不存在的完整输出回执。
- [x] 写 `TestCoverageMigrationAtomicUpgradeAndSnapshotRollback`，更新旧 v20/v25 fixture 删除新增表及预期最新 schema；验证失败回滚、重复 Open、未来版本拒绝和 Artifact 保留。先跑 `go test ./internal/store ./internal/server -run 'TestCoverage' -count=1` 观察失败。
- [x] 实现事务外有界检查、事务内重查/不可变写入/pin；幂等相同请求回同事实、异内容拒绝；v5 PASS 强制完整可信 coverage proof，v1～v4 不新增强制字段。
- [x] 定向测试及 `go test ./internal/store -count=1` 通过后提交 `feat(iu): persist coverage receipts and fence goal acceptance`。

## Task 4: 修正提案与 Guard 原子发布

**Files:** 新建 `internal/graph/recovery.go`、`recovery_test.go`、`internal/server/graph_recovery.go`、`graph_recovery_test.go`；修改 `internal/server/graph_replan.go`、`goal_publication.go`、`http.go`；Task 3 迁移同时预建本节两表。

**Interfaces:** `graph.ValidateRecovery(prior,next Spec) error`；`Server.RecoverGraph(ctx context.Context,priorJobID string,in graph.RecoveryProposal,approvalOperationID string) (*Job,goal.ReplanDecision,error)`。重用现有 graphEvaluationFact、PlanCanonical 和策略进度结构，Server 从可信事实生成 Guard 请求，不信任客户端自报 Progress。AcceptedChecks/FailedChecks/UnknownChecks 分别取当前适用回执的三值计数，ResolvedAssumptions=0，UnresolvedBlockers=FailedChecks+UnknownChecks；不能用尚未执行的新计划声称已取得进展。StrategyDelta 仅记录实际增加的 evidence_spans/instructions，JobSpecHash 为新计划 Compile 的 Digest。

限定 next 相对 prior：Sources/Coverage/Operators/Schemas/Edges/Outputs/Workspace/Imports/Reuse/Limits/节点身份与执行绑定完全相同；只允许 rules/spans 增大覆盖、Instructions 保留原前缀再追加。追加文字每节点至多 8 KiB 且仍受原总大小限制；去掉旧 span、删规则、改变模型/超时/成本/replay profile 均拒绝。必须有实际变化，且新增 spans 属于该 node 的指定读回执；不能拿相同全文引用伪装已取回其他范围。

HTTP `POST /v1/jobs/{id}/graph/recovery-proposals`，body 为 proposal 与单独 approval_operation_id；整个 body 最多 320 KiB，内层 ProposedSpec 保持 256 KiB。需 jobs:read/jobs:submit/goals:propose，沿用 propose-graph 的控制租约校验；Server 核对当前可信 FAIL evaluation 指向同代适用的 FAIL coverage receipt。UNKNOWN、无完整输出的纯执行失败或陈旧评估不进入本轮自动修正。

事实表：`iu_recovery_proposals(goal_id,proposal_id,request_hash,request,body,actor,created)` 不可变、每 Goal 最多 128 条；`iu_recovery_publications(goal_id,proposal_id,prior_generation,next_generation,job_id,created)` 不可变，主键 goal/proposal，唯一 goal/prior_generation 与 next Job。均建立 Goal/代/提案对应外键。成功次数取 publication 事实计数，不另设可漂移的计数器。

先持久化请求事实以支持等待审批；新代 Job/Plan/代关系、publication 事实和 Guard 计数在同一 Guard 发布事务提交。旧 publishGraphReplan 的 v5 路径只能由已经校验的内部恢复函数调用；普通 PublishGoalReplan/v5 请求返回 `V5_RECOVERY_PROPOSAL_REQUIRED`，v1～v4 保持原行为。审阅后改变 catalog/Goal 的手工路径不在本轮实现，不默默降级。

- [x] 写 `TestRecoveryRejectsUnauthorizedDiffAndOrdinaryReplanBypass`、`TestRecoveryRequiresTrustedFailAndMatchingReads`：直接 HTTP、伪造 progress、跨节点/代/计划读回执、模型变更、同 hash 不同 source 身份、删除约束均拒绝；UNKNOWN 无新 Task/代。
- [x] 写 `TestRecoveryPublicationAtomicAndIdempotent`、`TestRecoveryConcurrentProposalAndLostResponse`：提交前故障全部回滚；提交后重启并重放返回同 Job；两个并发提案最多一个发布；同 ID 异内容拒绝；授权已撤销时即使重放也不能访问结果。
- [x] 写 `TestRecoveryBudgetsApprovalAndDrain`：成功两次后换 ID 也拒绝第三次；更低 max_replans/deadline 先限制；8 回执/64 KiB 上限；活动 Attempt 未排空拒绝；NEEDS_APPROVAL/REJECT 不创建 Job、不消费 publication 次数；审批后同提案仅发布一次。
- [x] 先跑 `go test ./internal/graph ./internal/server -run 'TestRecovery' -count=1` 观察失败，再实现最小改动；不复制 Guard 算法，不在 SQL 事务中调用网络或模型。
- [x] `go test -race ./internal/graph ./internal/server -run 'Test(Recovery|GraphBusinessFailureCanPublishGuardedNewGeneration)' -count=1` 通过后提交 `feat(iu): publish bounded evidence recovery through existing guard`。

## Task 5: CLI 取证与可重放的一次性编排

**Files:** 新建 `cmd/computecloud/jobs_recovery.go`、`jobs_recovery_test.go`；修改 `cmd/computecloud/main.go`、`jobs.go`、`internal/server/graph_source_read.go`、`graph_source_read_test.go`、`http.go`。

**Interfaces:** `job verify-coverage --id JOB --file request.json`、`job coverage --id JOB --key RECEIPT`、`job recover-graph --id JOB --file recipe.json`。recipe 绑定固定 proposal ID、预期 prior Plan/evaluation、所需 node/spans 与追加指令；CLI 从 Server 状态与可信回执构建完整 proposal，不在本地自行签发证据或变更 verdict。

新增 `GET /v1/jobs/{id}/graph/source-reads/{node}`，经 jobs:read/项目/owner 权限返回此 node 的已用次数与最多两条回执，提供 CLI 的 ExpectedReads CAS 与稳定请求 ID 恢复依据。单节点响应有界，不增加无分页的全图正文导出。新读取的 ID 由 proposal ID/node/span 摘要稳定派生且满足已有 ValidKey 上限；CAS 冲突返回可重试诊断，不私自创建新 ID 消耗次数。

recover-graph 顺序为：获取状态及匹配的 coverage receipt → 检查可信 FAIL → 查询/执行幂等 source-read → 纯函数构建 next/重新 Compile → 获取既有控制租约 → 发送恢复提案 → 释放租约并返回 job/decision。超时、取消、NEEDS_APPROVAL、UNKNOWN 立即返回；没有隐藏循环、自动评估身份切换或无限重试。最终复验仍需独立评估身份显式 verify-coverage/evaluate-graph。

- [x] 写 `TestRecoveryCLIKeepsStableReadsAcrossRestart`、`TestRecoveryCLIUnknownNeverReadsOrPublishes`、`TestRecoveryCLIReleasesLeaseAndStopsOnCAS`：httptest 核对请求内容/顺序、读回执复用、CAS 冲突停止、租约释放、取消；不把两轮 shell 演示当原生执行事实。
- [x] 写 `TestRecoverySourceReadQueryRequiresCurrentAuthorization`：换 node/项目/代与撤销 managed key 不能取得正文；多次查询不增加 read count。
- [x] 先跑 `go test ./cmd/computecloud ./internal/server -run 'TestRecovery(CLI|Source)' -count=1` 观察失败，再实现并重跑；通过后提交 `feat(cli): orchestrate bounded IU evidence recovery`。

## Task 6: 版本化四臂反例与报告

**Files:** 新建 `internal/iuexperiment/coverage_protocol.go`、`coverage_protocol_test.go`、`coverage_simulation.go`、`coverage_simulation_test.go`；修改 `cmd/iu-experiment/main.go`、`main_test.go`；测试数据放 `internal/iuexperiment/testdata/coverage-v1/`。

**Interfaces:** `NewCoverageProtocol(model,runtime,cliVersion,effort string) (CoverageProtocol,error)`；`RunCoverageProtocol(ctx context.Context,p CoverageProtocol,call Call) (CoverageExperimentReport,error)`，复用 `experiment.go` 中现有 `Call func(context.Context, graph.ContextManifest) (Reply,error)`。新增 CLI `--coverage-plan` / `--coverage-protocol FILE`，与原 pilot 参数互斥；`--simulate` 可运行新协议。旧 pilot v1 数据集和 215 次 fixture 断言不改写。

协议固定规格第 7 节六个场景，每场景三个变体：正确、可定位违约、不确定；每变体四臂共 72 个结果。冻结全部数据、模型声明、Adapter/prompt、超时与调用 cap，逐臂/逐请求预检，不从错误样本过滤分母。调用 cap 从实际分片数和最多两次修正明确计算并进入协议 hash；真实运行缺少 cap 或模型版本绑定拒绝启动。

各臂共享原始可见输入、同模型与预算；隐藏 oracle 仅在输出后判分。运行时校验器移除只发生在无验证 Harness 臂，不能调用生产 v5 接口绕过 PASS 门禁。补证前后分别报原字段保留与业务判断；error/not_attempted/UNKNOWN、验证耗时、读取字节及所有调用成本分开。未知 token/费用 null；Provider 调用与 fixture invocation 明确区分。真实 Protocol 入口只交付代码，不自动调用真实 Provider。

- [x] 写 `TestCoverageProtocolPinsCasesAndAllArmDenominators`、`TestCoverageAblationsExposeFalseAcceptance`、`TestCoverageReportKeepsUnknownAndFailedRecovery`：72 条不缺臂；注入坏客户 ID 但碰巧正确总额仍失败；有/无验证的 pipeline acceptance 与独立 ground truth 区分；取消/预算耗尽仍保留样本，成本未知不判降本达标。
- [x] 写 `TestCoverageSimulationDoesNotLeakOracleToCalls`：捕获实际 prompt/上下文，隐藏答案不在调用输入；所有失败模式实际由 fixture 注入并可被相关检查定位，不仅硬编码 report 字段。
- [x] 先跑 `go test ./internal/iuexperiment ./cmd/iu-experiment -run 'TestCoverage' -count=1` 观察失败，再实现；与原协议全量回归均通过后提交 `test(iu): compare coverage recovery with four controlled arms`。

## Task 7: 原生故障门禁、文档与单 PR 交付

**Files:** 新建 `internal/server/graph_coverage_flow_test.go`；修改 `scripts/ci_task_flow.py`、`scripts/ci_iu_graph.py`、`scripts/test_ci_iu_graph.py`、`.github/workflows/ci.yml`（仅在现有 gate 不能覆盖新增流程时）、`docs/validation/iu-ci-simulation.md`、`docs/operations/iu-controlled-rollout.md`、`docs/implementation/iu-computation-model.md`、`long-term-roadmap.md`、README；新建 `docs/contracts/iu-graph-v5.md` 与示例操作说明。

- [ ] 写 `TestCoverageNativeFailReadReplanPassAndFrozenHistory`：真实 Server/两 Worker fixture，初代错误 → checker FAIL → 有界回源 → Guard → 新代正确 → 独立 PASS。核对真实 Task/Attempt/代、来源 pin、前后 Context hash，禁止客户端生成假 Attempt。
- [ ] 写 `TestRecoveryServerRestartDoesNotResetLimits` 与原生进程提交前/提交后故障用例；重启 Server、CLI 丢响应、关闭新图提交后的恢复/排空均保留事实，不放宽禁用新发布的配置。现有 v20 快照验收与新 v25 快照同时保留。
- [ ] gate 必测名单加入所有新增关键测试；缺失/SKIP/空 JSONL 均失败。新 72 条四臂报告与旧 80 条 pilot 分开检验；Provider=0；自然 UNKNOWN 可以是预期结果，不能当被跳过。报告同时保留测试 PASS 与业务 FAIL/UNKNOWN，不能混为一个成功计数。
- [ ] 本机运行 `go test -race ./internal/graph ./internal/store ./internal/server ./internal/worker ./internal/iuexperiment ./internal/aicloudadapter ./cmd/computecloud ./cmd/iu-experiment -count=1`、Python gate 测试、`go vet ./...`；原生子进程遇桌面环境限制明确记录，以实际 CI 门禁取证，不把局部编译当全绿。
- [ ] 用失败复现及补丁差异做全分支独立审查；修复已确认问题，重复相关验证。文档更新实际版本、接口、拒绝原因、未测范围与精确 SHA/CI，不提前勾选本计划或标阶段完成。
- [ ] 单分支提交并创建一个集成 PR，附加到当前聊天；当前 head 的常规 CI、IU gate、Linux/macOS/打包/镜像所需检查通过后报告可合并状态。未经本轮明确授权不自行合并或扩大默认开关，不产生一组未清理的阶段 PR。

## 计划自检与执行交接

规格 4→Task 1/2，规格 5→Task 2/3，规格 6→Task 4/5，规格 7→Task 6，规格 8/9→Task 7。Review Focus 五类均落入命名测试；旧 hash、审批、幂等、回执 pin、全局次数与 UNKNOWN 路径有独立断言。规格第 10 节的未支持项不以 stub 通过或 fixture 假装完成。

建议 **Native**：由当前会话按任务依次实施，因覆盖类型、回执与发布事务依赖紧密，先保持接口一致；最后由独立审查者检查整个分支。也可选择逐任务子代理实施/复核，但增加上下文交接成本。用户已确认继续，采用 Native 执行；完成前仍须取得本计划要求的独立审查和 CI 证据。
