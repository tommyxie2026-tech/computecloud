# Job、MCP 与模型网关契约 v0.2

日期：2026-09-22。状态：v0.2 已实现；本地验证与真实环境待验收项分别记录。配套[主设计](../design/gateway-mapreduce-v0.2.md)和[实施计划](../implementation/v0.2-plan.md)。

本文件定义 computecloud 自有 Job 接口；`/v1/jobs` 和下列 MCP 工具不是 Codex 原生方法。gRPC 扩展见 [runtime.proto](../../api/agent/v1/runtime.proto)，Job HTTP/MCP 与网关见本文及[运行指南](../implementation/v0.2-runbook.md)。请求 JSON Schema 和可解析示例位于 [v0.2 示例目录](../examples/v0.2/README.md)。

## 1. HTTP 任务接口

统一使用 HTTPS、`Authorization: Bearer <平台 Token>`、UTF-8 JSON。对象权限按 owner/project 检查，所有子任务和产物查询沿用同一权限。Job JSON 为独立 wire 格式，不直接暴露 ProtoJSON 的 bytes 编码。

| 方法与路径 | 权限 | 请求/响应 |
| --- | --- | --- |
| `POST /v1/jobs` | `jobs:submit` | `Idempotency-Key` + JobSpec；新建 202，重复同参 200 |
| `GET /v1/jobs/{id}` | `jobs:read` | Job 摘要、阶段计数、blocker、停止原因、事件水位 |
| `GET /v1/jobs/{id}/tasks?after=&limit=` | `jobs:read` | 按 task_id 排序，默认 50、最大 100 |
| `GET /v1/jobs/{id}/events?after_seq=&limit=` | `jobs:read` | Job 事件页，默认 100、最大 500，无长轮询 |
| `POST /v1/jobs/{id}/cancel` | `jobs:cancel` | `{control_id, reason}`；202 表示受理，200 表示已终态或同操作重放 |
| `GET /v1/jobs/{id}/result` | `jobs:read` | 200 返回终态执行结果与当前用量快照；未结束返回 409 `JOB_NOT_FINISHED` |
| `GET /v1/jobs/{id}/artifacts` | `jobs:read` | 必要的 Map/Reduce 产物元数据，分页 |
| `GET /v1/jobs/{id}/artifacts/{artifact_id}` | `jobs:read` | 下载该 Job 的获准文件，返回长度和 SHA-256 |
| `GET /v1/capabilities` | 已鉴权 | API 版本、限制、可用 Job 模式和网关能力，不含密钥或其他租户信息 |

核心 HTTP 只用分页轮询；细粒度 Task 流继续使用现有 `WatchEvents`。Job 轮询建议 2 秒起步，按响应 `poll_after_ms` 退避，上限 10 秒；这不是 Worker 租约频率。

请求体最大 256 KiB，单 input 文本额外限制 UTF-8 64 KiB；幂等键/control_id 1–128 个 ASCII 字符，作用域分别为 owner/project 与 Job。未知 JobSpec 字段拒绝。model、credential、policy、acceptance 必须显式引用已授权配置；没有符合能力的在线 Worker 不阻止接受，而是返回排队 blocker。

### 1.1 JobSpec

| 字段 | 约束 |
| --- | --- |
| `schema_version` | 固定 `v0.2` |
| `project_id` | 已授权项目 |
| `mode` | `single` 或 `map_reduce` |
| `workspace` | `repository_ref` + 40/64 位小写十六进制 `base_commit` |
| `input.text` | 整体任务说明；由各 Task 继承 |
| `execution` | 仅 single：engine、runtime_profile、model、credential_ref、policy_ref、acceptance_profile |
| `map.parallelism` | 1–8；不得超过服务配置和项目额度 |
| `map.partitions[]` | 1–32 项；各自包含 key、scope_paths、input、execution |
| `reduce` | strategy、input、execution；strategy 为 report_merge_v1 或 patch_merge_v1 |
| `limits.timeout_seconds` | 1–86400；从 Job 持久接受时开始计时 |
| `limits.max_attempts_per_task` | 核心固定 1；R1 开放前其他值拒绝 |

single 禁止 map/reduce 字段，map_reduce 禁止顶层 execution。partition key 在一个 Job 内唯一；以小写字母开头，仅含字母/数字/下划线/连字符，最长 64 字符。`_single`、`_reduce` 由系统保留。所有分片都必需，缺少一个不能提前进入 Reduce。

