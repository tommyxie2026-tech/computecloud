# IU 计算模型演化：基于 v0.5 主线的受限执行图

状态：待用户审阅的架构规格；尚无本规格对应的产品实现或测试结果。

## 1. 基线与迭代目标

本分支 `feat/iu-computation-model` 从远端 main 的 `006792b0994f7fcc67588684053b8f8e6a965785` 建立，不继承旧分支 `feat/iu-memory-computation` 的窄化交付范围。旧分支的两份 memory-foundation 文档是历史方案，不是本次验收依据。

用户明确本次基于 v0.5.0 主线演化计算模型。核查时 README 将 v0.5.0 定位为 Agent-aware Scheduler 阶段，稳定发布链接仍指向 v0.4.9；本文以具体 main SHA 锁定代码基线，不据此宣称版本发布完成。Server schema 当前 v19，Worker v6。

迭代目标是让 computecloud 原生表达和恢复基于 IU 的 AI 计算：输入快照 → 上下文编译 → 多阶段算元执行 → 记忆/证据准入 → 条件验证 → Guard 控制的新代计划。只增加记忆表不能达到此目标。

## 2. 已核实实现与真实缺口

| 当前实现 | 文件 | 对本次演化的约束 |
| --- | --- | --- |
| v0.2 Job 仅支持 single / map_reduce | `internal/job/spec.go`、`api/job/v1/job.schema.json` | 新增显式版本化图契约，不能向旧 schema 偷塞字段 |
| single 或 maps→单 reduce 的图编译 | `internal/server/goal_execution.go` | 提升为通用有限 DAG 编译、冻结和节点激活 |
| Task/Attempt、completion fencing、产物准入 | `internal/server/results.go`、`artifact_lifecycle.go` | 图复用执行事实，不能建立第二套 Attempt |
| reduce 冻结输入清单与 Worker bundle 处理 | `internal/server/job_inputs.go`、`internal/worker/jobs.go` | 为图扩展版本化输入清单和 bundle，不直接开放任意下载 |
| Re-plan 要求 prior Job FAILED 和持久失败评估 | `internal/server/goal_publication.go` | 当前不能直接接收执行成功但业务 FAIL 的结果；须显式扩展评估绑定和发布条件 |
| readiness-v1 只排序通过硬过滤的候选 | `internal/server/server.go`、`docs/implementation/v0.5-scheduler-gap-matrix.md` | 保持项目、凭据、能力、模板、隔离、租约、并发过滤先行 |

当前能力字符串没有类型化 CPU/内存保证；Worker slots 不是硬件资源证明。IU 模型需求只能绑定已支持且冻结的 Runtime/model/credential/policy 配置，不伪造资源或能力保证。

## 3. 选择与职责

选用受限原生 DAG，复用 durable execution。跨 Job 编排虽改动较少，却把依赖、恢复和记忆一致性推给外部；另造 IU 引擎会复制 Scheduler、lease 和 Attempt 状态。二者均不作为此次主方案。

```text
上层规划器 + aicloud Registry / Evaluation / Router
                  ↓ 冻结的执行绑定
Goal → Plan revision → Graph generation
                         ↓
SourceSnapshot → ContextManifest → IU Node → Task → Attempt
                         ↑                      ↓
                 MemoryView ← Receipt ← Accepted Artifact
                         ↓
                   Evaluate → PASS / FAIL / UNKNOWN
                         ↓
               Re-plan Guard → 新 Plan / Graph
```

IU 是有版本的算元契约；Node 是一次调用；Task/Attempt 是其持久执行和尝试。computecloud 负责冻结、校验、调度、恢复与审计；上层负责语义拆分、业务验证器与修正计划；aicloud 提供 Registry/Evaluation/Router。记忆存储服务于当前执行，不扩展为通用知识平台。

图控制器判定节点是否就绪，现有 Scheduler 选择合格 Worker。readiness 评分公式、默认关闭和回退行为保持现有合同。

