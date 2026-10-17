# IU Computation Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 Task/Attempt 上实现可恢复的有限 IU 执行图及上下文、记忆和评估闭环。

**Architecture:** 纯契约层负责图编译、上下文和记忆投影；SQLite 存储不可变图、节点激活和准入记录。Server 控制图就绪并复用 Scheduler，Worker 通过版本化输入/输出契约执行，最终评估通过现有 Guard 发布新代。

**Tech Stack:** Go 1.27.2+、SQLite、现有 gRPC/protobuf、JSON Schema；不新增外部服务。

**Spec:** `docs/superpowers/specs/2026-10-17-iu-computation-model-design.md`，含 R1–R5 修订。

## Global Constraints

- 当前分支 feat/iu-computation-model；旧 Job v0.2 和 v1 Worker 保持兼容。
- 图请求 256 KiB、32 节点、128 边、并行度 8；编译上下文最多 64 KiB UTF-8 字节。
- delta 最多 64 KiB、32 条记录、单 payload 8 KiB、单记录最多 64 source refs。
- 所有图/节点/准入身份包含 goal_id 与 graph_generation；项目授权不能由摘要替代。
- 默认关闭图提交；已接受图仍恢复/排空；不变更 readiness 评分默认值。
- 成本预算仅 claude_http 串行；未知 usage 禁止继续派发；max_tokens 首版拒绝。
- 不暴露隐藏思维链，不实现不可回滚外部副作用，不宣称语义无损或供应商账单硬上限。

## Review Focus

- 多 Goal 同 generation/节点名不会串线（任务 3/4）。
- 有效性 VALID 与验证 verified 正交，规则改变不能复用陈旧结论（任务 2/5）。
- PENDING 候选拒绝后可终结；冲突请求不毒化已有 Receipt（任务 5）。
- 三值条件未选择分支必须 SKIPPED，缺失 UNKNOWN 处理不能悬挂（任务 1/4）。
- legacy evaluator、读取投影与周期恢复不能提前终结图 Goal（任务 6）。

---

### Task 1: 有限图契约与编译

**Files:** Create `api/graph/v1/graph.schema.json`, `api/graph/v1/schema.go`, `internal/graph/spec.go`, `internal/graph/spec_test.go`。

**Interfaces:** `Decode([]byte) (Spec,error)` 严格解码；`Compile(Spec) (Plan,error)` 返回确定拓扑顺序与摘要；`Lower(job.Spec) (Spec,error)` 仅转换旧图形状。Spec 包含 Sources/Rules/Operators/Nodes/Edges/Outputs/Limits，端口在冻结图内按节点/端口符号引用，在激活时解析为实际产物。

- [ ] Step 1: 写 `TestCompileDAG`、`TestRejectCycleAndPortMismatch`、`TestGraphLimitsAndUnknownFields`、`TestLowerLegacy`；断言中文源保留、拓扑稳定、环/重复/越界/非法条件拒绝、旧 single/maps→reduce 形状一致。
- [ ] Step 2: `go test ./internal/graph -count=1`；先观察缺实现失败。
- [ ] Step 3: 实现上述接口；依赖必须存在、schema 必须匹配、条件仅 PASS/FAIL/UNKNOWN，所有输出可达，纯编译不创建 Task。
- [ ] Step 4: 同命令 PASS；跑 legacy job 包回归。
- [ ] Step 5: 提交图契约和测试。

### Task 2: 源快照、上下文和纯记忆投影

**Files:** Create `internal/graph/context.go`, `internal/graph/memory.go` 及各自 `_test.go`。

**Interfaces:** `CompileContext(ContextRequest) (ContextManifest,error)`；`ValidateDelta([]byte, MemoryView) (Delta,error)`；`MergeViews([]MemoryView) (MemoryView,error)`；`Invalidate(MemoryView, []string) (MemoryView,error)`。Manifest 保存实际输入与 hash、原始来源映射、token coverage=unknown；read set 固定并包含完整实际输入依赖。View、Record、Delta 均有版本化内容摘要。

- [ ] Step 1: 写 UTF-8 边界、覆盖/重叠源重建、规则遗漏、超 65536 字节、错 read-view、32/33 条、重复 source、冲突和两跳失效测试；VALID 不自动改 unchecked。
- [ ] Step 2: 运行 `go test ./internal/graph -count=1` 观察新增失败。
- [ ] Step 3: 实现结构化上下文与确定性投影；禁止截断，失效追加投影而非改历史，未知 token 数不能冒充测量。
- [ ] Step 4: 同命令 PASS，确认合法源重建摘要一致。
- [ ] Step 5: 提交上下文与记忆契约。

### Task 3: 持久化和默认关闭的提交/读取 API

**Files:** Create `internal/store/graph_migration.go`, `internal/store/graph_migration_test.go`, `internal/server/graphs.go`, `internal/server/graphs_test.go`; Modify `internal/store/migrations.go`, `internal/store/db.go`, `internal/config/jobs.go`, `internal/server/http.go`, `internal/server/jobs.go`, `internal/server/goal_execution.go`。

**Interfaces:** `SubmitGraph(ctx,key,raw) (*Job,error)`、`GraphStatus(ctx,jobID) (*GraphStatus,error)`；新增冻结图记录与 `(goal_id,generation,node_id)` 节点主键，所有外键指向同一图。复用原 Job ID/鉴权/模板冻结、队列准入/trace/幂等；图模式的 readJob 不解码为 legacy Spec。

