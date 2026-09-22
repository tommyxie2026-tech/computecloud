# Agent Runtime v1 接口与事件契约

- 项目：computecloud
- 日期：2026-09-22
- 状态：目标契约；v0.1 已有生成的 Protobuf / Go 实现，尚未开放的扩展保留为设计
- 适用范围：Go 调度器、Go Worker、Codex / Claude Code 适配器
- 上层设计：[Go 多客户端 Agent RPC 调度实施方案](../design/agent-orchestration-go.md)
- 调度集成：[ADR-001](../adr/0001-sqlite-lightweight.md)；验收：[多节点 PoC](../validation/multi-node-poc.md)

本文的方法名和字段均为 computecloud 自定义协议。客户端原生字段由适配器转换，不向调用方承诺不同客户端拥有完全相同的能力。

## v0.1 实现说明

当前可调用方法和字段以 [runtime.proto](../../api/agent/v1/runtime.proto) 为准。注册、续租和命令 ACK 合并到 ConnectWorker 帧；序号采用非负 int64；SendInput/RespondApproval 返回 Unimplemented；session_ref/provider_ref 非空明确拒绝。未加入 Proto 的预算、附件、placement 等字段不在本版范围。完整差异和操作示例见 [运行指南](../implementation/v0.1-runbook.md)。以下章节保留完整目标语义，不应推定全部已经实现。

## 1. 基本约束

1. 身份来自认证上下文，不能相信请求体中的 owner_id。
2. 写操作必须有幂等标识和规范化 request_hash；同键异参必须拒绝。
3. Task、Attempt、Session 和原生 Turn 的 ID 不混用。
4. 持久化接受、Worker 收到、原生客户端接受和操作完成是不同状态。
5. 任务生命周期独立于单个 RPC 的 context；取消由显式请求或任务 deadline 触发。
6. 标识符按不透明字符串处理。序号和 generation 在 Protobuf 使用 uint64，JSON 表示使用十进制字符串，避免 JavaScript 精度损失。
7. 示例 ID、模型名、凭据引用和路径均为占位符，不代表真实资源。
8. 一个活动 server 以本机 SQLite 为权威；Worker 只通过 RPC 对账，不共享数据库文件。调度循环重试不能产生重复 Agent。
9. SQLite INTEGER 使用有符号 64 位，所有持久化 seq / generation / version 限制在 0 至 2^63-1；越界显式报错，不截断 Protobuf uint64。

## 2. 任务规格

| 字段 | 必需 | 语义 |
| --- | --- | --- |
| `project_id` | 是 | 已授权项目 |
| `idempotency_key` | 是 | 在身份 + 项目范围内唯一 |
| `engine` | 是 | codex / claude |
| `runtime_profile` | 是 | codex_exec / claude_print；交互 profile 后续启用 |
| `model` | 否 | 省略时使用平台固定配置；保存解析后的实际值 |
| `provider_ref` | 否 | 受信 provider 配置引用；与 model、runtime_profile 共同校验并冻结配置版本，不接受任意 endpoint |
| `credential_ref` | 是 | 已授权凭据引用；不含密钥 |
| `workspace` | 是 | 仓库引用、固定基线 commit、隔离模板 |
| `input` | 是 | 初始文本和经过授权的附件引用 |
| `policy_ref` | 是 | 不可变策略版本引用 |
| `required_capabilities` | 否 | 调度前必须满足的能力集合 |
| `session_ref` | 否 | 明确恢复目标；验证身份、版本、工作区与位置 |
| `parent_task_id` | 否 | 用户继续工作或跨任务关联；不表示自动重试 |
| `timeout_seconds` | 否 | 平台允许范围内的运行期限 |
| `budget` | 否 | 任务预算；记录计量口径与是否可硬限制 |
| `acceptance_profile` | 是 | 平台受信的验收模板 |
| `priority` | 否 | 平台授权范围内的优先级 |
| `placement_policy_ref` | 否 | 受信节点池、标签、隔离和资源要求；省略时使用项目默认并保存其版本 |
| `retry_policy_ref` | 否 | 允许的 Agent 重试分类、次数与预算；省略时采用不自动重跑的默认策略 |

JSON 表示示例：