## 4. 版本化图与算元契约

新增独立 `api/graph/v1/graph.schema.json` 与 `internal/graph`，请求版本 `iu-graph-v1`。以项目级 HTTP Job 提交入口接纳并返回既有 Job ID；入口必须沿用现有鉴权、项目和请求幂等控制。旧 Job API 与 v0.2 schema 继续有效。

冻结图包含：project、固定 workspace、源快照引用、算元定义列表、节点、类型化边、全局约束、deadline、并发和有界重试/重规划预算。定义和图均记录内容摘要；不执行远程可变算元定义。

算元定义字段：`operator_id/version`、输入/输出端口及 schema 摘要、前置条件、输出验证 profile、上下文策略、执行绑定、资源预算、replay_safe 与副作用声明。每个 Node 固定 operator 摘要、参数、输入端口绑定、MemoryView 规则和 Scope。模型概率能力由外部评测版本描述，不能把经验成功率当成 Hoare 逻辑证明。

初始节点种类为 `agent`、`command` 和 `join`。agent 复用 Runtime Adapter；command 只能选择管理员注册的固定程序 profile 和经 schema 校验的参数，不能从模型输出拼接 shell。command 必须经过 Worker 同一执行环境与取消/超时边界，不复用当前 host-side verifier 充当隔离执行器。join 由 Server 做确定性端口/证据合并，不调用模型，也不伪造 Worker Attempt。

每个执行节点映射一个现有 Task，按节点设置 Stage 归属；join 是仅有控制状态的图节点。Graph generation 在发布后不可修改。执行中禁止追加节点、任意循环或修改运行中输入；修正只能通过新 Plan/Graph 发布。

初始边类型：artifact、memory、control。artifact 带输出端口、schema 摘要与源 Artifact ID/hash；memory 必须绑定准入回执。禁止跨项目引用和隐式读取其他 generation 的产物。历史复用通过显式只读 import 保留原来源，并重新检查授权和契约兼容。

首版上限：每图 32 节点、128 边、并行度 8；图请求最多 256 KiB；每个节点编译上下文最多 64 KiB UTF-8 字节；大原文通过受控快照引用传递。超过限制明确拒绝，不截断、不自行放宽。

## 5. 图状态、条件与完成语义

节点状态：BLOCKED、READY、RUNNING、SUCCEEDED、FAILED、SKIPPED、CANCELED；执行节点的运行结果来自 Task，图不覆写 Task 历史。非就绪节点不能预先变成可调度 QUEUED Task。

激活前要求所有所需输入 Artifact 已 ACCEPTED、memory 回执已提交、条件已确定且 generation 当前有效。在一个事务中冻结输入摘要并通过唯一键 `(goal_id,graph_generation,node_id)` 创建 Task、记录激活；崩溃重放不能重复创建。

Graph 的身份为 `(goal_id,graph_generation)`；generation 只在同一 Goal 内递增，不具有全局唯一性。节点、激活、输入绑定、准入回执和评估的复合外键、查询及幂等校验均携带这个命名空间；同时校验 project、Goal/Job 绑定和 plan_revision，不能只凭 generation/node_id 关联。View 内容摘要仅用于完整性和去重，不能充当授权标识；读取 View 仍须绑定所属 Goal 和导入授权。两个不同 Goal 的 generation=1、node_id=extract 必须能独立运行、独立准入。

条件使用已验证输出端口的枚举值 PASS/FAIL/UNKNOWN，不执行模型生成表达式。互斥分支在判定后将未选分支标为 SKIPPED。join 显式声明 `all_success` 或 `selected_success`；前者要求全部上游成功，后者要求全部已选分支成功且未选分支明确 SKIPPED。UNKNOWN 只走显式 unknown 边；无处理分支则停止并报告需要补证据。

