# IU Memory Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 实现M1记忆契约、固定视图、持久接纳与授权读取基础层，为后续图驱动完整闭环提供可信输入。

**Architecture:** 复用现有Task/Attempt和Artifact来源；以纯memory契约库和Server内部接纳service建立不可变记录。M1不开放任意客户端写记忆，不改Job v0.2执行图；M2/M3另计划接入实际Worker、依赖调度和业务验收。

**Tech Stack:** Go 1.27.2+、现有SQLite/store事务、现有JSON/HTTP与Server fixtures；不新增依赖。

**Spec:** ../specs/2026-10-17-iu-memory-foundation-design.md

## Global Constraints

- 功能默认关闭，旧Job/Artifact/API行为保持。
- Delta≤64KiB、records≤32、payload≤8KiB、source_ids/depends_on各≤64。
- ID 1..128可打印ASCII且不含空白；hash小写SHA256。
- 内容validation仅unchecked；接纳不等于事实正确。
- Source只认当前Goal/Plan/Graph、成功已released Attempt及ACCEPTED Artifact。
- 必需记忆缺失/重复/冲突拒绝，绝不静默截断或读取latest。
- 原子写records/receipt/view与Artifact pin；不改变Task状态。
- 默认本会话直接执行；未获用户明确委派前不使用子代理。

## Review Focus

- 异常JSON：重复键、非法UTF-8、超限不能进入hash或存储（Task 1）。
- 并发重放：同接纳键同hash只一份，不同hash拒绝，无半提交（Task 3）。
- 记忆用途：unchecked不能被传成supported；缺必需项不应合成（Task 2）。
- 租户/版本：猜到ID也不能读他人或过期图的有效视图（Task 3/4）。
- 恢复：已完成Task在接纳失败或重启后不重跑（Task 5）。

---

### Task 1: 版本化记忆契约与确定性hash

**Files:** Create internal/memory/contract.go, canonical.go, contract_test.go, canonical_test.go.

**Interfaces:**
- Record, View, Delta按Spec字段及json tag定义。
- DecodeDelta([]byte) (Delta,error)
- CanonicalDelta(Delta) ([]byte,error)
- HashDelta(Delta) (string,error)

- [ ] 写TestDecodeDeltaRejectsDuplicateKeysUnknownFieldsInvalidUTF8AndLimits，逐项验证限制。
- [ ] 写TestDeltaCanonicalSetsAreOrderIndependentButPayloadPreserved；集合排序后hash一致，payload原始Unicode/数字词法保持；重复ID/source拒绝。
- [ ] 写TestDeltaRejectsSupportedWithoutTrustedEvidence；仅允许unchecked。
- [ ] Run go test ./internal/memory -run 'TestDecodeDelta|TestDelta' -count=1；先确认缺实现失败。
- [ ] 实现上述类型与接口，复用jsonutil，不添加第三方canonical库。
- [ ] 重跑通过，git diff --check，提交feat(memory): define bounded immutable contracts。

### Task 2: 固定View与可观测输入编译

**Files:** Create internal/memory/view.go, context.go, view_test.go, context_test.go.

**Interfaces:**
- BuildView(View, map[string]Record) (ResolvedView,error)
- ResolvedView含Hash、Records（稳定ID排序）、ReadSet。
- CompileContext(goal string, local string, view ResolvedView, maxBytes int) (CompiledContext,error)
- CompiledContext含Text、SHA256、ReadSet、ByteLength；Token计量不编造。

- [ ] 写TestBuildViewRejectsMissingRequiredConflictingAndDuplicateRecords。
- [ ] 写TestCompileContextPreservesUnicodeRulesAndReadSet：后文例外完整保留，原字符串不归一化。
- [ ] 写TestCompileContextRejectsOverflowWithoutTruncation及TestAuxiliaryRecordCannotOverrideRequiredRule。
- [ ] Run go test ./internal/memory -run 'TestBuildView|TestCompileContext|TestAuxiliary' -count=1，先红后绿。
- [ ] 实现明确分节且不可由payload伪造边界的长度前缀序列化；不修改Template/Tokenizer或假设完整内部Token可见。
- [ ] 验证contract与context测试通过，提交feat(memory): compile fixed read views。

### Task 3: Server存储迁移与内部幂等接纳

**Files:** Create internal/store/memory_migration.go, memory_migration_test.go; modify internal/store/db.go, migrations.go; create internal/server/memory_projection.go, memory_projection_test.go.

**Interfaces:**
- Server.AdmitMemoryProjection(ctx context.Context, jobID, artifactID, outputPort string, deltaBytes []byte, expectedViewHash string) (MemoryReceipt,error)
- MemoryReceipt含ID、DeltaHash、Source身份、PlanRevision/GraphGeneration与Created。
- Server.CreateMemoryView(ctx context.Context, jobID string, definition memory.View) (memory.ResolvedView,error)
- 无公开写HTTP入口；生产触发器留给M2，不在M1声称已有自动projection。