- [ ] Step 1: 写真实 SQLite v19 升级、重开、双 Goal 同名、默认关闭、跨项目/凭据拒绝、同键同/异摘要测试。
- [ ] Step 2: `go test ./internal/store ./internal/server ./internal/config -run 'TestGraph|TestIU' -count=1` 观察失败。
- [ ] Step 3: 分配下一个空闲迁移版本；实现提交/查询和 legacy 分流，不在此派发未就绪节点。
- [ ] Step 4: 同命令 PASS，旧迁移/Job 测试通过。
- [ ] Step 5: 提交存储和 API。

### Task 4: 图控制器与 Worker v2 通路

**Files:** Create `internal/server/graph_execution.go`, `internal/server/graph_execution_test.go`, `internal/worker/graph_execution.go`, `internal/worker/graph_execution_test.go`; Modify `api/agent/v1/runtime.proto` 及生成文件、`internal/server/jobs.go`, `internal/server/server.go`, `internal/server/job_inputs.go`, `internal/worker/execute.go`, `internal/worker/jobs.go`, `internal/config/jobs.go`。

**Interfaces:** `advanceGraph(ctx,jobID) error`、`assignmentGraph(ctx,q,taskID) (*pb.JobExecution,error)`；v2 图描述包含冻结 Node/ContextManifest/输入绑定。现有节点激活事务创建 Task 并绑定；join 不生成 Task；command profile 只选固定程序/校验参数，经现有环境生命周期执行。

- [ ] Step 1: 写两层并行→join→验证路径、条件三分支、SKIPPED join、激活崩溃重放、v1 Worker 资格拒绝、固定输入下载权限、command 隔离/取消和成本串行竞争测试。
- [ ] Step 2: 定向运行 server/worker 图测试观察失败。
- [ ] Step 3: 实现原子激活/节点投影、周期推进、v2 capability/bundle 协商和实际输入执行；继续复用原 Scheduler 硬过滤及 Attempt fencing。
- [ ] Step 4: 定向 PASS，并跑原 single/map_reduce/Worker/retry 回归。
- [ ] Step 5: 提交通路实现。

### Task 5: Artifact 驱动的记忆准入与恢复

**Files:** Create `internal/server/graph_memory.go`, `internal/server/graph_memory_test.go`; Modify `internal/server/results.go`, `internal/server/artifact_lifecycle.go`, `internal/server/graph_execution.go`。

**Interfaces:** `reconcileGraphMemory(ctx) error`；Artifact 接受事务写 PENDING+pin；事务外有界解析后事务内校验并提交 View/Receipt；ADMITTED 才就绪，REJECTED 驱动排空/失败评估。持久失效事件与授权 read set 导入在准入/激活/评估时统一检查。

- [ ] Step 1: 写接受后崩溃、准入提交后重放、hash/来源拒绝、同键冲突不毒化、临时异常退避/deadline、规则变更及跨代 import 测试。
- [ ] Step 2: 定向图记忆测试观察失败。
- [ ] Step 3: 实现三态准入、重试时间、永久拒绝终结、pin 生命周期和显式 join；不重跑已完成 Task。
- [ ] Step 4: 定向 PASS，Artifact GC/取消/旧代 completion 回归。
- [ ] Step 5: 提交准入和恢复实现。

### Task 6: 业务评估、Guard 与完整验收

**Files:** Create `internal/server/graph_evaluation.go`, `internal/server/graph_evaluation_test.go`, `examples/iu-graph/orders.json`, `docs/contracts/iu-graph-v1.md`; Modify `internal/server/goal_execution.go`, `internal/server/goal_publication.go`, `internal/server/goal_proposal_http.go`, `cmd/computecloud/jobs.go`, `Makefile`, `.github/workflows/ci.yml`, `docs/README.md`。

**Interfaces:** `EvaluateGraph(ctx,jobID,GraphEvaluation) error`；评估指针按预期版本更新；图 replan 验证当前 FAIL/输出指纹并复用 Guard 原子发布。CLI 通过 Job API 展示图节点/准入/评估/有效并行度；图关闭后已接受图仍继续恢复。

- [ ] Step 1: 写旧 evaluator 三入口隔离、UNKNOWN/deadline、FAIL→Guard→新代、超预算拒绝、旧代评估和完整订单 fixture：两笔 6000 合并优惠后 11000；规则修订后 10000。
- [ ] Step 2: 定向图评估测试观察失败。
- [ ] Step 3: 实现评估与发布分流、操作文档/示例/CLI/CI 入口；保持旧 Job API 行为。
- [ ] Step 4: `go test ./... -count=1`、`go test -race ./internal/graph ./internal/store ./internal/server ./internal/worker -count=1`、`go vet ./...` 和对应现有 smoke/图端到端验证；分别记录通过、失败及环境限制。
- [ ] Step 5: 自审完整 diff、修正问题，提交并推送当前特性分支，创建 draft PR 并附到本任务。不得声明真实 Provider/独立主机验收通过。

## 执行记录

实际交付及未完成项见 [实现范围](../../implementation/iu-computation-model.md)。上方 checkbox 是原始计划，不作为完成声明：Task 5 的持久修订/跨代 import、Task 6 的对应动态规则复用验收尚未实现。已完成各任务的原生图闭环切片，并建立独立 CI；剩余能力保持默认关闭下的显式边界。