command 验证器正常运行并返回业务 FAIL 时，Task 可 SUCCEEDED，Graph 记录业务失败结果；进程/协议/环境失败仍是执行 FAILED。Job 执行状态与业务评估分开记录。正常完成但业务 FAIL 的 Job 不伪装成基础设施失败。

现有 Goal evaluator 的终态与新三值业务结果通过独立评估记录衔接：绑定 Goal、Plan、Graph generation、输出摘要、验证器版本。PASS 才满足业务目标；UNKNOWN 不自动成功或无限重试。业务 FAIL 允许提出新计划，但只能在无旧代活动执行、证据已持久化、预算及 Guard 通过后发布。必须扩展现有 PublishGoalReplan 的合法前置状态，不能绕过其授权、审批和一次性消费逻辑。

### 5.1 图 Job 与旧 evaluator 的隔离及状态转换

Job 的冻结执行类型区分 `legacy-v02` 与 `iu-graph-v1`。`evaluateBoundJob` 及其所有调用入口（包括完成处理、周期 reconcile 和 GET Goal 的读取投影）对图 Job 只能转交图 evaluator，不能执行旧 `job-artifact-v1` 成功终结逻辑。旧 Job 保持原有行为。图 Job 的成功条件由图控制器判定，不能继续套用 single/map_reduce 的阶段查询或单一 reduce result 判断。

| 持久事件 / 前提 | Job 执行状态 | Goal 状态 / 动作 |
| --- | --- | --- |
| 当前代仍有选中节点、准入或 join 未完成 | 非终态；即使全部 Worker Task 已成功也不提前完成 | RUNNING；等待图控制处理 |
| 选中路径执行成功、所需准入 ADMITTED、join 和冻结输出清单完成，无活动 Attempt | SUCCEEDED | 原子进入 EVALUATING，并写入固定输出摘要的待评估记录 |
| 当前代业务评估 PASS，输出摘要及验证器版本匹配 | 保持 SUCCEEDED | SUCCEEDED；这是图 Goal 成功的唯一入口 |
| 当前代业务评估 FAIL | 保持 SUCCEEDED | 有重规划额度则 REPLAN_GUARDING；无额度则 FAILED |
| 当前代业务评估 UNKNOWN 或验证器暂不可用 | 保持 SUCCEEDED | 保持 EVALUATING，原因 NEEDS_EVIDENCE；不可自动成功或自动重规划 |
| 基础设施重试耗尽，或存在永久准入拒绝；所有活动执行已停止 | FAILED | 原子进入 EVALUATING，写入执行/准入失败评估，再按 Guard 和预算决定修正或 FAILED |
| Guard ALLOW 且新代发布事务提交 | 旧 Job 保持终态；新 Job QUEUED | REPLANNING → GRAPH_READY；只有新代可激活 |
| Guard NEEDS_APPROVAL / REJECT | 不改写旧 Job | 分别 NEEDS_APPROVAL / FAILED；保留决策原因 |
| EVALUATING 等待超过 Goal deadline / 用户取消 | 不改写已终结 Job；活动 Job 先取消排空 | 分别 FAILED（EVALUATION_DEADLINE_EXCEEDED）/ CANCELED |

业务评估记录包含 `evaluation_id`、Goal/Plan/Graph、Job ID、冻结输出摘要、验证器 profile/version、证据摘要与 PASS/FAIL/UNKNOWN。记录不可变；补充证据生成新记录，带 `supersedes_evaluation_id` 和预期当前版本，仅当前代且通过并发版本校验的记录可更新评估指针。不同评估内容不能复用同一幂等键。已终结 Goal 不因迟到评估重开。

Re-plan 发布按冻结执行类型选择旧失败评估或图失败评估；图路径只接受当前评估指针指向的 FAIL，或图 evaluator 认证的执行/准入失败，且匹配冻结输出/失败证据摘要。UNKNOWN 不满足发布条件。事务内重查当前 Goal 状态、评估 ID/版本、旧代零活动执行和计划绑定，再消费批准并发布新代；不能仅把 `prior.State != FAILED` 改为允许 SUCCEEDED。