```json
{
  "project_id": "project-example",
  "idempotency_key": "fix-login-20260922-001",
  "engine": "codex",
  "runtime_profile": "codex_exec",
  "credential_ref": "credential-example",
  "workspace": {
    "repository_ref": "repository-example",
    "base_commit": "0123456789abcdef0123456789abcdef01234567",
    "isolation_profile": "trusted-worktree-process"
  },
  "input": {
    "text": "修复登录模块的已知测试失败，并给出变更说明。"
  },
  "policy_ref": "repo-development-v1",
  "required_capabilities": ["event_stream", "cancel"],
  "timeout_seconds": 1800,
  "acceptance_profile": "go-unit-tests-v1",
  "priority": 0
}
```

base_commit 必须解析为实际可访问提交；示例中的值不可直接用于提交任务。运行参数使用结构化配置，API 不暴露任意 shell 命令、任意宿主路径或未审核的额外 CLI flags。

响应及存储包含 `resolved_spec`：实际 engine / runtime_profile / provider / model、凭据引用、配置版本、基线 commit、策略版本和资源要求。创建 Task 后不可变；自动重试不换模型、账号或基线。未指定 provider_ref / model 时由服务端受信配置解析；缺少有效映射则拒绝提交。

节点暂时离线或槽位不足时任务保留 QUEUED，GetTask 提供 `scheduling_blocker`（如 NO_READY_WORKER、CAPACITY_EXHAUSTED、SESSION_PINNED、SCHEDULER_PAUSED）和最后评估时间。已知规格永远不被当前平台支持时应在提交阶段返回能力错误。排队期限耗尽时先确认没有已分配执行，再结束任务。

## 3. 调用方 API

| RPC | 请求要点 | 响应与语义 |
| --- | --- | --- |
| `SubmitTask` | 任务规格 | 持久提交后返回 task_id、QUEUED 和已有/新建标识 |
| `GetTask` | task_id | 状态、当前 Attempt、session_ref、resolved_spec、scheduling_blocker、最后事件序号、结果与错误 |
| `WatchEvents` | task_id、after_seq | server streaming；返回所有已提交且 seq 更大的可见事件，然后持续跟随 |
| `SendInput` | task_id、control_id、attempt_id、generation、mode、expected_turn_id、input | 返回控制状态；不承诺输入已经执行 |
| `RespondApproval` | task_id、control_id、request_id、attempt_id、generation、decision、request_digest | 必须匹配仍待处理的审批请求与授权身份 |
| `CancelTask` | task_id、control_id、reason | 返回当前状态；运行任务先进入 CANCELING |
| `ListArtifacts` | task_id、分页游标 | 返回有权限访问的产物元数据和短期访问引用 |

P0 部署所有查询/提交/取消能力；SendInput 与 RespondApproval 可以保留协议方法，但不支持的 profile 返回明确能力错误。

Task 已成功或失败后，SendInput 不能使其重新进入运行状态；应调用 SubmitTask 创建新任务，按需携带 session_ref 与 parent_task_id。

WatchEvents 在终态事件发送完成后正常结束。after_seq 大于当前可见水位时返回参数错误；低于可回放最小水位时返回 `CURSOR_EXPIRED`。调用方必须处理重连时的重复事件。

## 4. 输入与审批语义

| 输入模式 | 含义 | 必需的能力 |
| --- | --- | --- |
| `queue_next` | 排队为后续原生轮次输入；不改变当前轮次 | queued_input |
| `steer_current` | 向仍在执行的目标轮次追加指令 | steer_current |
| `answer_request` | 回答已存在、可响应的输入请求 | user_input；必须带 request_id |

不支持时不能自动切换语义，例如不得把 steer_current 改成取消后重启。当前轮次校验失败返回 `STALE_TURN`。

`queue_next` 的平台接受只表示控制请求已入队。目标会话结束或 Task 已进入终态时，未执行输入必须得到明确失败事件，不能无声丢失或进入别人的会话。

控制请求状态：

| 状态 | 含义 |
| --- | --- |
| `ACCEPTED` | 已持久化控制意图 |
| `DELIVERED` | Worker 已持久接收 |
| `APPLIED` | 原生执行器明确接受，或可证明目标动作已完成 |
| `REJECTED` | 能力、状态、权限或原生协议拒绝 |
| `UNKNOWN` | 原生写入/接受状态不明；禁止盲目再次注入 |

控制请求在平台可去重，不意味着原生协议自动具备端到端幂等。如果 Worker 在“已向客户端写入、尚未持久确认”之间崩溃，必须核查原生状态；不能仅因重发了同一个 control_id 就宣称没有重复输入。

