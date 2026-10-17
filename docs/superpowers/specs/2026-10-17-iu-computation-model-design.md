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

激活前要求所有所需输入 Artifact 已 ACCEPTED、memory 回执已提交、条件已确定且 generation 当前有效。在一个事务中冻结输入摘要并通过唯一键 `(graph_generation,node_id)` 创建 Task、记录激活；崩溃重放不能重复创建。

条件使用已验证输出端口的枚举值 PASS/FAIL/UNKNOWN，不执行模型生成表达式。互斥分支在判定后将未选分支标为 SKIPPED。join 显式声明 `all_success` 或 `selected_success`；前者要求全部上游成功，后者要求全部已选分支成功且未选分支明确 SKIPPED。UNKNOWN 只走显式 unknown 边；无处理分支则停止并报告需要补证据。

command 验证器正常运行并返回业务 FAIL 时，Task 可 SUCCEEDED，Graph 记录业务失败结果；进程/协议/环境失败仍是执行 FAILED。Job 执行状态与业务评估分开记录。正常完成但业务 FAIL 的 Job 不伪装成基础设施失败。

现有 Goal evaluator 的终态与新三值业务结果通过独立评估记录衔接：绑定 Goal、Plan、Graph generation、输出摘要、验证器版本。PASS 才满足业务目标；UNKNOWN 不自动成功或无限重试。业务 FAIL 允许提出新计划，但只能在无旧代活动执行、证据已持久化、预算及 Guard 通过后发布。必须扩展现有 PublishGoalReplan 的合法前置状态，不能绕过其授权、审批和一次性消费逻辑。

## 6. Token、源信息与上下文编译

IU 不是 token 个数单位，也不接收无来源的 token 切片。SourceSnapshot 保存原始字节、消息角色/次序、附件摘要及版本；Span 使用原始字节偏移和 source hash，UTF-8 文本边界校验后才能渲染。跨模型重用文本来源，各模型自行编码；token ID 不跨 tokenizer 直接拼接。

拆分分为两件事：可无损重建源序列的分区索引，以及面向算元的相关上下文选择。前者通过覆盖、重叠和重建摘要验证；后者可能遗漏语义，不能由重建成功推出语义完整。

ContextManifest 固定全局规则、局部原文 Span、有来源的上游结果、MemoryView、模型/tokenizer 标识和序列化模板版本。编译后保存实际输入摘要与各段来源映射。先序列化，再用目标 tokenizer 计数；无可验证 tokenizer 时记录 token 数 unknown，仍执行字节硬上限，不能声称已提供模型 token 硬预算。

不能容纳必需上下文时，返回 `CONTEXT_INSUFFICIENT`，由上层选择扩窗、显式取回原文、重新拆分或更强模型；不能静默删除规则、只保留摘要后宣称无损。摘要和记忆均保留原始来源，可供验证器回查。冲突、未知指代和缺失依赖必须作为结构化未决项输出。

## 7. 记忆与证据传递

MemoryRecord 包含 kind、payload、source refs、依赖、supersedes、验证状态及验证作用域。kind 为 observation/hypothesis/decision/constraint/unresolved/result。模型生成记录默认 unchecked，不能自证 verified；不要求暴露或存储模型隐藏思维链。

每个节点读取固定 MemoryView，完成后提交候选 MemoryDelta；并行节点各自产生 delta，由显式 join 合并，禁止共同追加一个全局 latest-memory。冲突进入 unresolved，不采用最后写入覆盖事实。验证记录只提升其明确覆盖的命题，不能整批漂白假设。

准入键为 `(graph_generation,node_id,artifact_id,output_port)`：同键同摘要重放返回原回执，同键不同摘要拒绝。delta 首版最多 64 KiB、32 条记录，每条 payload 最多 8 KiB、source refs 最多 64。内容摘要采用版本化确定性编码；不声称普通 JSON marshal 等同 RFC 8785。

准入在产物接受后进行：事务外有界读取解析，事务内重查执行来源、hash、固定 read-view、当前 generation、Task 成功和 Attempt 已释放，原子写 Record/View/Receipt 并 pin Artifact。源业务记录另有 source ID 去重，避免 Attempt 幂等替代业务去重。

