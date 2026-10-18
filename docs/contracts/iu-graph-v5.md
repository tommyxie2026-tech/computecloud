# IU graph v5：约束覆盖与受限补证

状态：代码与定向测试已实现；完整平台和原生进程验收以集成 PR 当前 SHA 的 CI 为准。Server schema v26，Worker v6；默认 graphs_enabled=false。v1～v4 的请求、冻结 hash 与业务评估行为不切换。

## 覆盖声明和输入

完整例子在 [coverage-v5/orders.json](../../examples/iu-graph/coverage-v5/orders.json)。v5 必须包含 `iu-coverage-v1` catalog：

- `goal_source_id` 指向冻结 Sources 内完整的原始验收规格；`goal_spec_sha256` 校验其原始字节。
- `profile/profile_version/scope` 绑定管理员配置的独立 checker。
- `obligations` 每条声明 constraint_id、原文 spans、负责 node_ids 和 check_id。
- `checks` 把 check_id 绑定到明确的 Graph Outputs；`sha256` 是自身该字段置空后的 Go 结构规范化 JSON 摘要。

图请求仍最多 256 KiB；catalog 至多 64 条 obligation，每条 sources/node_ids 各 32；checks 与每个输出绑定列表各 32。节点规则与 spans 必须实际覆盖声明原文，缺口在发布前拒绝，不创建 Job/Goal/Task/Attempt。命令 compile-graph 返回结构化 coverage_report 并以错误状态退出。必需 spans 去重后仍受节点 ContextPolicy 的 128 条上限；不得先发布再因策略本身不可编译而失败。

原始字节、角色、消息顺序和 hash 留在冻结 Source/Context 中；原件可取回不等于模型已阅读。声明覆盖只证明结构关系，不能发现任意自然语言的隐藏依赖。Catalog 的模型自述不构成完整性证明。

## 有限订单域

Server 配置：

```yaml
jobs:
  graphs_enabled: true
  graph_coverage_verifiers:
    orders:
      version: '1'
      kind: orders-v1
      scope: orders
      timeout_ms: 1000
```

启用以上配置需沿用既有 jobs、Worker 模型/凭据/模板及项目授权；示例未授权自动部署。checker 只能为 orders-v1 或 unavailable，不执行客户端表达式、shell 或外部 verifier。

GoalSource 为严格 iu-orders-goal-v1 JSON：version、currency、unit、order_source_ids、rule_source_id、exception_source_ids、required_checks。单位为 cent；同一 Goal 的结果必须保持货币/单位。规则包含 threshold_cents、discount_cents、max_discounts_per_customer=1；订单必须有 order_id/customer_id/paid_cents/is_special。金额为非负 int64；溢出保持 UNKNOWN。

唯一已支持的例外为 special_no_discount：特价订单计原价，但不参与优惠门槛且不打折。按客户聚合非特价订单，每客户优惠最多一次，优惠不超过该客户参与优惠的金额。重复且相同的订单只算一次；同 ID 冲突保持 UNKNOWN。

固定检查为 source_identity、source_fields、customer_grouping、threshold_once、exception_rules、currency_unit。GoalSpec、catalog 与 checker 的检查集合须完整匹配，来源不能有未分类内容。严格检查字段名和必需字段；重复键、大小写别名覆盖、未知规则、未解指代/单位和不可用证据不能自动通过。最终金额正确也不能掩盖源字段损坏。

## 独立回执与验收

POST `/v1/jobs/{id}/graph/coverage-verifications`，body 至多 4096 bytes：

```json
{"id":"proof","expected_validity_revision":0,"output_sha256":"<graph 查询输出摘要>","profile":"orders","profile_version":"1"}
```

调用方需要 jobs:read、goals:evaluate 和项目/owner 权限，不能提交 verdict。检查在事务外执行；提交事务重查认证、当前代、输出、有效性 revision、checker 和来源。不可变回执同事务 pin 已 ACCEPTED 的输出 Artifact，最多 128 条/代。相同 ID/内容幂等，异内容拒绝。GET `/v1/jobs/{id}/graph/coverage-verifications/{receipt}` 读取已保存的回执；读取历史不代表它仍适用。

v5 evaluate-graph 的 PASS 必须带 `coverage_proof:{receipt_id,sha256}`，hash 为完整回执的规范化摘要。Server 检查当前适用性、完整逐项 PASS 和 Artifact 引用，并在提交前验证实际证据文件可取回/hash 正确。事务内继续校验冻结绑定；不保证外部绕过存储合同直接破坏文件时的跨系统原子性。profile 修订/卸载、旧代、失效、错输出、缺失/伪造证明均不能 PASS。