- [ ] 用现有Server Job/Attempt fixture建立真实ACCEPTED Artifact与Goal绑定；不要仅伪造来源布尔值。
- [ ] 写TestMemoryAdmitOnlyCurrentSuccessfulSource、TestMemoryAdmitRejectsOrphanFailedStaleAndWrongScope。
- [ ] 写TestMemoryAdmissionReplayReturnsSameReceiptAndConflictRollsBack，注入写中断确认无receipt/record/pin残留。
- [ ] 写TestMemoryConcurrentAdmissionHasOneReceipt：同内容并发重放不重复；不同内容冲突。
- [ ] 写TestMemoryMigrationFromV19AndImmutableRows，检查外键与artifact_refs pin。
- [ ] Run go test ./internal/store ./internal/server -run 'TestMemory' -count=1，先确认失败。
- [ ] 事务外有界解析，事务内重新查真实执行来源与当前generation；添加表和不可变约束，复用store.Tx与pinArtifactRef。
- [ ] View通过确切receipt集合构建并验证依赖，无latest，无跨Goal隐式导入。
- [ ] 测试通过后提交feat(memory): persist admission receipts and fixed views。

### Task 4: 默认关闭与只读HTTP接口

**Files:** Modify internal/config/config.go, jobs.go; create internal/server/memory_http.go, memory_http_test.go; modify internal/server/http.go; extend internal/config tests.

**Interfaces:**
- 新Server配置memory_computation_enabled，默认false。
- GET /v1/jobs/{id}/memory/receipts/{receipt}
- GET /v1/jobs/{id}/memory/views/{view_hash}
- 复用jobs:read及Job owner/project授权；功能关闭返回明确不可用，不泄漏记录是否存在。

- [ ] 写TestMemoryDisabledDoesNotChangeLegacyJobs。
- [ ] 写TestMemoryReadRejectsCrossOwnerProjectGoalAndGuessedIDs。
- [ ] 写TestMemoryReadsReturnFixedViewAndUncheckedStatus；内容/来源准确，不能提升验证状态。
- [ ] go test ./internal/config ./internal/server -run 'TestMemory' -count=1，先红后绿。
- [ ] 接入路由和配置验证，仅授权读取；不增加写权限或共享数据库旁路。
- [ ] 相邻Job/Artifact/Goal测试通过，提交feat(memory): expose scoped read receipts。

### Task 5: 重启与完整M1验收

**Files:** Create internal/server/memory_recovery_test.go, scripts/ci_memory_foundation.py; modify Makefile; add docs/contracts/memory-foundation-v1.md and examples/memory-foundation/README.md.

**Interfaces:** make ci-memory-foundation；脚本使用固定fixtures与Server服务测试，不调用真实模型。

- [ ] 写TestMemoryAdmissionSurvivesRestartAndDoesNotCreateAttempts。
- [ ] 写TestMemoryAcceptedSourceWithoutReceiptCanBeAdmittedAfterRestart，明确需显式重试service而非已实现自动扫描。
- [ ] 写TestMemoryViewRequiresAllReceiptsAndPreservesBranchRecords。
- [ ] 写客户两笔6000分聚合后应用一次1000分优惠的纯库示例，预期11000分；重复来源拒绝。
- [ ] Run go test ./internal/memory ./internal/store ./internal/server -run 'TestMemory|TestCustomer' -count=1，先红后绿。
- [ ] 编写fixture脚本/文档，明确此交付是基础层，不是M2图或真实Provider认证。
- [ ] Run make test；运行新增make ci-memory-foundation；Run go test -race ./internal/memory ./internal/server -run 'TestMemory' -count=1。
- [ ] 新migration涉及执行数据时运行现有store upgrade、Artifact、Goal与Retry相关测试，并按仓库要求补make smoke。
- [ ] 记录实际执行结果和限制，提交test(memory): verify durable admission foundation。

## M2/M3后续依赖

M2评审后另写计划：Job graph v0.3、按边输入绑定、Worker delta输出/投影触发器、可信command、等待receipt后激活与自动reconcile。M3再接版本化业务Evaluation、Guard图修订、显式历史导入、并行预算。第一批不能偷用任意客户端新建Job绕过Guard来宣称完整闭环。

## 本次准备状态

- 远端及隔离本地分支已创建，基于main 34eeace。
- 原始本地仓库dev分支未改动；独立副本位于本会话work/computecloud-iu。
- Go 1.27.2工具链下载遇到proxy.golang.org TLS连接错误并已停止；未运行基线，未降低版本要求。
- 尚未写产品代码或迁移。本计划待用户审核后才执行。