## 6. Token、源信息与上下文编译

IU 不是 token 个数单位，也不接收无来源的 token 切片。SourceSnapshot 保存原始字节、消息角色/次序、附件摘要及版本；Span 使用原始字节偏移和 source hash，UTF-8 文本边界校验后才能渲染。跨模型重用文本来源，各模型自行编码；token ID 不跨 tokenizer 直接拼接。

拆分分为两件事：可无损重建源序列的分区索引，以及面向算元的相关上下文选择。前者通过覆盖、重叠和重建摘要验证；后者可能遗漏语义，不能由重建成功推出语义完整。

ContextManifest 固定全局规则、局部原文 Span、有来源的上游结果、MemoryView、模型/tokenizer 标识和序列化模板版本。编译后保存实际输入摘要与各段来源映射。先序列化，再用目标 tokenizer 计数；无可验证 tokenizer 时记录 token 数 unknown，仍执行字节硬上限，不能声称已提供模型 token 硬预算。

不能容纳必需上下文时，返回 `CONTEXT_INSUFFICIENT`，由上层选择扩窗、显式取回原文、重新拆分或更强模型；不能静默删除规则、只保留摘要后宣称无损。摘要和记忆均保留原始来源，可供验证器回查。冲突、未知指代和缺失依赖必须作为结构化未决项输出。

## 7. 记忆与证据传递

MemoryRecord 包含 kind、payload、source refs、依赖、supersedes、验证状态及验证作用域。kind 为 observation/hypothesis/decision/constraint/unresolved/result。模型生成记录默认 unchecked，不能自证 verified；不要求暴露或存储模型隐藏思维链。

每个节点读取固定 MemoryView，完成后提交候选 MemoryDelta；并行节点各自产生 delta，由显式 join 合并，禁止共同追加一个全局 latest-memory。冲突进入 unresolved，不采用最后写入覆盖事实。验证记录只提升其明确覆盖的命题，不能整批漂白假设。

### 7.1 Read set、修订和失效传播

MemoryView 固定显式 read set：每项携带 record ID、内容摘要、来源快照/Span、当时有效性及验证记录 ID；ContextManifest 另记录实际呈现给算元的规则集、算元/模板版本、参数和其他输入摘要。候选 delta 必须声明它基于的 View/ContextManifest 摘要及派生记录的依赖。Server 校验依赖在授权可读的冻结集合中；首版保守地让每条派生记录依赖算元实际收到的完整输入集合，不相信模型自行省略依赖的声明。固定 read set 不代表模型内部确实使用了每项信息。

源修订、规则版本变更、有效反证或显式 supersedes 以新事件追加，旧记录和旧 View 保持不可变。新 View 的有效性投影沿依赖边传递：基础依赖已被替代或反证则派生结论为 INVALID；出现尚未裁决的矛盾、验证器版本不兼容或有效性无法确认则为 NEEDS_REVALIDATION；只有依赖匹配且无失效事件才为 VALID。这里的 VALID 仅表示依赖仍适用，与 unchecked/verified 验证状态正交，不能把未验证假设升级为事实。传播到传递闭包，不能只标记直接子节点；依赖图必须无环。supersedes 只是修订声明，未经契约认可的反证/修订不能直接提升为有效事实，冲突仍进入 unresolved。

固定 View 支持重放历史计算，不承诺其在新规则下仍有效。当前代新节点激活、最终业务评估和跨代 import 时均重查所需依赖的有效性版本；检查和输入绑定在同一事务内提交。若在节点执行后出现有效失效事件，保留其 Task/Artifact 历史，但禁止把旧结果作为当前有效结论或 PASS 证据。新代图携带新的有效性投影，按依赖范围重算或重验证。