审批至少支持 `allow_once` / `deny`。批准必须绑定 request_id、动作摘要、Attempt 代次和有效期限；参数变化需要新请求，不能复用旧批准。普通 SendInput 不能代替批准。

Task 因原生审批或问题暂停时保持运行环境和租约，单独应用等待期限。超时按策略拒绝或停止执行；取消任务时使尚未完成的请求失效。P0 批模式不宣称具备此暂停回传能力。

## 5. 事件契约

控制平面对外事件的 JSON 示例：

```json
{
  "schema_version": "1",
  "task_id": "task-example",
  "attempt_id": "attempt-example",
  "generation": "1",
  "event_id": "event-example",
  "worker_seq": "42",
  "seq": "57",
  "type": "tool.completed",
  "occurred_at": "2026-09-22T08:00:00Z",
  "recorded_at": "2026-09-22T08:00:01Z",
  "payload": {
    "tool_name": "shell",
    "status": "succeeded",
    "exit_code": 0,
    "output_artifact_id": "artifact-example"
  }
}
```

Worker 原始上报不含全局 seq / recorded_at，由控制平面在持久化时赋值。控制平面自产生的排队/调度事件使用独立 producer 标识，worker_seq 缺省；尚未分配 Attempt 时 attempt_id / generation 也可缺省。`schema_version` 与数字字段的 JSON 表示约定保持一致。

| 类型 | payload 核心内容 | 使用规则 |
| --- | --- | --- |
| `task.state_changed` | from、to、reason、version | 由权威状态机产生 |
| `attempt.started` | runtime_version、session_ref、workspace_ref | 运行实例已初始化 |
| `message.delta` | message_id、text | 可选；不是所有 runtime 都提供 token 级增量 |
| `message.completed` | message_id、text 或 artifact_ref | 完整文本的权威记录；避免与 delta 重复拼接 |
| `tool.started` / `tool.completed` | tool_id、类别、状态、退出码与产物引用 | 不根据自然语言推断工具成功 |
| `workspace.changed` | 文件列表、diff_ref、基线 | 不必把完整文件内容装进事件 |
| `input.required` | request_id、问题/选项、turn_id、expires_at | 仅声明了响应能力的适配器可产生 |
| `approval.required` | request_id、动作摘要、策略与期限 | 不泄漏密钥；绑定具体动作 |
| `control.updated` | control_id、状态、原生确认或错误 | 区分接受与执行 |
| `usage.updated` | token 分类、来源、计量范围、费用估计 | 缺失不填 0；恢复会话累计值不能当本轮增量重复相加 |
| `artifact.created` | artifact_id、类型、SHA-256、大小 | 已登记可访问产物 |
| `attempt.completed` | 原生结果、验收、停止原因 | 不是自动等同于 Task 成功 |
| `task.completed` | 唯一终态、结果引用、错误 | 所有前序关键事件持久化后产生 |
| `runtime.diagnostic` | 原始事件类型、受限原始载荷引用 | 未知事件可保留，禁止据此猜终态 |
| `task.scheduling_blocked` | reason、policy_version、evaluated_at | 无可用资源或调度被暂停；不伪造执行失败 |

大文本、日志和二进制内容走产物存储。适配器保留必要原生字段以便排障，但不得向无权限订阅者暴露原始凭据或其他任务内容。

顺序和去重以序号/唯一键为准，时间戳仅用于观测。发生事件缺口时明确记录完整性错误；不能把不完整执行报告为成功。

## 6. Worker 接口与执行身份

| RPC | 关键语义 |
| --- | --- |
| `RegisterWorker` | 报告节点身份、worker_epoch、slots、OS/arch、版本、profile、capabilities、已验证模型配置、工作区访问能力；身份经 TLS + 独立节点令牌认证；可选 mTLS |
| `ConnectWorker` | Worker 发起双向 gRPC 流；协商协议、connection_epoch 和水位；承载控制命令及其收件确认 |
| `HeartbeatWorker` | 报告节点就绪、draining、可用资源、配置 inventory_revision 和故障；不替代 Attempt 续租 |
| `StartAttempt` | 参数含 task_id、attempt_id、generation、租约、任务快照摘要；Worker 持久去重后启动 |
| `InspectAttempt` | 返回启动日志、实际执行环境、进程身份、原生会话、事件水位及清理状态 |
| `ApplyControl` | 按 control_id、代次与 request_hash 去重；明确原生接受状态 |
| `StopAttempt` | 幂等发起停止；返回停止中或已停止，不等于终态提交 |
| `RenewLease` | 校验节点身份、attempt_id、generation、租约令牌；返回新的 TTL |
| `ReportEvents` | 批量上报事件；ACK 为已经提交的连续 worker_seq 水位 |
| `CompleteAttempt` | 附 final_worker_seq、原生状态、验收、产物、退出/清理证据；校验当前代次后提交 |

