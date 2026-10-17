# IU 有限计算图契约 v1

本契约是 computecloud v0.5.0 主线上的实验性增量，默认关闭。IU 是具有输入、算元、上下文、输出和验证边界的逻辑单元；可执行节点最终落到现有 Task/Attempt。Server 上的 join 仅做确定性组装，不创建 Worker 任务。现有 Scheduler、租约、fencing、进程清理、Artifact 和 Guard 继续承担执行职责。

模型 Registry / Evaluation / Router 的产品职责属于 aicloud。本迭代冻结调用方提供的模型和验证器版本，未增加 aicloud 客户端或自动模型选择。computecloud 中的授权评估提交是结果接纳接口。

## 输入与 token 的关系

IU 不是固定数量 token，也不直接切分模型内部状态。本版以 UTF-8 源快照和字节区间表达拆分：source 保存全文和 SHA-256，span 保存 source ID、全文摘要及半开区间 [start,end)。边界必须落在有效 UTF-8 边界；重叠区间可重建，缺口明确报错。source 的 role、message_index 保留原消息身份；它们在上下文中是数据元信息，不会提升为 Runtime 的 system 指令权限。

Server 为每个节点编译实际发送的 JSON 输入，包含 instructions、全部全局 rules、local spans、固定 MemoryView、上游具名 inputs、参数和 output_schema。输入内容摘要、模型绑定和 read-view 摘要被冻结到节点激活记录；Worker 执行这些实际字节。上游 value 是 JSON 编码的字符串，使用方需再次解析。inputs.port 为当前算元的输入端口名，node / source_port 保留上游节点和输出端口，不能混用两端名称。

这保证源数据可追溯、指定片段没有被静默截断；不能证明节点所选片段在语义上充分。跨分片规则应放入 rules，强耦合内容应分配给同一节点，最终验证器应读完整业务证据。超出 64 KiB 上下文时返回 CONTEXT_INSUFFICIENT，调用方重新拆分或提交新图。本版无自动检索、自动摘要、附件快照或 tokenizer；token_count=null、token_coverage=unknown，不能作为 token 无损的实验证据。

## 算元与图

公开 JSON Schema：../../api/graph/v1/graph.schema.json；完整示例：../../examples/iu-graph/orders.json。

- agent：冻结 Execution（runtime_profile / model / credential_ref / policy_ref / acceptance_profile），调用现有 Runtime Provider。
- command：仅引用 Server 预配置 command_profile；固定程序路径由 Worker 运维配置提供，参数只经 JSON Schema 校验后通过 stdin 传递，不拼接 shell 或可执行参数。
- join：输入具名 JSON 值组装成对象输出，同时合并 MemoryView；all_success 要求所有依赖成功，selected_success 排除未选分支。输出仍需满足冻结 Schema。

每个输入、输出端口引用 schemas 中冻结的本地 JSON Schema。边必须 schema 名称一致；实际输出在 Worker 和 Server 准入时校验，join 在 Server 校验。Schema 只支持文档内 JSON Pointer $ref；禁止外部引用、$id、anchors 和动态引用，执行时不访问 Registry。

artifact / memory 边绑定上游端口值，memory 边另外传播已准入记忆；control 边只控制先后关系。条件边只能读取可信 command 的 verdict-v1 输出；每个条件端口必须声明 PASS、FAIL、UNKNOWN 三种处理路径。未选节点为 SKIPPED。缺失或非法值不能悄悄当 PASS。

图编译冻结 DAG，不运行任意循环。循环修复通过 Goal 新 generation 表达。旧 single / map_reduce 仍走原执行器；Lower 仅用于受本契约大小约束的形状映射，不自动迁移已有 Job。

## 记忆传递与准入

公开记忆是 observation / hypothesis / decision / constraint / unresolved / result 等结构化记录，不收集或暴露模型隐藏思维链。每条记录含 payload、来源、依赖和有效性；模型产生的 validation 只能为 unchecked。VALID 表示尚未因依赖变化失效，不能解释为事实已验证。

激活时冻结 read-view 和实际 context hash。Worker 输出 iu-node-output-v1：
```json
{"version":"iu-node-output-v1","values":{"result":11000},"records":[]}
```
Worker 封装 iu-execution-output-v2，绑定 Goal / generation / node / context / delta。Server 接受 Attempt Artifact 的同一事务创建 PENDING 候选并 pin；随后在事务外有界读取 tar，事务内重新检查执行身份、Artifact hash、固定输入、来源与 Schema，再写 ADMITTED 或 REJECTED。