跨代 import 必须声明原 Goal/Graph/Record/Artifact、完整依赖指纹、原验证器版本与有效性版本，并校验当前授权、源内容、必需规则、算元契约及验证器兼容性。任何依赖缺失、INVALID 或无法证明兼容，均不能以 VALID 导入；NEEDS_REVALIDATION 只能作为显式待验证输入进入验证节点。重新验证产生新记录及新验证作用域，保留原来源，不改写旧验证状态。

验收必须包含：订单来源不变、优惠规则从“减 1000”改为“减 2000”后，旧 11000 分结论及其下游结论失效；重新计算为 10000 分。多跳依赖、冲突未裁决和缺失依赖三种情况均不得绕过复核直接 PASS。

### 7.2 准入身份和原子提交

准入键为 `(goal_id,graph_generation,node_id,artifact_id,output_port)`：同键同摘要重放返回原回执，同键不同摘要拒绝。Receipt 同时外键引用该 Goal 内的节点和对应 Artifact，并校验 project/Job/Attempt 来源；不能因 Artifact ID 唯一而省略 Goal 绑定。delta 首版最多 64 KiB、32 条记录，每条 payload 最多 8 KiB、source refs 最多 64。内容摘要采用版本化确定性编码；不声称普通 JSON marshal 等同 RFC 8785。

准入在产物接受后进行：事务外有界读取解析，事务内重查执行来源、hash、固定 read-view、当前 generation、Task 成功和 Attempt 已释放，原子写 Record/View/Receipt 并 pin Artifact。源业务记录另有 source ID 去重，避免 Attempt 幂等替代业务去重。

completion 成功后、准入前崩溃，由 reconciler 从持久 ACCEPTED Artifact 补准入；不重跑已完成 Task。下游 memory 边等待 Receipt，不能读取半准入内容。过时代候选不进入当前代 View；已准入历史保留审计，但跨代 import 必须显式声明。

### 7.3 准入拒绝与恢复终点

候选准入有独立持久状态 `PENDING → ADMITTED | REJECTED`，与 Task 终态分离。Artifact 接受事务根据冻结输出契约写入 PENDING 投影请求，绑定 Artifact/输出端口及其摘要并 pin 源 Artifact；这一事务边界确保 completion 后崩溃仍能发现候选，也避免待准入产物失去恢复来源。只有 ADMITTED 才生成可供下游读取的成功 Receipt；REJECTED 写不可变拒绝记录，不产生可读 MemoryView。

| 条件 | 处理 | 图的后续动作 |
| --- | --- | --- |
| 进程崩溃、暂时不可读、事务忙或可恢复存储异常 | 保持 PENDING，记录错误码/重试次数/next_retry_at；按 1、2、4、8、16、30 秒封顶退避，每次解析重试重新校验来源 | memory 边继续等待，不能启动下游 |
| 首次候选的 schema 错误、超限、Artifact/hash/来源不匹配或非法依赖 | REJECTED，持久记录具体原因与候选摘要 | 当前代选中路径记为 MEMORY_ADMISSION_REJECTED，停止新激活并取消排空其他执行，然后进入失败评估 |
| 已有准入键收到不同摘要 | 拒绝本次调用 IDEMPOTENCY_CONFLICT，记录冲突；不覆盖已有 PENDING/ADMITTED/REJECTED 的摘要或状态 | 保留原候选处理结果，冲突请求不能毒化已合法准入的节点 |
| 到达冻结图 deadline，PENDING 仍未完成 | REJECTED，原因 MEMORY_ADMISSION_DEADLINE_EXCEEDED | 按图执行失败进入评估，不能无限等待 |
| Goal 已取消或候选所属代已过时 | REJECTED，原因 CANCELED / STALE_GENERATION | 不影响新代、不重新打开已终结 Goal |
| 同键同摘要重复处理 | 返回已有状态及成功/拒绝记录 | ADMITTED/REJECTED 都是该候选的终态 |

