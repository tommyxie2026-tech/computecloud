# IU-4 按依赖增量重算

状态：设计/实施中。新图版本 iu-graph-v3、新 Worker capability；v1/v2 不增加隐式结果复用。首版只读、显式一次性 goals:import 授权，与 IU-3 相同 Goal/下一代/目标计划摘要绑定；授权 purpose=node_result，不能与 memory 导入混用。

结果复用需要管理员配置的 replay binding：冻结 runtime/model 版本、环境及工具镜像摘要、策略摘要和只读承诺；Server 与 Worker 必须配置相同绑定。Worker 仅向匹配 runtime/version/model 宣告摘要 capability，并在执行前复查。此配置是管理员对环境不可变性的声明，不是模型自证，也不承诺远程服务永不漂移；没有可核实版本的真实 Provider 不启用。

reuse key 覆盖实际序列化上下文、模型、read-view、算元/Schema、模板、workspace、所有 replay binding。每次激活重新构建实际 Context；只有 exact key 相同、来源成功准入、授权/validity revision 仍有效、无冲突/UNKNOWN/失效记录且完整闭包时 REUSE；来源输入/规则/模型/工具改变则 RECOMPUTE。结构非法或跨租户授权 REJECT。默认继续原 Task 执行。

发布事务消费一次性 node_result 授权、pin 原始 Artifact，保存不可变 reuse offer（原 output/view/context/key/provenance）。激活事务只在依赖就绪、分支选中、未取消/到期后决定。REUSE 使用独立 iu_node_reuse 事实；节点仍为 SUCCEEDED，但 task_id 为 NULL，查询明确 execution_kind=REUSED，不伪造 Task/Attempt/Admission。复用 view 通过相同授权导入映射为新代身份并保持 unchecked。旧执行不改写，当前 Goal 仍独立验收。

为避免把缺失的工具/模型证据误作相同，未注册 binding、非 replay_safe agent、command 及无法比较输入的情况均回退全量。条件分支先求值，不复用 SKIPPED 分支；UNKNOWN 不能变 PASS。结果复用是受授权缓存，不等同语义正确性证明。记录新 Task/Attempt 数、复用来源与未知 usage，不能把 fixture 省调用计为真实费用收益。

全局 Rules 仍注入所有节点；其变化影响所有读取节点。若规则确实只属于局部算元，图必须在最初版本就显式通过相应节点 span 绑定，不能在复用时擅自缩减依赖。没有足够可分解性的图可能没有增量收益。