completion 成功后、准入前崩溃，由 reconciler 从持久 ACCEPTED Artifact 补准入；不重跑已完成 Task。下游 memory 边等待 Receipt，不能读取半准入内容。过时代候选不进入当前代 View；已准入历史保留审计，但跨代 import 必须显式声明。

## 8. 产物、故障与副作用

扩展 protobuf 图执行描述与版本化 `computecloud-job-io-v2`，通过 Worker capability 显式协商。旧 Worker 不接图任务；旧 single/map_reduce 的 v1 bundle 和冻结清单继续使用原路径。bundle 内允许的图元数据名称、数量、大小和 hash 必须由 v2 契约限定，不开放任意 tar member。

数据库采用向前迁移：当前 v19 之后的下一个可用 Server 版本，实施时重查并分配；不在此预占已可能被其他分支使用的号码。新增图冻结定义、节点状态/激活、输入绑定、memory records/views/receipts 和业务 evaluation；尽量引用现有 Artifact 和 Goal 表。沿用升级前 drain、备份和 WAL 恢复要求。

图运行取消、deadline、Worker 丢失继续使用现有 Task/Attempt 机制；只对 replay_safe 节点采用现有允许的基础设施重试。业务 FAIL 走条件/评估/重规划，不能偷偷加入重试白名单。图级预算在原子预留前校验；当前估算费用控制不能泛化为所有 Provider 的供应商硬账单上限。

首版禁止图算元直接进行不可回滚外部发布、付款或消息发送；需要副作用时输出待提交 Artifact，由已有授权控制动作提交。后续独立设计 effect journal、外部幂等键与对账；不承诺外部世界 exactly-once。内部纯变换可按冻结输入重放，模型采样重新生成不保证相同文本，恢复优先复用已准入产物。

## 9. 交付切片与完成定义

这些切片共同组成一次计算模型迭代，不能在只完成前两个后宣称 IU 已闭环。规格审阅后编写逐任务实施计划，按依赖顺序实施。

| 切片 | 工作范围 | 可验收结果 |
| --- | --- | --- |
| A：图契约与编译 | graph schema、算元/端口、有限 DAG 校验、旧 Job lowering 纯编译 | single/map_reduce 形状保持；多阶段/类型/环/越界有明确结果 |
| B：持久图控制 | 节点状态、输入冻结、原子激活、调度接入与恢复 | 多阶段 ready 节点进入现有 Scheduler；重启不重复激活 |
| C：IU 数据通路 | SourceSnapshot、ContextManifest、v2 Worker 输入/bundle、固定模型绑定 | Worker 实际执行编译输入；可还原其源引用和上下文摘要 |
| D：记忆准入与 join | Record/View/Delta、准入回执、pin、冲突/去重、reconciler | 并行结果可重复合并；完成后崩溃恢复且下游只读固定 View |
| E：验证与有界修正 | command profile、条件分支、业务三值评估、Guard 扩展 | 业务失败能提出新代计划；旧代结果、无授权和超预算均被拒绝 |
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

实现阶段需要 Go 1.27.2+；当前主机默认 Go 版本不满足要求，先前下载尚未成功。未运行本规格的实现测试，不将文档校验当成代码验收。真实 Provider 质量/成本收益需要冻结模型版本、样本、随机性、相同预算和对照组后另行评测；不由 fixture 推定。

## 11. 自审与审阅重点

- 已区分现有实现、新合同与尚待验证效果；没有把记录原文解释为确保模型语义无损。
- 明确模型算元、Server join 与 command 执行边界；不让每一个图节点都伪造一个模型调用。
- 三个事实分别持久化：执行接受、记忆准入、业务目标接受。任何一项成功都不替代其他两项。
- 新 schema/wire/迁移是此架构规格的显式范围，不假借 v0.5 Scheduler 文档默认授权。
- 本次完成依据是多阶段 IU 图实际闭环和故障恢复；memory-foundation 仅是其中一个切片。
