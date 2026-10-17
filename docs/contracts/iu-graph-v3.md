# IU graph v3：显式增量结果复用

状态：实现中，CI/最终独立审查待验收。Server schema v23；原 Worker invocation 不变，新增管理员绑定的 iu_replay_<digest> capability。默认关闭；未配置的真实模型不能复用。

v3 新增 Node.replay_profile、Node.rules（局部必需规则）和顶层 reuse：

```json
{"version":"iu-graph-v3","reuse":[{"grant_id":"reuse_a","node_id":"a"}]}
```

原图全局 rules 仍注入所有节点，不能静默省略。局部规则必须在最初图就明确声明，变更某局部规则才可能保留不读取该规则的根节点。节点没有显式 reuse 时全量执行。v1/v2 不支持这些新字段。

Server jobs.graph_replay_bindings 与 Worker graph_replay_bindings 注册同一 ReplayBinding，字段为 version、runtime_profile、runtime_version、model、environment_sha256、runtime_config_sha256、policy_sha256、read_only。runtime_config_sha256 是规范化实际 Worker Runtime 配置的摘要，policy_sha256 是实际执行 Policy 的摘要；environment_sha256 是管理员独立审计并锁定的工具/模型部署摘要。它不是自动探测的证明。配置路径必须为 Worker 实际加载后的规范化路径；密钥值不写进报告。

Worker 只对匹配 runtime/version/model/config 的绑定宣告 capability，执行前再次核对 model 和 policy。Server 冻结绑定、模板与 workspace，产生需要精确 capability 的普通 Task。非 replay_safe agent、command 或缺少绑定不允许进入结果复用合同。

一次性授权沿用 v2 API，但 purpose 必须为 node_result，record_ids 必须包含整个源 View 的闭包；目标摘要绑定完整 v3 计划。memory 授权不能消费为结果授权。Guard 发布事务重查授权/issuer/有效性 revision/View，保存不可变 offer、来源映射及 Artifact pins。

节点激活在当前依赖和分支就绪后重编实际 Context，比较包含 Node、Context、read-view、Operator、Schemas、模板、workspace 与 ReplayBinding 的完整 key。完全一致且来源有效才 REUSE；输入变化、UNKNOWN、unresolved 或无足够证明则 RECOMPUTE，走普通 Task。取消、deadline 与 IU-2 失效仍优先。重启读取持久事实，不重复创建 Task。

复用使用独立 iu_node_reuse 事实；查询 execution_kind=REUSED，task_id=null，并显示来源 Job/node/generation、reason 和 key。没有创建 Task、Attempt、Admission，原始 Artifact 和执行历史不改写。输出保留，Memory 导入使用目标命名空间且仍为 unchecked；当前代 Goal 仍需独立业务验收。保守永久 pin 接受的复用来源，未来 retention 另案。

fixture 对照要求折扣 1000→2000 后结果 11000→10000，原始提取节点可复用，折扣及独立验证重新执行；同时核对 full/incremental 实际 Task/Attempt、来源和业务结果。fixture 不测真实模型费用、质量或普遍收益。外部部署验收按用户要求未完成。