永久拒绝不会把原本 SUCCEEDED 的 Task 改成 FAILED，也不能通过重跑该 Task 修复；修正内容须由 Guard 批准的新代节点产生新 Artifact。PENDING 的事务故障重试不计为 Worker Attempt。拒绝记录、Artifact 保留期和审计 pin 按现有生命周期统一管理，reconciler 不自行解除审计保留。对 join 的确定性合并失败也必须写入持久控制失败，进入同一图失败路径，不能构造不存在的 Worker Task/Attempt。

## 8. 产物、故障与副作用

扩展 protobuf 图执行描述与版本化 `computecloud-job-io-v2`，通过 Worker capability 显式协商。旧 Worker 不接图任务；旧 single/map_reduce 的 v1 bundle 和冻结清单继续使用原路径。bundle 内允许的图元数据名称、数量、大小和 hash 必须由 v2 契约限定，不开放任意 tar member。

数据库采用向前迁移：当前 v19 之后的下一个可用 Server 版本，实施时重查并分配；不在此预占已可能被其他分支使用的号码。新增图冻结定义、节点状态/激活、输入绑定、memory records/views/receipts 和业务 evaluation；尽量引用现有 Artifact 和 Goal 表。沿用升级前 drain、备份和 WAL 恢复要求。

图运行取消、deadline、Worker 丢失继续使用现有 Task/Attempt 机制；只对 replay_safe 节点采用现有允许的基础设施重试。业务 FAIL 走条件/评估/重规划，不能偷偷加入重试白名单。图级预算在原子预留前校验；当前估算费用控制不能泛化为所有 Provider 的供应商硬账单上限。

### 8.1 首版并行与费用预算的确定边界

首版保留现有 `buildGoalRuntimeBudget` 的单 Goal 费用控制串行语义，不实现并行费用额度拆分。提交时返回冻结的 `requested_parallelism`、`effective_parallelism` 和 `budget_execution_mode`，供 API/CLI 查询：

- 未配置 `max_cost_units` 或 `max_tokens` 时，模式 `unmetered`，有效并行度为请求值（1..8），仍受图/项目/凭据/Worker 并发、总 Attempt 数和 wall-time 限制；该模式没有 token/费用硬上限承诺。
- 配置 `max_cost_units` 时，仅接受所有需要 Worker 执行的 agent 节点均使用已支持 `claude_http` 预算合同的图；首版对混入 command 执行节点的费用预算图明确拒绝 `GOAL_RUNTIME_BUDGET_UNSUPPORTED`，Server join 不消耗 Worker Attempt。模式 `claude_estimated_cost_serial`，`effective_parallelism=1`，即使请求为 8 也必须在提交响应中明确展示约束原因；图结构和依赖不改变。
- 配置 `max_tokens`，或成本预算混用不支持的 Runtime/计费语义时，提交即拒绝 `GOAL_RUNTIME_BUDGET_UNSUPPORTED`，不能等到部分节点执行后才发现不支持。

串行模式在派发 Attempt 的事务内重查同一 Goal 所有未释放 Attempt；只有上一 Attempt 已释放且使用量已完整结算后，才能重新计算剩余额度并预留下一 Attempt。沿用 `RemainingUsageTx` 的 fail-closed 规则：缺失或不完整 usage 返回 `GOAL_USAGE_UNKNOWN`，禁止将未知费用当零继续派发；API 显示阻塞原因，等待可信结算补齐、取消或 deadline 终结。`GOAL_BUDGET_IN_FLIGHT` 是等待资格，不是 Task 失败，不能消耗重试次数。图控制器与 Scheduler 均不能绕过此条件；Server join 与内存准入可运行，但不增加模型执行并发。

预算模式在 Goal 执行期间冻结；不提供通过运行中关预算来解除串行约束的路径。未来要支持有费用上限的并行执行，须另行定义每 Attempt 额度 reservation、原子扣留、未知 usage 的保守占用、结算/退还和跨代共享上限；本规格不把“检查同一余额”视为实现了并行预算控制。

