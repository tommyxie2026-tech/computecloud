# IU 下一轮：约束覆盖、证据补充与受限修正

日期：2026-10-18。状态：待审阅的架构规格，尚未实现；不是功能完成或实验效果报告。

源码基线：computecloud main `156867bb0fa3d73a9c3217e816b758f2200b4528`。本文件在 `feat/iu-semantic-recovery` 编写，不预设下一发布版本或数据库迁移编号。

## 1. 用户目标与本轮成功标准

用户认可上一轮审查建议，要求进行下一轮。延续已明确的范围：先交付代码与 CI；真实 Provider、aicloud 服务和两台独立 Worker 主机验收继续保留未完成。

初衷是使 IU 成为 Task 上可组合、可验证、可恢复的智能算元契约。原文、结构化记忆和证据参与后续计算；不以模型隐藏思维作为持久状态，不建立第二套执行引擎。aicloud 继续拥有 Registry/Evaluation/Router。

本轮应使以下过程可运行、可复现：

1. 将明确的业务约束绑定到原文、负责节点和最终检查项。
2. 在发布前发现已声明约束没有进入负责节点上下文的情况。
3. 对已经完成但被可信检查判定违约的图，读取授权证据，生成受限的新计划，经现有 Guard 发布新代，再独立验收。
4. 保留不充分、无法判定、拒绝、超预算和失败样本；CI 的确定性结果与真实模型质量分开报告。

声明清单的覆盖不证明清单已经发现所有自然语言含义。只有在受支持、独立检查的有限业务域内，才能对具体检查项报告通过。

## 2. 已核实的现状

| 源码或合同 | 当前行为 | 本轮落点 |
| --- | --- | --- |
| `internal/graph/context.go`、`token.go` | 按 source/span/hash 编译；检查已声明 required_evidence；超过字节预算显式失败 | 把跨节点的约束责任编译成这些机械检查，不替换原编译器 |
| `internal/server/graph_execution.go` | memory 合并、固定输入、Task 激活；编译不足停止图 | 保持已有冻结输入和调度事实 |
| `internal/server/graph_source_read.go` | 每节点每代两次、每次 16 KiB；幂等、CAS、权限及持久回执 | 复用读取，不允许 Worker 私自读取或改动运行中 Context |
| `internal/server/graph_evaluation.go` | Job 成功不等于 Goal 成功；独立评估维护 PASS/FAIL/UNKNOWN | 增加 v5 覆盖证据的准入检查；保留旧版本行为 |
| `internal/server/graph_replan.go` | 仅当前可信 FAIL、终结 Job、零在途 Attempt、预算和 Guard 允许时发布 | 修正提案只能走此路径，不能因 UNKNOWN 放宽发布条件 |
| `internal/graph/memory.go` | 传递不可变 View，保留冲突，保守依赖完整读取集合 | 本轮不缩小已有来源闭包，不把记忆压缩与语义补证同时改动 |
| `internal/iuexperiment/protocol.go` | 已有单节点、IU、无记忆、无验证四臂协议；20 条为四场景各重复五次 | 另加跨片段反例与补证场景，不把重复误写成独立任务族 |
| `internal/aicloudadapter` | 只读证据 Fetch/Freeze，未接生产自动路由 | 本轮不接生产路由，不自动换模型 |

## 3. 方案选择

| 方案 | 收益与代价 | 结论 |
| --- | --- | --- |
| A. 上层覆盖编译与受限提案，Server 校验并持久化证据，复用 Guard | 责任明确，可从现有 v4 和四臂工具扩展；需要独立验收合同 | 选用 |
| B. Server 内置通用语义 Planner、自主检索和模型路由 | 可集中决策，但会混合业务语义、权限、模型策略与执行内核，并扩大改动面 | 不纳入本轮 |
| C. 仅补充提示词，要求模型声明信息完整 | 改动小，但不能阻止漏约束、自证或重启后重复修正 | 不作为验收实现 |

## 4. 约束覆盖契约

新增独立、严格版本化的 `iu-coverage-v1` 结构；通过 `iu-graph-v5` 显式携带。v1～v4 的请求 schema、冻结输入、旧 hash 和评估合同不偷改。没有新能力支持的服务端拒绝 v5，不回退成旧图执行。

CoverageCatalog 包含：