执行成功、覆盖通过和完整业务通过仍是不同事实。UNKNOWN 留在待评估状态，不变成 FAIL 触发自动重规划；系统执行失败仍可按旧机制记录内部 FAIL，但不进入本轮限定的业务修正路径。

## 有界修正

`job recover-graph --id JOB --file recipe.json` 使用以下独立版本 recipe：

```json
{"version":"iu-recovery-recipe-v1","id":"repair-1","prior_plan_sha256":"<原计划 hash>","evaluation_id":"<当前可信 FAIL ID>","evaluation_sha256":"<该 evaluation 的规范化 hash>","reads":[{"node_id":"extract","span":{"source":"goal","start":0,"end":123,"sha256":"<原文 hash>"}}],"instructions":{"extract":"\n补充的、待执行的检查指令"}}
```

例中 span 只是形状，需使用实际 byte range/hash。CLI 获取当前事实和独立回执，通过稳定请求 ID 查询/读取授权原文，构造新计划，获取既有控制租约，调用 POST `/v1/jobs/{id}/graph/recovery-proposals`。最终独立复验仍需评估身份显式执行，不由提交身份自授 PASS。

读取查询为 GET `/v1/jobs/{id}/graph/source-reads/{node}`，CLI 为 `job read-source-status --id JOB --node NODE`；每节点返回至多两条 request/receipt。该查询不消耗读取次数。原 source-read 的两次/节点/代、16 KiB/次及 CAS 不变；冲突时 CLI 停止，不偷偷换 ID 重试。

修正提案只能扩大原有 rules/spans 或保留原指令前缀再追加文字（每节点追加最多 8 KiB）。Sources、catalog、Goal、模型/Runtime/凭据/策略、Schema、图结构、workspace、limits、imports/reuse 均不变；新增 span 必须由该节点精确的回源回执授权。首轮不自动发放 import/reuse grant，不做跨 Goal 分享、路由或模型升级。

每提案最多 8 个回执、64 KiB 补充字节；完整 Context 仍最多 64 KiB。每 Goal 最多两次成功自动修正，仍服从更严格的原 Guard、deadline、attempt 和费用合同。相同失败证据仍可能被原 NO_NEW_EVIDENCE 拒绝，两次是上限而非保证可运行两次。

成功次数来自持久 publication 事实。提案、授权检查和 Guard 关联不靠 CLI 内存；成功的新代、publication 与 Guard 消费同事务提交。并发/丢响应重放不重复发布或消费；NEEDS_APPROVAL/REJECT 不产生执行。等待批准的同一提案可走既有批准合同；不绕过预算上限。普通 propose-graph 不能绕过 v5 恢复限制，返回 V5_RECOVERY_PROPOSAL_REQUIRED。需改 catalog 或 Goal 的提案退出此自动路径，另行审阅。

## 运行与证据

```sh
computecloud job compile-graph --file examples/iu-graph/coverage-v5/orders.json
computecloud job submit-graph --config client.yaml --file examples/iu-graph/coverage-v5/orders.json --key coverage-orders-v1
computecloud job verify-coverage --config evaluator.yaml --id JOB --file verification.json
computecloud job coverage --config evaluator.yaml --id JOB --key proof
computecloud job evaluate-graph --config evaluator.yaml --id JOB --file evaluation.json
computecloud job recover-graph --config proposer.yaml --id JOB --file recipe.json
```

替换模型、workspace/base_commit 和配置后运行；graph 查询提供实际计划、输出与评估版本。构造/修改 catalog 时重新计算其规范化摘要，不能按任意 JSON key 排序猜测。Hash 绑定测试和 CLI compile 是机械校验入口，不是业务真值。

`make ci-iu-graph` 同时要求旧 80 臂 pilot、新 72 臂覆盖反例、严格必测名单，以及真实 Server/双 Worker 进程的 FAIL→补证→Guard→新代→PASS 和重启后 CLI 重放证据。一个 Runner 上的进程不是独立双主机。

真实 Provider/aicloud/独立主机验收仍 NOT_RUN。精确生产 token 硬预算、附件、任意语义 Planner、记忆压缩、patch DAG 和外部 exactly-once 不在本合同范围。
