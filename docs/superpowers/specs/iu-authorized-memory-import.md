# IU-3 一次性跨代记忆授权

状态：代码/定向回归已完成，CI/最终独立审查待验收。基于 IU-2；只支持同项目、同 Goal 的历史代到下一活动代，不支持跨 Goal/租户共享。

新增 iu-graph-v2（v1 schema 不变）：imports 声明 grant_id 与目标 node_ids，最多 8 项，目标必须存在且不能重复绑定同一授权。原 Node/Worker invocation 协议不变；导入是受控冻结 read-view，不是新的 Task 或节点结果复用。

授权创建需要 jobs:read 和独立 goals:import。请求包含客户端幂等 ID、来源 job/node、精确 View hash、完整 record_id 依赖闭包、来源 validity revision、目标 generation、目标完整 plan digest。先固定 grant ID 写入目标图再编译摘要，避免授权摘要循环。来源授权与来源项目/Goal 在事务中复核。

授权是一次性发布批准，状态 ISSUED→CONSUMED 或 ISSUED→REVOKED；有效期不超过 Goal deadline。撤销只允许未消费授权；消费后不能倒改已冻结执行，需通过 IU-2 的独立失效权限追加反证。此接口不承诺持续授权租约。发行者 managed key 在消费事务也重查撤销/有效期/项目和 goals:import。

新代发布事务重查来源 revision、投影完整性、VALID 与无冲突、依赖闭包、模型/算元/Schema/模板/工作区/指令兼容，以及来源实际读取的所有源片段在目标图仍有相同 ID/hash。来源缺信息即拒绝，不信任模型自报的精简依赖。规则变化时不得导入依赖旧规则的 11000；不依赖旧规则、来源仍匹配的原始观察可作为 unchecked 记忆导入，但仍执行当前算元和独立业务评估。

导入 record 使用稳定的目标命名空间 ID，依赖与 supersedes 只能引用闭包内映射；validation 重置 unchecked，provenance 单独记录原 Goal/generation/node/View/record。原 View 不改写，原 Artifact 在授权存续与已接受导入期间分别 pin。一次性消费及 import snapshot 同事务；重放不产生重复快照或消费。

首次提交新 Goal 时禁止 imports；imports 只能通过 Guard 合法新代发布，并在节点激活前已冻结在图中，杜绝“先执行、后补记忆”。目标节点激活读取冻结导入快照，并与合法上游 View 显式合并；不读取可变 latest-memory。全量重算仍为默认，无 imports 的 v1 维持原行为。

拟议入口：POST /v1/jobs/{source}/graph/import-grants；POST /v1/jobs/{source}/graph/import-grants/{grant}/revoke；GET 同一 grant 路径查询授权与消费事实。CLI issue-import / revoke-import；新代发布继续既有 Guard API，使用 v2 图。迁移按下一可用 Server schema 版本追加授权、状态、快照/来源表。

验收：不完整闭包、失效/冲突、错源摘要、过时 revision、跨项目/Goal、撤销/过期/已消费授权、发行者 key 撤销、错计划/目标代均拒绝；合法导入保留来源并降为 unchecked；发布前后重启不重复消费/Task；旧代不能借相同 record ID 污染目标。真实效果另行测量。

首版保守限制：仅相同 node 声明；实际上下文含 Artifact inputs 时拒绝导入，直到有当前代输入相同的证明。相关 schema v22、API、CLI 与 pin 释放边界见 [v2 合同](../../contracts/iu-graph-v2.md)。
