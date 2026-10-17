# IU-2 持久失效事件

状态：实施中；以下为本阶段决定，不代表已验收。

事件以 (goal_id, generation, id) 为幂等命名空间，绑定 expected_revision、reason（RULE_CHANGED/SOURCE_REVISED/COUNTEREVIDENCE）、evidence_sha256、原来源 ID/hash 或已存在 record ID。最多 64 个事件、每事件最多 32 个来源/32 个 record；无匹配目标拒绝。同键同内容重放成功，不同内容冲突。更新/删除事件禁止。

`goals:invalidate` 与 `jobs:read` 同时满足才允许写。Owner/project 和活动 generation 必须匹配；managed API key 在提交事务内重查有效期、撤销、项目和 scope。静态身份随启动配置冻结，不声称支持热撤销。

SQLite IMMEDIATE 事务提交事件、revision 和 RECORDED 进度；事件一旦提交就保守禁止该代激活、准入和 PASS。后台将来源/record 失效投影到不可变 read-view 的新快照，传递到派生记录，原 View/Artifact/Task 历史不改写。每轮最多应用 4 个事件，避免失效投影挤占调度；余量保持 RECORDED。进度和投影另表存放，事务失败保持 RECORDED，重启可重复应用。APPLIED 不代表旧证据重新有效。

活动 Job 按已有 STOPPING/排空路径失败；已执行成功的 Job 保持 SUCCEEDED，但不能以失效证据 PASS。FAIL/UNKNOWN 必须绑定当前 validity revision；重规划提交事务复核评估 revision，已有 Guard 决策不能绕过新事件。保留全量重算。

终结 Goal 不允许业务失效写入；显式 audit_only 事件可保留迟到反证，不修改 Goal、Task 或历史评估、不触发失效投影。旧代写入拒绝。新代有独立 revision；旧事件不会自动授予新代任何复用资格。

HTTP: POST /v1/jobs/{id}/graph/invalidations；GET /v1/jobs/{id}/graph/validity 查询事件/应用状态；GET /v1/jobs/{id}/graph/memory/{node_id} 查询当前投影。GraphStatus 增加 validity_revision；业务评估增加 expected_validity_revision。CLI 增加 invalidate-graph / graph-validity，复用现有鉴权入口。

验收：两跳来源失效、事件提交后重启、幂等/冲突、撤权/CAS/旧代、原 View 不变、迟到准入拒绝、事件先提交则 PASS 拒绝、PASS 先提交则仅能记录 audit_only。CI 真实进程模拟与旧合同回归分别取证。