控制平面提供 RegisterWorker / ConnectWorker / HeartbeatWorker / RenewLease / ReportEvents / CompleteAttempt；Worker 主动连接。StartAttempt / InspectAttempt / ApplyControl / StopAttempt 是 Worker 命令处理方法，经反向流下发并关联响应。P0 同机和 P1 多机使用同一通路与持久收件箱，不开发第二套直连传输。

### 6.1 注册与反向流

Worker 登记的 `worker_id` 必须与已认证的节点令牌绑定身份相符；启用 mTLS 时也须核对证书身份，`worker_epoch` 标识该次 Worker 进程实例，`connection_epoch` 由服务端分配并标识当前有效连接。二者都不是 Attempt generation。Worker 重启后先核查并认领已有执行环境，再开放新槽位；新 worker_epoch 不证明旧 Agent 进程已经停止。

模型可用性同时依赖版本、配置和凭据就绪状态。注册仅上报授权范围内的引用与探测状态，不传输凭据。CLI 可执行或 `--version` 成功不能证明模型调用已鉴权；验证结果要记录时间，过期或失败后不得继续声明 READY。

命令信封包含：

| 字段 | 约束 |
| --- | --- |
| `command_id` | 控制平面持久生成；重连重发保持不变 |
| `command_type` | start / inspect / control / stop；未知类型拒绝 |
| `worker_id`、`worker_epoch`、`connection_epoch` | 必须属于当前已认证路由；旧连接写入被拒绝 |
| `task_id`、`attempt_id`、`generation` | 绑定唯一执行；不自动改投新 Attempt |
| `request_hash` | 覆盖业务参数及执行身份；不包含会随重连变化的路由信封字段 |
| `expires_at` | 命令接受期限；超时后核查，不推定此前没有执行 |
| `payload` | 版本化的不可变业务请求；租约更新使用独立续租消息 |

Worker 持久记录 command_id + request_hash 后回复 `RECEIVED`；这是投递确认，不是原生执行确认。已接收命令的处理结果也要持久化，重复交付返回原结果或当前状态。同键异参拒绝。对 start 命令还必须按 attempt_id + generation 去重，即使收到不同 command_id 也不能再启动。

断流后，控制平面重放未确认命令，Worker 重放未确认结果；路由代次变化不改变业务去重键。开始新 worker_epoch 前，必须 Inspect 持久启动日志和真实容器/cgroup，经过核查才能把旧执行绑定到新实例。不得简单把旧启动命令改写成新 epoch 后重放。

命令/心跳不允许被大量文本输出无限阻塞。ReportEvents 使用独立的有界批量 RPC 和发送队列；必要时与控制流分开连接，避免网络流控相互阻塞。数据传输失败按应用事件日志水位补传，不能挤占取消通道。TLS、节点认证、重连退避和连接保活均需配置，但连接保活成功不等于 Agent 租约有效。

租约令牌仅在受保护 RPC 中传递，不写入事件和日志。StartAttempt 重复请求必须返回已有执行状态；同身份异参返回冲突。

启动日志先于实际启动持久化；执行环境创建与启动之间仍可能崩溃，因此必须能查询容器/cgroup 或具有启动身份的进程。拿不到确定结论时返回 UNKNOWN 并进入核查。

### 6.2 完成提交规则

- `CompleteAttempt` 对相同完成摘要幂等；摘要冲突必须拒绝并审计。
- 过期 generation 不得改变当前 Task。
- 未收到 final_worker_seq 前的全部关键事件时返回待补齐状态。
- 取消已获得权威状态后，晚到成功回报不能改成 SUCCEEDED。
- 运行环境仍有不应存活的后台进程时，不能声明清理完成。
- Artifact 哈希和引用必须已登记；不能让 Task 成功后才发现主要产物不存在。
- 调度扫描、RPC 超时或重试都不能绕过本规则；数据库提交成功后才能 ACK。

