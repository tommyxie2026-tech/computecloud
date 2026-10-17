# IU graph v2：一次性授权记忆导入

状态：代码已实现，等待对应提交的 CI 与最终独立审查；真实 Provider/aicloud/独立主机验收未完成。v2 继承 v1 有限图语义，新增 `imports`；v1 JSON Schema 不变，Worker invocation 仍为原协议，Server schema v22。

```json
{"imports":[{"grant_id":"grant_a","node_ids":["extract"]}]}
```

最多 8 个授权，每项 1～32 个已声明节点；目标图必须使用 `version: iu-graph-v2`。只支持同项目、同 Goal 历史代到下一代，首次提交新 Goal 拒绝 imports。默认没有导入，继续全量重算。导入仅提供 unchecked 输入，不跳过 Task，不产生新的 verified 结论。

1. 先选固定 grant ID，将它与目标节点写入 v2 图，调用 `graph.Compile` 得到规范化目标 `Plan.Digest`。摘要算法以 Go 合同为准，不能直接 hash 任意格式的 JSON。
2. 查询来源 `GET /v1/jobs/{source}/graph/memory/{node}` 和 `/graph/validity`，取得精确 View hash、revision，列出完整记录依赖闭包，含 supersedes。
3. 具有 jobs:read、goals:import 的独立主体创建授权：`POST /v1/jobs/{source}/graph/import-grants`。

```json
{"id":"grant_a","node_id":"extract","view_sha256":"<64位摘要>","record_ids":["raw"],"expected_revision":0,"target_generation":2,"target_plan_sha256":"<规范化计划摘要>","expires_at":1800000000000}
```

`expires_at` 为 Unix 毫秒，必须晚于当前时间且不超过 Goal deadline。来源 Job 必须已终结且未取消，Goal 必须仍允许后续计划。每 Goal 最多 64 个授权，记录闭包最多 32 条。相同 ID/相同内容幂等；不同内容冲突。同一来源 Job 可通过 `GET /graph/import-grants/{grant}` 查询。

4. 使用既有 `/goal/replans` Guard API 提交 FAIL 证据与完整目标图。新代发布事务复查授权、来源有效性 revision、精确 View、全部闭包、命名空间和执行绑定；任何失败整次发布回滚。授权绑定完整目标计划，不可借给其他代/计划。
5. 原子写入 immutable snapshot/provenance，并将授权从 ISSUED 改为 CONSUMED。新代节点激活读取冻结快照，合并上游 memory 后正常创建 Task；最终 Goal 仍独立验收。

首版仅接受相同节点定义、算元、Schemas、Workspace、规则、模板和 command binding；来源实际读取的每个源 span，以及所选记录的 Sources，都必须在目标中保持同 ID/hash。含 Artifact inputs 的旧 Context 暂时拒绝导入，直到能提供当前代输入相同的证明。来源缺失、冲突、INVALID/NEEDS_REVALIDATION 均拒绝；不能导入依赖旧折扣规则的 11000 来替代新规则计算。原始观察只有在其全部读取依赖保持相同时才能导入。

记录新 ID 由目标 Goal/generation/node/grant 与旧 ID 确定性映射；所有依赖一并映射，旧 View/record 不改写。provenance 保留源 Job/Goal/generation/node/View/revision/record mapping。所需来源 Artifact 保守全部 pin（最多 128 个），包括之前导入的来源 pin；已接受导入 pin 永久保留，后续 retention 需另立合同。

`POST /graph/import-grants/{grant}/revoke` 只能撤销未消费授权。过期授权有界清理，每轮最多 16 项并释放 grant 临时 pin。已消费授权返回 `IMPORT_GRANT_CONSUMED_USE_INVALIDATION`；这是一次性发布批准，后续反证使用 IU-2 失效事件，不承诺持续租约。managed issuer key 在创建和消费事务都复查撤销、期限、project、scope。

CLI：`job issue-import --id SOURCE --file grant.json`、`job get-import --id SOURCE --key grant_a`、`job revoke-import --id SOURCE --key grant_a`。密钥及授权 digest 不在查询响应中输出。