- `version`、原始验收规格的 `goal_spec_sha256`、catalog 内容摘要。
- 至多 64 条 obligation；每条包含稳定 `constraint_id`、精确来源 spans、负责的 node IDs、最终 `check_id`。
- 冻结的独立 coverage checker profile/version；profile 配置有自己的内容摘要。
- checker 的适用域。它描述检查范围，不是对模型能力或所有语义的证明。

约束来源必须引用同一冻结 Sources 的原始字节，保留 role/message_index。节点、span、check ID 不存在、ID 重复、来源摘要漂移、UTF-8 边界不合法均拒绝。每条至少一个来源、一个负责节点和一个最终检查；条目内部的 spans 与节点引用各不超过 32。受图请求 256 KiB 总上限约束。

覆盖编译逐条计算“约束 → 来源 → 负责节点 → 最终 check”。每个负责节点的实际 rules/spans 必须包含该约束声明的全部必需来源，可用合法重叠区间覆盖；完整 Sources 可取回不算已经装载。报告保留缺口的 constraint_id、node_id、source/span 和原因。

发布前 preflight 有结构缺口时不创建 Job/Task/Attempt。命令输出结构化不足报告，调用者可补全后重新检查；不能把尚未发布的草稿包装成一次 Re-plan。Server 对提交图执行相同检查，客户端 preflight 通过不替代服务端校验。

这仍不能检测 catalog 自身漏列了例外。为防止把模型生成的清单直接当完整事实，最终独立 checker 必须从冻结的原始输入与原始验收规格重新检查受支持的规则集合，并返回它实际覆盖的 check IDs。无法判定清单完整性的任务保持 UNKNOWN。

## 5. 独立覆盖回执与最终验收

Server 注册管理员只读 `graph_coverage_verifiers` profile。首轮仅实现一个受限订单域 checker，以及明确返回 UNKNOWN 的 unavailable profile；不执行模型提供的命令或任意表达式。

订单域使用版本化结构化输入：订单身份、客户、金额/单位、全局门槛、优惠次数与明确例外。非法、缺失、重复且冲突、未知规则类型均显式 UNKNOWN；不声称能完整解释任意自然语言条款。自然语言跨段任务可进入评测，但未配置可信 checker 时不能被内核自动判成完整覆盖。

新增 coverage verification 入口，由有 `jobs:read`、`goals:evaluate` 及项目/owner 权限的独立评估身份调用；调用者指定 profile、目标输出和预期版本，不能提交自选 verdict。Server 按冻结 profile 执行内置只读检查。普通提交身份不能因此取得验收权限。

不可变 CoverageReceipt 绑定：Goal、generation、plan/catalog/goal-spec/output 摘要、validity revision、checker profile/version/hash、逐 check 的 PASS/FAIL/UNKNOWN 与可重建证据摘要。Evidence 必须来自冻结 Sources 与已接受且仍被固定引用的输出 Artifact；纯字符串 hash 不是可获取的证据。涉及 Artifact 的引用与回执同事务 pin。

检查分两步：事务外有界读取/计算；提交事务内重查认证、项目、当前代、有效性、输出、profile 与来源是否仍一致。同键同内容幂等，不同内容拒绝。profile 超时、卸载、漂移和读取失败保持 UNKNOWN。回执总数沿用每代有界原则，首轮每代最多 128 条。

v5 的 Goal PASS 必须显式引用当前适用的 coverage receipt，且所有必需 check 均 PASS；已有 record_proofs 继续按其原范围复查，不能代替覆盖回执。任一已证实业务违约为 FAIL；无违约但存在未知为 UNKNOWN。覆盖通过仅允许进入完整业务验收，不替代额外验收项目。v1～v4 仍按原合同评估。

## 6. 证据补充与修正的闭环

### 6.1 两种不足分开处理

- **发布前缺口**：preflight 拒绝发布，完善草稿，不消耗一次真实执行，也不绕过已存在 Goal 的发布权限。
- **已发布图的失败**：可信 checker 确认具体违约，当前业务评估为 FAIL，Job 已终结且旧代已排空后，才允许进入下述修正。

UNKNOWN 继续 EVALUATING/NEEDS_EVIDENCE，可补交独立证据并生成带 supersedes 的新评估；在产生可信 FAIL 前不能自动重规划。若仍 UNKNOWN，则等待授权操作或 deadline 停止。不伪造 FAIL，不无限等待，也不因此自动 PASS。