规范化请求由固定版本 Go 类型编码：字符串原样保留，commit 已为小写，map partitions 按 key 排序；忽略 JSON 成员顺序但不忽略字符串中的空白。拒绝重复 JSON 成员名，避免不同解码器含义不同。不接受浮点数字段。`request_hash` 对该规范形式计算 SHA-256；`spec_hash` 还包含解析后的运行/策略/验收摘要和共同 deadline。重放提交先比较请求摘要，再返回已冻结的规格，不用当前配置重算原 spec_hash。

### 1.2 接受与查询

接受响应示意（字段值是示例）：

```json
{
  "job_id": "job_01",
  "state": "QUEUED",
  "mode": "map_reduce",
  "existing": false,
  "last_seq": "1",
  "poll_after_ms": 2000,
  "links": {
    "self": "/v1/jobs/job_01",
    "events": "/v1/jobs/job_01/events",
    "result": "/v1/jobs/job_01/result"
  }
}
```

GET Job 额外返回 `created_at_ms`、`updated_at_ms`、`deadline_ms`、`version`、`counts`、`scheduling_blockers`、`stop_reason`、`usage`。时间戳、事件序号和版本作为十进制字符串传输，避免 JavaScript 整数精度损失；数量、限制使用安全范围内的整数。

`counts` 按 stage 和 Task state 聚合当前数据库事实；不把完成百分比伪装成准确剩余时间。`usage.coverage` 为 `complete`、`partial` 或 `unavailable`，未计量总数为 null。核心没有完整计量时不得返回 0 token。Job 执行结果发布后不改写；usage 查询独立用量表，是可随后结算完善的快照。

终态结果含 `state`、`error_code`、`stop_reason`、`final_artifacts`、`child_failures`、`base_commit`、`manifest_sha256`、`usage`。失败/取消也可下载已获准的诊断产物，但 `final_artifacts` 不冒充成功报告。大报告通过下载引用提供，MCP 不把 tar 包内嵌进文本上下文。

### 1.3 错误和取消

任务 API 的错误体为 `{ "error": { "code": "...", "message": "...", "request_id": "..." } }`；不携带 Token、秘密文件路径或原始内部错误。