只有 ADMITTED 输出能激活下游。重启重扫 PENDING，已成功 Task 不重跑；暂时读取异常退避并受 deadline 限制；永久拒绝令节点失败并排空图。终态 receipt 不可覆盖，幂等冲突不能污染原 receipt。

来源引用必须属于实际上下文或读取记忆的授权来源。新增记录保守依赖全部呈现记录；同 ID 不同内容成为冲突，不能覆盖。supersedes / Invalidate 的投影传播到依赖后代；历史不可变。依赖旧记录的新替代记录也可能被保守标为失效，需要独立新上下文重新计算，不能自称已恢复 VALID。

当前执行链仅在同一 generation 内传递记忆。跨代发布采用全新图和全新来源，不隐式复用旧结论；跨代显式授权 import、持久化规则修订事件和独立 record 验证提升 API 尚未实现。纯投影工具不能冒充这些运行时能力。

## API、幂等和权限

| 操作 | HTTP | 权限 |
| --- | --- | --- |
| 提交图 | POST /v1/graphs + Idempotency-Key | jobs:submit、项目及全部 credential 授权 |
| 查询图 | GET /v1/jobs/{id}/graph | jobs:read、现有 owner / 项目边界 |
| 业务评估 | POST /v1/jobs/{id}/graph/evaluations | goals:evaluate + jobs:read，同一授权边界 |
| 取消与续期 | 现有 Job API | 现有 jobs:cancel / jobs:extend |
| 新代发布 | 现有 Goal replan / Guard API | 现有 goals:propose、jobs:submit 及 Control lease |

相同提交键和冻结图返回已有 Job；异内容冲突。节点、候选与评估身份包括 Goal ID 和 generation。摘要不能替代权限。

查询返回冻结 plan、有效并行度、节点 Task / 状态 / 错误、准入 receipt / 重试时间和当前评估指针。底层 Artifact 及事件可通过现有 Job artifacts / trace 查看。

## 执行成功与业务成功

Job SUCCEEDED 表示选中节点、输出准入和图结果清单完成。Goal 此时为 EVALUATING；command 的 PASS 只是业务证据，不能自动结束 Goal。独立授权评估者提交：
```json
{
  "id":"orders-eval-1",
  "expected_version":0,
  "output_sha256":"REPLACE_WITH_GRAPH_STATUS_OUTPUT_SHA256",
  "verifier_profile":"orders-proof",
  "verifier_version":"1",
  "verdict":"PASS",
  "evidence_sha256":"REPLACE_WITH_HASH_OF_ACTUAL_EVIDENCE"
}
```
verifier 身份须匹配冻结配置；这是受权限约束的提交声明，不是密码学证明。评估服务应读取结果、复算/核验真实证据并保存证据，不能只把任意字符串算 hash 当验证。

expected_version 与 supersedes_evaluation_id 实现 CAS；同键同内容重试安全，跨代与旧输出评估拒绝。PASS 仅允许成功执行；UNKNOWN 等待新的证据或 deadline；FAIL 有剩余重规划额度时进入 REPLAN_GUARDING，否则 FAILED。新代经原 Guard 验证当前 FAIL、输出摘要、预算和活动 Attempt 后原子发布。评估等待可取消，deadline 也作用于评估、Guard 和待审批状态。没有自动规划器或自动审批。

## 限额、兼容和发布边界

- 图请求 256 KiB；32 节点、32 算元、128 边、128 sources；并行度 1–8。
- 单上下文 64 KiB；delta 64 KiB、最多 32 records、单 payload 8 KiB、单 record 最多 64 source refs；结果 tar 继续受原 Artifact 限制。
- source hash 和 span 是字节级校验；序列化 hash 绑定版本化 Go JSON 编码，不声称采用 RFC 8785。
- max_cost_units 仅 claude_http agent 图支持，强制串行且未知 usage 禁止继续派发；混合 command 成本图及 max_tokens 显式拒绝。费用口径沿用原估算，不是供应商账单硬上限。
- Worker 必须声明 iu_graph_v1；v1 Worker 不接图节点。JobExecution 新增 protobuf 字段，不改变旧字段。
- jobs.enabled 与 jobs.graphs_enabled 必须同时开启才能提交；关闭后已有图继续恢复或排空。
- 原生图当前是只读工作区任务，任何 changes.patch 非空拒绝；没有不可回滚外部副作用的 exactly-once 承诺。固定 command 也必须由运维保证只读可信。
- Gateway credential route 当前拒绝，必须绑定直接 Worker Runtime。
- Server schema 升级为 v20，Worker 仍为 v6。升级前按现有备份流程保存 DB 和 Artifacts；旧二进制拒绝 v20，关闭功能不等于可降级数据库。