首版禁止图算元直接进行不可回滚外部发布、付款或消息发送；需要副作用时输出待提交 Artifact，由已有授权控制动作提交。后续独立设计 effect journal、外部幂等键与对账；不承诺外部世界 exactly-once。内部纯变换可按冻结输入重放，模型采样重新生成不保证相同文本，恢复优先复用已准入产物。

## 9. 交付切片与完成定义

这些切片共同组成一次计算模型迭代，不能在只完成前两个后宣称 IU 已闭环。规格审阅后编写逐任务实施计划，按依赖顺序实施。

| 切片 | 工作范围 | 可验收结果 |
| --- | --- | --- |
| A：图契约与编译 | graph schema、算元/端口、有限 DAG 校验、旧 Job lowering 纯编译 | single/map_reduce 形状保持；多阶段/类型/环/越界有明确结果 |
| B：持久图控制 | Goal 命名空间、节点状态、输入冻结、原子激活、调度接入与预算模式 | 不同 Goal 同名节点不冲突；重启不重复激活；费用预算串行限制可见 |
| C：IU 数据通路 | SourceSnapshot、ContextManifest、v2 Worker 输入/bundle、固定模型绑定 | Worker 实际执行编译输入；可还原其源引用和上下文摘要 |
| D：记忆准入与 join | read set、失效投影、三态准入、回执/pin、冲突去重和 reconciler | 完成后崩溃可恢复；永久拒绝不悬挂；规则变化传播失效 |
| E：验证与有界修正 | command profile、条件分支、图 evaluator、业务三值评估、Guard 扩展 | 旧 evaluator 不提前完成图 Goal；业务失败能提出新代计划；旧代结果、无授权和超预算被拒绝 |
| F：兼容和完整验收 | API/CLI 状态展示、迁移/取消/旧 Worker 回归、示例、CI 与操作说明 | 端到端证明 A–E；开关回退行为和限制可查 |

图提交开关默认关闭。关闭后拒绝新图提交，但已持久图必须继续恢复、取消和排空；不能靠关闭入口放弃活动执行。无需修改 v0.5 readiness 默认值。旧 Job lowering 先做等价性测试，不立即强制旧执行路径全面迁移。

## 10. 必须通过的证据矩阵