| HTTP | code 示例 | 含义 |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` / `UNSUPPORTED_FEATURE` | 错误字段、坏路径、要求未开放能力 |
| 401 / 403 | `UNAUTHENTICATED` / `PERMISSION_DENIED` | 未认证 / 无接口或提交权限 |
| 404 | `NOT_FOUND` | 不存在或没有该对象读取权限 |
| 409 | `IDEMPOTENCY_CONFLICT` / `JOB_NOT_FINISHED` | 同键异参 / 结果尚未就绪 |
| 413 | `REQUEST_TOO_LARGE` | 请求或输入越界 |
| 429 | `GATEWAY_BUSY` | 当前用于模型并发准入；Job 接收速率/队列上限暂未实现 |
| 503 | `STORAGE_UNAVAILABLE` / `MAINTENANCE` | 无法持久接受或正在维护 |

同一 control_id、相同规范请求返回当前 Job；异参返回 409。取消幂等证据写入 job_events 的 operation_key/hash，与停止意图同事务提交。已终态 Job 不改变状态，但新控制键仍记录，保证再次使用同键异参时可检测冲突。

直接 `CancelTask` 一个 Job 管理的子 Task 返回 `FAILED_PRECONDITION/MANAGED_JOB_TASK`，调用方应取消整个 Job；Job 控制器使用内部取消函数。独立 Task 行为不变。用户不能通过公开 Task 提交接口自行设置 job_id、stage 或系统保留幂等前缀 `__job/`。

## 2. MCP 工具与生命周期

HTTP 路径 `/mcp`，采用 2025-11-25 Streamable HTTP 基线；官方 Go SDK 固定 v1.8.0，真实客户端兼容验收仍待完成。支持 `initialize`、`notifications/initialized`、`ping`、`tools/list`、`tools/call`；不提供 sampling、模型请求反向回调或实验性任务扩展。

| 工具 | 参数 | 结果 |
| --- | --- | --- |
| `submit_job` | `idempotency_key`、`spec`（同 JobSpec） | Job ID 与短摘要；不等待 Agent 执行结束 |
| `get_job` | `job_id`、可选 `after_seq` | 摘要及最多 100 个阶段事件、next_seq/has_more |
| `cancel_job` | `job_id`、`control_id`、`reason` | 受理/当前状态，不声称进程已停止 |
| `get_result` | `job_id` | 终态摘要和产物引用；未结束返回明确业务错误 |

各工具提供输入和输出 Schema，结果使用 structuredContent，并附简短文本以兼容客户端。合法调用的业务失败返回工具结果 `isError=true`；JSON-RPC 方法/参数错误使用标准协议错误。工具标注只用于客户端提示，授权始终由 Server 检查。

采用无持久 MCP Session 的实现：不签发 MCP-Session-Id，不把会话存入 SQLite；POST 返回单个 JSON-RPC 结果，通知返回 202；不提供服务器推送的 GET 返回 405，DELETE 返回 405。协议版本按初始化协商，每次调用带版本；缺失版本按规范的兼容行为处理，仅在实际支持的版本集合内接受。库不能默认打开未经实现的 capabilities。

允许无 Origin 的 CLI 请求；存在 Origin 时必须匹配服务端精确白名单，否则 403。每个请求都校验 Bearer。最大工具结果 64 KiB，截断需给出游标或文件引用，不能静默丢失内容。工具调用服务端预算 10 秒，提交以持久成功为准，取消通知不能回滚已接受 Job。

示例 [codex-mcp.toml](../examples/v0.2/codex-mcp.toml) 只使用已核对的官方 Codex 配置键。平台 Token 的配置/读取由部署环境负责，不把它写入示例文件。

## 3. Worker 扩展契约

Job 核心继续使用 RuntimeService 和已有 TaskSpec。新 Server 在 Assignment 追加字段，旧字段编号不变；旧 Worker 不具备新能力时不能接收 Job Task。

| 提议消息/字段 | 内容 |
| --- | --- |
| `Assignment.job`（新字段 8） | job_id、stage、partition_key、scope_paths、strategy、template_digest、input_manifest_json、input_manifest_sha256 |
| `Runtime.template_digests`（新字段 9） | 受信运行/策略/验收组合引用到内容摘要的映射 |
| `Runtime.capabilities` | 新增 `job_io_v1`；Reduce 要求 `artifact_inputs_v1`；G1 另需 `gateway_inference_v1` |
| `DownloadInputArtifact(InputArtifactRequest)`（新 RPC） | 参数包含当前 AttemptRef、目标 artifact_id；返回 Chunk 流 |
| `Assignment.gateway`（G1 新字段 9） | 固定 base_url、独立模型 Token、绑定路由/模型；不通过用户 Task 查询返回 |

`template_digest` 是服务端解析执行配置后计算的摘要；Worker 用本地受信配置算同一摘要并比对，不执行 Agent 提供的验证脚本。注册摘要、Assignment 摘要、实际执行摘要不一致时失败 `TEMPLATE_MISMATCH`。

模板键为 `[runtime_profile, policy_ref, acceptance_profile]` 的规范 JSON 数组之 SHA-256。模板内容摘要覆盖固定 CLI 版本、适配器模板版本、实际权限策略、验收命令 argv 和对应 Job 产物规则；排除秘密及运行器安装路径；验收 argv 内的绝对路径仍参与摘要，部署节点须保持一致。双方使用同一版本编码函数：对象键排序、字符串原样保留、命令/参数顺序保留。Server 从操作者部署的受信模板清单取得期望值，不从 Worker 自报值推定信任。更改模板内容必须更新引用版本和摘要。

输入下载同时满足：Worker Token 匹配分配节点；AttemptRef 匹配当前任务代次；执行仍有效且租约未过期；artifact 是该 Reduce 冻结清单成员；源 Task 属于同 owner/project/Job 且已成功。下载中定期重新检查取消/租约，不持有数据库事务发送流。

Worker 将清单和下载文件放进本次 Attempt 的受控输入目录，向 Agent 只传文件位置和任务说明。清单来源和授权以 Assignment/Server 为准，不能通过 Agent 修改文件扩大下载权限。所有下载在 CLI 启动之前验证；验收报告记录 manifest_sha256。

### 3.1 产物契约

仍使用现有结果 tar 包，保留 report.json、changes.patch、stderr.log；扩展项由 Worker 的受信打包器收集，包总量继续服从当前 32 MiB 上限，Reduce 输入总量初始上限 128 MiB。清单只列包 ID/哈希和期望条目，不依赖节点绝对路径。

`findings.json` 最小结构：schema_version=`findings.v1`、base_commit、partition_key、findings 数组。每条 finding 包含 id、severity（info/low/medium/high/critical）、summary、path、line_start、line_end、evidence；路径/行号按基线检查，Map 不得声明其他分片来源；sources 仅用于 Reduce 汇总。空 findings 合法；解析失败不等于“没有问题”。

Agent 在原生最终结果中输出 findings JSON，Worker 解析、验证并在仓库外写出 findings.json，确认基线/分片与 Assignment 一致。这样 inspect/read-only 模式不需要赋予 Agent 文件写权限。对 report_merge_v1，最终报告保留源 finding ID；Reduce 新发现的问题标为 reduce 来源并独立保留证据，不能伪造 Map 已确认。

报告 Reduce 的最终 JSON 使用 schema_version=`merged-report.v1`，包含 base_commit、manifest_sha256、summary、findings。每条 finding 保留上述字段，额外带 sources 数组（partition_key、finding_id）；新增发现使用 origin=`reduce` 且 sources 可为空，引用 Map 的发现使用 origin=`map` 且 sources 非空。Worker 校验引用存在且 Map 来源的路径、行号与证据匹配至少一个原 finding，生成 review.json 与 review.md。行号从 1 起，line_end 不小于 line_start；所有代码证据必须能定位到基线。无法证实的结论在 summary 中注明，不伪造来源。

代码 Reduce 的 merge-report.json 由 Worker 生成，记录 manifest_sha256、应用顺序、补丁摘要、最终 diff 和真实验收退出码；模型文本只作为说明。沿用 report.json 的运行报告不与模型生成的审查结论混淆。

patch_merge_v1 的 Map 包另含由 Worker 从受信 Git diff 生成的 changes.paths.json；实际路径必须位于 scope 内。所有 rename 的旧/新路径都计入冲突检查；首版拒绝 submodule/gitlink、符号链接变更和 Git 元数据改动。Reduce 重新检查实际补丁，按 key 排序应用，任何冲突返回 `PATCH_CONFLICT`；验收命令失败返回 `VERIFICATION_FAILED`。不因为模型输出“测试通过”而改变验收事实。

## 4. 核心 SQLite 增量（schema v2）

以下 DDL 已接入 `internal/store/migrations.go`，在排空执行、独占锁和单事务内顺序执行；版本检查不会覆盖未来版本。

```sql
CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  project TEXT NOT NULL,
  idem TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  spec_hash TEXT NOT NULL,
  spec BLOB NOT NULL,
  mode TEXT NOT NULL CHECK (mode IN ('single','map_reduce')),
  state TEXT NOT NULL CHECK (state IN
    ('QUEUED','EXECUTING','MAPPING','REDUCING','STOPPING','RECONCILING',
     'SUCCEEDED','FAILED','CANCELED')),
  version INTEGER NOT NULL DEFAULT 1,
  seq INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL,
  deadline INTEGER NOT NULL,
  parallelism INTEGER NOT NULL CHECK (parallelism BETWEEN 1 AND 8),
  stop_reason TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  manifest_json BLOB,
  manifest_hash TEXT NOT NULL DEFAULT '',
  result_json BLOB,
  UNIQUE(owner, project, idem)
);
CREATE INDEX jobs_active ON jobs(state, created);