### 6.3 租约和取消证据

RenewLease 必须绑定当前 Attempt 及授权 Worker 实例；HeartbeatWorker 不得批量隐式续租所有 Attempt。租约失效触发 RECONCILING 或继续既有 CANCELING，资源与 Session 预留保持隔离，直到获得停止/隔离证据并核查副作用。

InspectAttempt 至少返回 `runtime_state`（NOT_STARTED / RUNNING / EXITED / UNKNOWN）、持久启动记录、环境身份、进程启动身份、当前所有者、最后持久事件水位和 `cleanup_state`。停止证据包含执行环境引用、核查时间、核查方法和残留进程结果；PID 本身不构成证明。无法联系节点时不得编造停止证据。

CancelTask 对未分配任务以事务 CAS 结束并阻止随后准入；已分配任务持久写入 CANCELING 和 worker_commands 中的停止记录。StopAttempt 接收成功不能直接变成 CANCELED。通过进程/容器监督证明清理完成后才提交终态；过期的 start 命令亦不得复活已取消执行。

Worker 一旦接受 stop，必须持久化该 Attempt 的停止墓碑。乱序晚到的 start 即使尚未创建过进程也返回已停止；只有新的、经过授权的 Attempt 身份才能启动新的执行。

## 7. 能力协商

| capability | 含义 |
| --- | --- |
| `event_stream` | 可输出规范事件，不隐含 token 级增量 |
| `text_delta` | 可提供明确文本增量事件 |
| `resume` | 在满足身份、版本与本地状态条件下恢复 |
| `cancel` | 可停止执行并提供清理确认 |
| `queued_input` | 可在同一运行会话排队后续输入 |
| `steer_current` | 可向目标活动轮次追加输入 |
| `interrupt_turn` | 可原生中断某轮；不隐含撤销副作用 |
| `approval_response` | 可识别并回应原生审批请求 |
| `user_input` | 可识别并回答原生问题请求 |
| `portable_session` | 跨 Worker 恢复已通过完整验证 |

Capability 由 profile + 客户端版本 + 适配器版本 + 实际运行环境共同决定。声明能力前必须通过相应验收用例；不能仅凭产品名称注册全部能力。

P0 最小要求为 event_stream + cancel；resume 在原生状态目录和工作区已正确保留并经过集成验证后启用。未支持的能力应影响调度，而不是运行到一半才静默降级。

## 8. 错误模型

RPC 层错误与异步任务错误分离。SubmitTask 成功只表示持久化接受；之后客户端鉴权失败通过事件与 GetTask 返回，不回写为之前 RPC 的失败。

| 领域错误 | gRPC 映射（同步请求） | 处理 |
| --- | --- | --- |
| `INVALID_SPEC` | INVALID_ARGUMENT | 修改参数 |
| `AUTHENTICATION_REQUIRED` | UNAUTHENTICATED | 恢复平台/节点身份 |
| `ACCESS_DENIED` | PERMISSION_DENIED | 校验项目、凭据、目录授权 |
| `IDEMPOTENCY_CONFLICT` | ALREADY_EXISTS | 同键异参；使用原请求或新键 |
| `CAPABILITY_UNAVAILABLE` | FAILED_PRECONDITION | 当前 profile 不支持；部署未实现的方法可返回 UNIMPLEMENTED |
| `STALE_ATTEMPT` / `STALE_TURN` | FAILED_PRECONDITION | 获取当前状态，不能直接重发到新目标 |
| `RESOURCE_LIMIT` | RESOURCE_EXHAUSTED | 等待额度或按策略调度 |
| `CURSOR_EXPIRED` | OUT_OF_RANGE | 获取快照与可用回放水位 |
| `TRANSIENT_TRANSPORT` | UNAVAILABLE | 幂等重试或重新订阅 |
| `CONTROL_DEADLINE` | DEADLINE_EXCEEDED | 查询动作状态，不能推定没有执行 |
| `WORKER_LOST` | 异步事件 | 核查，不能立即启动重复执行 |
| `STALE_WORKER_EPOCH` | FAILED_PRECONDITION | 重新注册并核查真实执行，不能直接重放启动 |
| `SESSION_LOCATION_UNAVAILABLE` | 异步调度阻塞 | 等待原节点或显式新任务，不自动跨节点复制会话 |
| `STORAGE_UNAVAILABLE` | UNAVAILABLE | 数据库未成功提交则不 ACK；恢复后同键核查/重试，不重复创建 Task |
| `CLEANUP_UNCONFIRMED` | 异步核查结果 | 保持 CANCELING / RECONCILING，不释放执行预留 |
| `PERMISSION_BLOCKED` | 异步事件 | P0 失败；调整授权后新建任务 |
| `PROTOCOL_LIMIT` / `PROTOCOL_ERROR` | 异步事件 | 保留受限诊断并停止对应执行 |
| `VERIFICATION_FAILED` | 异步事件 | 保留变更与验收证据 |