1. **跨片段业务依赖：** 同一客户两笔各 6000 分，累计满 10000 减 1000 且只减一次，正确结果 11000。局部独立计算得到 12000 的对照必须被验证器判 FAIL；IU 图取回全局规则并合并 source ID 后得到正确结果。该 fixture 证明传递机制，不证明任意模型能力。
2. **文本来源：** 中文 UTF-8、消息边界、跨片段指代、重叠 Span、必需规则超过预算。源可重建；缺信息显式不足；不截断冒充完整。
3. **分支与 join：** PASS/FAIL/UNKNOWN 各走正确边；未选 SKIPPED 不被当成成功证据；循环、错端口、错 schema、重复节点和无处理 UNKNOWN 被拒绝或安全停止。
4. **故障注入：** Task 完成前后、Artifact 接受后、Receipt 提交前后、Task 激活事务边界分别崩溃；恢复后无重复 Task、无重复业务记录、无半准入 View，Artifact 不被 GC 提前清除。
5. **代际与权限：** 旧 Attempt/旧 generation/跨项目输入/撤销 import 权限均不能更新当前图。command 限定 profile，并在实际隔离 Worker 中做越权负向验证。
6. **评估修正：** 执行成功但业务 FAIL 可持久化；Guard 拒绝重复计划、重复无进展、超预算和未经授权的新代发布；UNKNOWN 保留未决状态。
7. **兼容：** 旧 single/map_reduce、v1 Worker、备份恢复、取消、deadline、Retry Safety 全部回归；readiness 开关开/关两组均保持硬过滤、确定性与公平性合同。
8. **命名空间：** 两个不同 Goal 均提交 generation=1、node_id=extract；分别成功激活和准入，回执/View 查询不串线。同一 Goal 同键同摘要只创建一次，不同摘要拒绝，篡改 Goal/Job/project 绑定被拒绝。
9. **评估竞争：** Job 成功后业务评估尚未到达时，触发完成处理、周期 reconcile、GET Goal 三条路径并重启，Goal 均保持 EVALUATING。PASS 才成功；FAIL 可进 Guard；UNKNOWN 保持 NEEDS_EVIDENCE 到补证据、取消或 deadline。过时评估、同键不同内容和新代发布竞态不得终结错误的代。
10. **依赖失效：** 规则改版、源被修订和有效反证分别触发至少两跳派生失效；缺失依赖和版本不兼容只能进入复核。原订单不变而折扣从 1000 改为 2000 时，旧 11000 不能导入为 VALID，新计算为 10000。激活后失效、最终评估前失效均不得使用陈旧证据 PASS。
11. **准入活性：** 注入合法 Artifact 内的非法 delta、超限、hash/来源不符，必须持久 REJECTED、排空并进入评估；Task 保持原 SUCCEEDED，无新 Worker Attempt。暂时读取/事务故障可从 PENDING 恢复，持续故障到 deadline 必须终结；重启后 ADMITTED/REJECTED 不能被再次改判。同键不同摘要必须拒绝本次请求，原候选摘要、状态和已成功回执不变。
12. **预算并发：** 无费用/token 预算时，两独立 agent 节点可并行；启用受支持费用预算时请求并行度 8 返回有效值 1，两个候选竞争也只能原子派发一个 Attempt。等待不算失败或重试，结算后才重新核算余额；command 混用、max_tokens 和不支持 Runtime 在提交阶段拒绝。重启及跨代不能复用旧余额或绕过串行限制。

实现阶段需要 Go 1.27.2+；当前主机默认 Go 版本不满足要求，先前下载尚未成功。未运行本规格的实现测试，不将文档校验当成代码验收。真实 Provider 质量/成本收益需要冻结模型版本、样本、随机性、相同预算和对照组后另行评测；不由 fixture 推定。

## 11. 自审与审阅重点

- 已区分现有实现、新合同与尚待验证效果；没有把记录原文解释为确保模型语义无损。
- 明确模型算元、Server join 与 command 执行边界；不让每一个图节点都伪造一个模型调用。
- 三个事实分别持久化：执行接受、记忆准入、业务目标接受。任何一项成功都不替代其他两项。
- 新 schema/wire/迁移是此架构规格的显式范围，不假借 v0.5 Scheduler 文档默认授权。
- 本次完成依据是多阶段 IU 图实际闭环和故障恢复；memory-foundation 仅是其中一个切片。

## 12. 本轮 Review 修订对应表

| Review 项 | 明确决定 | 实施验收条目 |
| --- | --- | --- |
| R1：幂等键命名空间 | 所有图身份、节点激活及准入包含 goal_id；外键与授权一并校验 | 第 10 节第 8 项 |
| R2：旧 evaluator 提前成功 | 冻结执行类型分流，图完成后待评估；状态转换和评估指针控制新代发布 | 第 10 节第 9 项 |
| R3：依赖失效遗漏 | 固定 read set、完整输入依赖、追加式失效传播与严格跨代 import | 第 10 节第 10 项 |
| R4：准入永久失败悬挂 | PENDING/ADMITTED/REJECTED；可恢复错误退避，永久拒绝和 deadline 明确终结 | 第 10 节第 11 项 |
| R5：费用预算与并行矛盾 | 首版成本预算串行且提交时可见；未知/不支持预算拒绝，不实现并行额度拆分 | 第 10 节第 12 项 |

本表表示规格已修订，不表示这些行为已经实现或测试通过。