ALTER TABLE tasks ADD COLUMN job_id TEXT REFERENCES jobs(id);
ALTER TABLE tasks ADD COLUMN stage TEXT NOT NULL DEFAULT ''
  CHECK (stage IN ('','single','map','reduce'));
ALTER TABLE tasks ADD COLUMN partition_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX tasks_job_partition
  ON tasks(job_id, stage, partition_key) WHERE job_id IS NOT NULL;
CREATE INDEX tasks_job_state ON tasks(job_id, stage, state);

CREATE TABLE job_events (
  job_id TEXT NOT NULL REFERENCES jobs(id),
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  recorded_at INTEGER NOT NULL,
  body BLOB NOT NULL,
  operation_key TEXT,
  operation_hash TEXT,
  PRIMARY KEY(job_id, seq),
  UNIQUE(job_id, operation_key),
  CHECK ((operation_key IS NULL AND operation_hash IS NULL) OR
         (operation_key IS NOT NULL AND operation_hash IS NOT NULL))
);
```

独立 Task 的 job_id 为 NULL，stage/key 为空；Job Task 的三项必须一起存在。此跨列条件由统一写函数检查，必要时在实现迁移重建 tasks 时加 CHECK，不能让三个 API 分别维护。每次 Job 状态或停止原因变更同事务递增 version、写事件；Job seq 单调，Task seq 独立。

### 4.1 关键事务

**创建**：查提交键 → 比较 request_hash → INSERT Job → INSERT 全部初始 Tasks → INSERT job.created → COMMIT。子任务幂等键由系统生成 `__job/<id>/<stage>/<key>`。

**Reduce 屏障**：BEGIN IMMEDIATE → 重读 Job 状态/版本/期限 → 校验预期分片集合、成功状态、有效 Attempt 和清理 → 生成稳定清单 → INSERT Reduce Task（唯一约束）→ 更新 Job 为 REDUCING 及清单 → 写事件 → COMMIT。失败回滚，不在内存里记“Reduce 已启动”。

**分配**：现有 Task 分配事务增加 Job 停止/期限/并发检查；首次分配将 Job 从 QUEUED 改为 EXECUTING/MAPPING，同事务写 Job 事件。Reduce 的 Job 状态已在屏障中更新。

**完成**：核查 task.attempt 与 AttemptRef、Worker、代次、凭据、租约、Job 未停止、事件水位、产物和清理 → 更新 Task 与 Attempt → 写 Task 和 Job 摘要事件 → COMMIT。重复同一 final_hash 返回已持久 ACK；迟到不同结果拒绝。

**终态发布**：重读 Job 及所需终态 Task → 核查全部清理完成 → CAS 更新 Job 终态及 result_json → 写终态事件。取消/失败路径不要求所有子任务成功，但要求不会继续运行。

### 4.2 R1 多 Attempt 的迁移与隔离

当前 attempts.task 带 UNIQUE，generation 创建值固定为 1。R1 必须重建 attempts，移除单列唯一约束，改为 `UNIQUE(task,generation)`，并增加 `UNIQUE(task) WHERE released=0` 的部分唯一索引。新增 Task 的 next_eligible_at 与 Attempt 终结原因字段；tasks.attempt 始终指向当前执行。

重建迁移在维护窗口和独占锁内进行，显式复制列、重建索引并检查外键；不能通过删除历史 Attempt 绕过约束。第一版迁移只允许所有执行已释放的库。

所有改变当前业务状态的入口都需核对 tasks.attempt：RenewLease、ReportEvents、UploadArtifact、CompleteAttempt、Stop/恢复核查。旧 Attempt 的重复完成只能返回历史 ACK，不能推进当前 Task；旧事件不写入当前序列；旧上传流即使开始时有效，也要在文件登记前重查代次。租约、释放和文件传输的竞态必须覆盖。

## 5. G1 模型网关契约（schema v3）

G1 可关闭；关闭时模型路径返回明确不可用，Job API 和 MCP 继续运行。

| 方法 | 支持范围 |
| --- | --- |
| `GET /v1/models` | 只返回本身份允许且已验收的模型 ID |
| `POST /v1/responses` | HTTP JSON / SSE，同步生成和调用方工具循环 |
| `POST /v1/responses/compact` | 同上游的非流式压缩请求/返回；同一身份和路由 |
| 其他模型路径 / WebSocket Upgrade | 明确 404/400 `unsupported_feature`，不转成 Agent 任务 |

使用固定上游 HTTPS 地址与允许模型，不接受调用方指定目标 URL/上游账号，不跟随携带秘密的跨源重定向。请求里的组织/项目头不能覆盖服务配置。只转发白名单业务头，剥离客户端 Authorization、Cookie、逐跳头和内部路由头，再使用服务端上游凭据。

初始支持内联文本、函数/自定义工具定义及调用结果、推理/compaction 内容；模型网关校验外层请求能力，受支持的嵌套内容原样保留。允许工具类型、请求字段和客户端版本组合在 G1 兼容矩阵中锁定。拒绝未实现的媒体、托管工具、文件/容器引用和服务器会话参数，不把它们丢弃后继续执行。

`/responses` 的 `store` 缺省规范为 false；显式 true 拒绝；`/responses/compact` 不添加 store 参数。拒绝 previous_response_id、conversation、background:true、item_reference 和需要共享上游对象授权的参数。请求体初始上限 16 MiB，网关总在途并发初始上限 16，可配置但需压测；任务 API 的 256 KiB 限制不套用于模型上下文。

G1 使用至少 30 秒连接/响应头预算、300 秒流空闲预算、最长 30 分钟请求预算，并受绑定 Attempt 剩余期限约束；实现时暴露为配置。持续流转发不受普通短 HTTP WriteTimeout 意外截断，使用单次写入期限和整体 context；SSE 字节连续转发并增量检查事件，单事件上限 4 MiB，非流响应上限 16 MiB；超限关闭流，不把截断内容改写成成功。

代理内部不重试已发出的 POST。上游 401/403/429/5xx 在尚未发送下游头时保留其状态和安全的错误内容、Retry-After/request ID；流中错误或 EOF 关闭流并标记中断，不改写成成功 JSON。`X-Request-ID` 是 Server 自有跟踪标识，不是上游幂等保证。

模型接口错误遵循 OpenAI 风格 `{error:{message,type,code,param}}`，与 Job API 错误体区分。网关 ID 和上游 request ID 分开记录。不同请求即使文本相同也不自动去重，不宣称客户端重试不会再次计费。

### 5.1 请求用量表

```sql
ALTER TABLE attempts ADD COLUMN model_token_hash TEXT;
CREATE UNIQUE INDEX attempts_model_token
  ON attempts(model_token_hash) WHERE model_token_hash IS NOT NULL;