### 6.2 有界上层提案

增加一次性 CLI 编排入口，工作是读取 Server 事实、构造提案并调用已有 API。它没有独立 Scheduler、Attempt 或长期后台循环。首轮修正只能扩大同一冻结原文的 span 选择、恢复声明的规则、补充节点执行指令；不换模型、Runtime、credential、policy、workspace、算元 Schema、图拓扑或原始 Goal。

调用者先检查诊断和缺失的辅助证据，使用现有 source-reads 入口读取精确 spans。回执须与 prior Goal/generation/plan/node/hash 一致，保持两次/节点/代、16 KiB/次限制。补证不得指向 URL、任意路径、其他项目或未授权 generation。

随后产生不可变 RecoveryProposal，包含 proposal ID、prior Plan/generation、当前 FAIL evaluation ID/hash、coverage receipt、source-read receipt IDs、明确的增加 spans 与新计划摘要。不删除原有必需规则、约束和证据，也不以摘要替换原文；对允许字段之外的变更直接拒绝。

Server 在正式发布事务中检查提案的上述限制、认证/撤销、预期版本、当前 FAIL 指针、零在途执行和所有回执适用性，然后调用既有 Guard 发布新代。catalog、GoalSpec 和 Sources 在该自动修正路径保持不变；若发现需修订 catalog 或原始验收规格，退出自动修正，交独立审阅的新提案。

新代重新冻结 Context 并创建正常 Task/Attempt。不会修改旧 Attempt 的输入或接纳迟到输出；导入与复用只在现有显式授权及完整 key 匹配下发生。本轮默认全量重算，不自动发放 import/reuse grant。

### 6.3 状态、次数与重启

每个 Goal 最多两次自动修正；仍受原 max_replans、deadline、attempt 和已有费用策略约束，取更严格限制。每次提案最多 8 个 source-read 回执和总共 64 KiB 补充字节，且完整新 Context 仍须满足 64 KiB 上限；既有读次数耗尽不因 CLI 重启清零。

计数与幂等结果必须保存在 Server 的事实表，不能依赖 CLI 内存或本地文件。计数消费、提案与新代关联在 Guard 的发布事务中一起提交；重复请求返回已有结果，不再消费。Guard NEEDS_APPROVAL/REJECT 时保留诊断但不创建执行、不消费成功发布次数；未消费不等于可以绕过幂等和请求大小限制。

事实表只保存提案/回执/发布关系；是否可执行仍由现有 Goal/Plan/Task/Attempt/Guard 决定。不新增一套 IU 运行状态机。

## 7. 场景与四臂对照

保留原 monolithic、iu、iu_no_memory、iu_no_verifier 四臂含义，新增独立版本协议与数据集摘要，不修改既有 v1 协议的固定 20 条校验或重标旧报告。

| 场景 | 预期检验 |
| --- | --- |
| 跨两片订单累计到门槛 | 局部提取保留客户与身份，优惠在全局聚合后只应用一次 |
| 后片声明的特价例外 | catalog 漏项与 context 漏装分别可见；可信全局 checker 能拒绝错误金额 |
| 跨片客户引用、表头单位 | 依赖未解或单位不明确时 UNKNOWN，不猜补 |
| 重复订单与冲突订单 | 去重与冲突分别处理，不能由最后到达覆盖 |
| 完整输入下模型仍忽略规则 | 独立 FAIL 后可补充授权证据/指令，新代重新执行和检查 |
| 来源不可读、撤权或超预算 | 拒绝/UNKNOWN，不继续执行或伪造通过 |

各场景至少包含正确结果、可定位的错误结果与不确定结果；fixture 明确注入错误，不能四臂都使用预制正确答案后声称验证有效。独立 benchmark oracle 只用于报告 ground truth，不向执行臂泄露隐藏答案；业务运行时 verifier 与离线评分分开记录。

报告保存初次与修正后的独立业务成功率、字段保留、约束遗漏、错误接纳、UNKNOWN、所有尝试/补证/验证调用、读取字节、端到端耗时和实际 usage/cost。失败、未运行、超预算计入总分母，未知用量为 null。补证效果另外记录修正次数、真正新增 Task/Attempt、旧新 generation 与来源回执。