错误携带 `code`、安全可展示的 `message`、`retry_class` 和可选的 `native_error_ref`。`retry_class` 至少区分 never、after_delay、after_reconcile、after_user_action；不使用一个 retryable 布尔值驱动所有自动重跑。

## 9. 版本与验收要求

- Protobuf package 计划为 `computecloud.agent.v1`；go_package 在 go.mod 初始化后按实际仓库模块路径确定。
- v1 新增字段采用向后兼容方式；删除字段保留编号，不复用 enum 数字。
- 明确区分未提供、unknown 和真实零值，尤其是退出码、用量和费用。
- 未知事件可忽略其展示或保留诊断，但未知控制输入必须拒绝。
- 控制协议升级先验证服务端与 Worker 混合版本；按能力调度，不要求瞬时全量升级。
- 最少验证重复提交、重复启动、控制重复/未知状态、事件重传、游标补读、取消竞态、租约过期与原生事件不完整等行为。

本文未宣称协议已编译、服务已运行或真实客户端已通过上述验收。后续编码应把本契约转换为实际 `.proto`、领域类型、数据库约束和行为测试。

## 10. SQLite 与内置队列契约

- SubmitTask 在同一事务保存任务、幂等键和规范化摘要；QUEUED 行就是持久任务队列。进程内 channel 只负责唤醒，丢失唤醒不丢任务。
- 调度在短 `BEGIN IMMEDIATE` 事务中核对状态、节点和额度，写 Attempt、Session 持有者及 worker_commands；提交后才发送启动命令。
- 待发命令允许至少一次交付，Worker 持久去重；启动与停止命令都必须支持 ACK 丢失后的重放。
- CancelTask 和 CompleteAttempt 的状态、关联事件以及资源释放必须在同一事务内完成，按状态版本和唯一约束处理竞态。
- server.db 和 worker.db 是独立本地数据库；不做跨文件原子事务，不让远端节点通过共享文件系统连接。
- RPC、LLM、构建、事件等待都在事务外。WatchEvents 每次查询有限批次并关闭 rows，再发送网络数据；不能让慢消费者持有数据库连接/事务。
- server 重启后先对账原活动 Attempt，重建 Worker 连接；持久命令继续发送，状态不明时保持 RECONCILING。已确认事件的重放以 SQLite 提交为准。
- 单活动 server 由数据目录独占进程锁保护；第二个 server 打开同目录应失败。SQLite 文件锁不代替应用单实例约束，也不用于多节点选主。
- 备份恢复使用维护模式：确认旧 server 停止、暂停派发，与 Worker 的持久执行记录对账后再恢复调度；从旧备份恢复不保证找回备份之后提交的数据。

## 11. 产物传输

首版产物存储在 server 本机目录，不依赖对象存储。提供 `UploadArtifact`（客户端流式）和 `DownloadArtifact`（服务端流式）；查询仍使用 ListArtifacts。初始元数据包含 task_id、attempt_id、generation、artifact_id、类型、预计大小和 SHA-256。

上传校验执行身份、配额和路径授权，只能使用服务端生成的相对路径；完成临时文件写入、大小/哈希校验、持久化和原子改名后再登记 artifacts。相同 artifact_id + 相同摘要重复上传返回已登记结果，同 ID 不同内容拒绝。首版断点失败可重传整个有界文件，暂不实现复杂分片续传。

下载校验任务权限，不向调用方公开任意宿主路径。流量和文件大小受限，不能阻塞取消/心跳通道；大输出不会直接塞入 SQLite 行或无限累积到内存。完成任务前必须确认主要产物已登记。