CREATE TABLE gateway_requests (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  project TEXT NOT NULL,
  job_id TEXT REFERENCES jobs(id),
  attempt_id TEXT REFERENCES attempts(id),
  route TEXT NOT NULL,
  model TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('STARTED','COMPLETE','FAILED','UNKNOWN')),
  upstream_request_id TEXT,
  input_tokens INTEGER,
  output_tokens INTEGER,
  usage_json BLOB,
  usage_complete INTEGER NOT NULL DEFAULT 0 CHECK (usage_complete IN (0,1)),
  started INTEGER NOT NULL,
  finished INTEGER,
  error_code TEXT NOT NULL DEFAULT ''
);
CREATE INDEX gateway_requests_job ON gateway_requests(job_id, started);
```

请求开始必须先记录 STARTED；最终更新只能从 STARTED 结算一次。流中累计用量在内存中整理，最终一次写入；崩溃未结算行转 UNKNOWN，未知计量不强行按零计入。失败响应也可有可用用量，所以状态与 usage_complete 独立。

Attempt 模型 Token 由该次 256 位随机租约材料、ID、代次及专用域标签派生；原文通过受保护的 Assignment 交付，表内模型字段只存 SHA-256。Server 持久命令省略原文、发送时重建，Worker 私有执行数据按租约秘密保护。其 owner/project/job/model 由当前 Attempt 及冻结规格推导，不信任客户端的追踪头。普通模型调用方只能使用自己身份对应的固定项目；不能伪造 job_id 归属。

schema v3 与未来 R1 attempts 重建相容，迁移需保留 Token 哈希和 gateway_requests 外键。G1 关闭时已有用量记录仍可读取。

## 6. 兼容、限制与版本

- v0.1 gRPC Task 客户端继续可用；Job 子任务写权限由 Job 服务掌握。新增能力不能只靠 Proto 向后兼容来假定老 Worker 可执行。
- v0.2 JSON Schema 是结构约束；权限、字节数、唯一分片、路径、模板匹配、补丁交集和实际模型能力由业务校验实现。
- 核心 Job API、MCP、G1 Responses 各自独立做契约测试；示例适用于 v0.2，v0.1 配置解析器会拒绝新增键。
- 固定 Codex/Claude 和 SDK 版本后记录实际协议轨迹；升级需要重跑相应兼容矩阵。本文不承诺任意客户端、任意上游兼容。
- 官方接口依据及日期见[主设计来源](../design/gateway-mapreduce-v0.2.md#10-来源与核查边界)。