“无验证”消融可测错误接纳倾向，但不能借此绕过生产 v5 Goal 的强制验收。四臂实验维持在独立 Harness；生产恢复测试只使用完整验收路径。

本轮 fixture 证明协议行为；真实模型命令入口和报告校验可交付，但真实 Provider 结果保持 NOT_RUN。普通 Runner 上的多进程仍不代表独立双主机。

## 8. CI 验收与拒绝条件

必须实际执行且不能 SKIP 的验收覆盖：

1. 缺 constraint/node/check/source、重复身份、错 hash、UTF-8、跨项目和未知 schema 拒绝；missing span 不创建 Task。
2. 全局规则在负责节点实际 Context 中，旧版本编译和冻结 hash 不变。
3. 伪造 verdict/回执、profile 漂移、失效 revision、陈旧 generation 和输出替换不能 PASS。
4. UNKNOWN 不能触发自动 Re-plan，无法解释的例外与单位保持 UNKNOWN。
5. 授权补证 → 当前 FAIL → Guard → 新代 Task/Attempt → 独立验收的原生 Server/Worker fixture；旧代 Context/Attempt 不改写。
6. 发布提交前后崩溃、响应丢失重放、CLI/Server 重启、同键异内容、并发提案和活动 Attempt 排空；无重复代、无重复消费。
7. 修正两次上限、source-read 上限、deadline、Context 超限、撤权和 NEEDS_APPROVAL；禁止借换 ID 或客户端重启清零。
8. 新增故障样本在四臂中保存实际失败/未知/误接纳，缺失样本不得从报告分母删去。
9. 新表迁移/备份/恢复及严格 gate 名单；完整 Linux/macOS 回归不被新增测试替代。

报告必须附实际执行 SHA、环境、命令、样本协议与原始证据。代码/CI 通过只表示本轮工程验收；H0～H9 的质量、降本和分布式收益仍待真实实验。

## 9. 实施切片及文件职责

以下是规格级切片，不是已获审阅的详细实施计划：

| 切片 | 拟议落点 | 可单独检查的结果 |
| --- | --- | --- |
| A. 覆盖结构与 preflight | `internal/graph/coverage.go`、`api/graph/v5/`、现有 compile-graph | 缺口报告与提交阻断、v1～v4 兼容 |
| B. 可信验收 | `internal/server/graph_coverage.go`、config、追加迁移、graph_evaluation | 独立回执、来源 pin、v5 PASS 门禁 |
| C. 有界修正提案 | `internal/graph/recovery.go`、`internal/server/graph_recovery.go`、`cmd/computecloud/jobs.go` | 受限 patch、Guard 事务、持久次数和幂等 |
| D. 对照与故障门禁 | `internal/iuexperiment`、`scripts/ci_iu_graph.py`、Server/Worker fixture | 真实原生恢复事实与四臂协议，Provider 仍 NOT_RUN |
| E. 文档与交付 | 版本合同、示例、README、长期路线图 | 状态与证据一致，单个集成分支/PR 收敛 |

实现时追加迁移，不回写既有迁移文件，不自行分配尚未核对的下一 schema 编号。所有切片保留 graphs_enabled 默认关闭，旧模式不自动切换。具体签名、事务表和逐项测试将在本规格审阅后形成实施计划。

## 10. 本轮不扩大的边界

- 不保证任意自然语言语义完整，不把 catalog、来源 hash 或模型自述等同真值。
- 不新增生产 tokenizer 硬上限、附件/二进制、模型内部 token/KV 传递。
- 不接生产 aicloud 路由、不自动换模型、不宣称费用或能力收益。
- 不同时实现记忆压缩/投影、跨 Goal 共享、patch DAG 或不可回滚外部副作用。
- 不用 UNKNOWN 伪装 FAIL，不绕过 Guard/批准，不重开已终结 Goal。

## 11. 审阅要点

需确认的架构取舍是：先交付有限业务域的、独立可检查的覆盖与 FAIL 修正闭环；自然语言的完整性仍明确未知。模型路由、记忆压缩、真实 Provider 和独立主机作为后续独立验收，不将其假装纳入本轮已完成范围。

本规格尚无实现、测试或模型结果。后续顺序为规格审阅 → 详细实施计划审阅 → 实现、回归与独立审查 → 单一 PR 与精确 SHA 的 CI 证据。
