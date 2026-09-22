# ADR-001：SQLite 与轻量部署决策

- 日期：2026-09-22
- 状态：已接受并在 v0.1 落地；本地存储、恢复与备份验证通过，真实双机环境待验收
- 最新约束：使用 SQLite；整个服务尽量轻量、简单
- 关联：[主设计](../design/agent-orchestration-go.md)、[运行契约](../contracts/agent-runtime-v1.md)、[同类调研](../research/agent-orchestration-landscape.md)、[验收计划](../validation/multi-node-poc.md)

## 1. 决策

采用一个 Go 二进制、一个活动 server、多节点 worker、SQLite 和本地文件目录。API、调度、节点登记、租约核查均在 server 进程内；适配器、进程监督和验收均在 worker 内。SQLite 是正式首版方案，支持多节点执行，不要求独立数据库服务器。

暂不引入 PostgreSQL、Redis、消息队列、Hatchet、Temporal、Kubernetes、选主服务或插件平台。此前“优先验证 Hatchet、Temporal 备选”的建议被用户的轻量化约束替代；它们保留在调研中，未来出现具体需求时再做决策。

首版目标是可靠地派发和管理 CLI 任务，不开发通用持久工作流引擎。多模型由已授权的静态配置选择，暂不增加独立 Model Registry 服务。保留小的 Go 类型边界，避免预先构建多后端抽象。

## 2. 最小部署

| 项目 | 首版选择 |
| --- | --- |
| 可执行文件 | `computecloud`，含 server / worker / task / backup 子命令 |
| 控制节点 | 1 个 server 进程、server.db、本地产物目录 |
| 执行节点 | 每节点 1 个 worker 进程、worker.db、CLI 与工作区 |
| 传输 | gRPC；Worker 主动连接；远程 TLS + 每节点令牌 |
| 运行方式 | 直接启动或由现有 systemd 托管；无需容器编排 |
| 代码执行 | 受信环境默认独立工作区与进程组；cgroup/容器按隔离需求启用 |
| 管理入口 | 命令行和 RPC，GUI 延后 |
| 平台中间件 | 0 个外部数据库/缓存/消息/工作流服务 |

“单二进制”指平台交付物，Codex / Claude CLI、Git 及任务需要的编译工具仍是执行节点依赖。首版目标为受信用户和受控节点；不可信任务的执行配置必须提供相应隔离，进程组不能当作安全沙箱。

v0.1 命令如下；完整配置与恢复步骤见 [运行指南](../implementation/v0.1-runbook.md)。本文的 server.db/worker.db 为角色名称，实际文件均为各自 data_dir 下的 state.db：

```sh
computecloud server --config server.yaml
computecloud worker --config worker.yaml
computecloud task submit --config client.yaml --file task.json
computecloud task watch --config client.yaml --id TASK_ID
computecloud task cancel --config client.yaml --id TASK_ID
```

## 3. SQLite 访问规则

### 3.1 单实例与连接

server.db 只由一个活动 server 管理；启动时对数据目录持有进程级独占锁，第二个 server 必须失败退出。远端 Worker 通过 RPC 访问业务状态，不能挂载或打开 server.db。SQLite 官方说明 WAL 不适用于跨主机共享的网络文件系统，因此数据库文件放本机磁盘。[S1]

Go 驱动拟选无 CGO 的 `modernc.org/sqlite`，便于单二进制交付；编码时锁定驱动及依赖版本，在目标 OS/arch 验证。运行时报告驱动版本和 `sqlite_version()`，不能把系统 sqlite3 命令的版本当作实际嵌入版本。[S4]

WAL 版本基线要求包含上游已修复的 WAL-reset 问题：首版选择内嵌 SQLite 3.51.3 或其后的已验证版本，具体驱动版本在编码时锁定；不依赖“驱动最新”这一推断。[S1]

首版每进程数据库使用一个长期复用的 database/sql 连接，`SetMaxOpenConns(1)`、`SetMaxIdleConns(1)`；读写均为短操作。迁移和维护也经过统一入口，不另起后台 SQL 工具并发操作活动库。SQLite WAL 的并发读优势可在后续有测量依据时通过有限只读连接启用；首版不为此增加复杂度。

### 3.2 初始化与耐久性

初始配置如下，是工程默认值，需在真实磁盘上验证：

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA wal_autocheckpoint = 1000;
```

初始化必须读取有效值并确认成功。连接级配置在每个新连接建立时应用，而不是只在建库时执行一次。`FULL` 用于已提交任务、命令和 ACK 的持久化要求；不是对硬件故障的绝对保证。WAL 与同步配置行为以 SQLite 官方文档为准。[S1][S3]

首版保留自动 checkpoint，不另造 checkpoint 服务。分页读取后及时关闭 rows，不能在持有事务或连接时等待订阅者网络发送。监测 WAL 大小、写事务耗时、busy 次数和可用磁盘；长事务和大量日志先在应用层修正。

### 3.3 内置任务队列

`tasks.state = QUEUED` 且到达 `next_eligible_at` 的行构成队列，按授权优先级和创建时间读取有限批次。使用索引支持扫描，Go channel 在新任务/资源释放时唤醒调度；另以可配置短周期兜底查库。任务不能只存在于内存 channel。

一个调度 goroutine 负责准入，使用短 `BEGIN IMMEDIATE` 事务重读任务状态、核对节点/账号/项目额度及 Session 持有者，然后写 Attempt、活动引用和 StartAttempt 待发命令。提交后才进行网络交付。[S2]

显式事务使用同一专用连接，不把 BEGIN 和后续 SQL 分散到池中不同连接，也不在 sql.Tx 内再次 BEGIN。SQLite 不使用行级 `FOR UPDATE` 或 SKIP LOCKED。通过唯一约束、版本 CAS 和检查影响行数实现条件变更。

busy 只做有界等待/重试；总期限服从 RPC 或内部操作预算。提交结果不明时根据原幂等键查询，不能把重试当作新任务。数据库不可写时不返回持久接受，不确认事件水位。

### 3.4 少量表覆盖可靠性

server.db 的核心表为 tasks、attempts、sessions、workers、worker_commands、events、artifacts；worker.db 保留本地 attempts、commands、events，另有 schema 版本记录。完整字段见主设计。

不再单设提交去重、资源计数、连接记录、通用 outbox 或工作流投影服务：提交键并入 tasks；未释放 Attempt 和 Session 持有者表达预留；worker_commands 就是发件箱。命令、状态与必要审计作为 events 保存，避免多个不一致状态源。

未获清理证明的 RECONCILING/CANCELING Attempt 继续占用安全相关的资源与 Session 持有权。资源释放是显式事务变更，不能仅按过期时间自动删除记录；必要的启动去重、停止墓碑和事件序号不会因为轻量化而省略。

## 4. 故障与恢复

| 故障 | 首版行为 |
| --- | --- |
| server 正常重启或崩溃 | 从本地 SQLite 恢复队列、命令及已提交状态；先对账活动 Attempt，再继续派发 |
| server 停机较短 | Worker 按已有有效租约继续，事件本地暂存；恢复后核查续租和水位 |
| server 长期不可达 | Worker 在租约失效前按策略停止；无法确认停止时阻止重复写执行 |
| Worker 重启但 CLI 仍在 | 查询启动日志与真实执行环境，核查后认领或停止；不能直接再起 CLI |
| SQLite busy / 磁盘满 | 有界等待或明确失败，不能假提交；暂停接纳并保留诊断 |
| 本地数据盘损坏 | 从备份恢复；首版不提供自动高可用切换，也不保证备份之后数据零丢失 |

Task / Attempt / Session 与 Process 的区分保持不变。连接重连只能恢复通信，恢复 CLI 历史还需要明确的原生 resume、兼容版本、身份和工作区。两种恢复不混为一谈。

## 5. 本地文件与备份

大日志、diff、附件和检查点放文件目录，SQLite 保存索引、摘要和大小。产物上传做大小和哈希校验、临时写入、持久化与原子改名，然后登记数据库；数据库事务不跨越网络传输。具体 RPC 见运行契约。

首版采用最容易验证的维护窗口备份：

1. 暂停新提交/派发，排空任务或显式取消并取得清理确认；保留未完成核查的事实。
2. 停止 server，确认所有数据库连接关闭、进程锁释放；不要手工删除仍存在的 -wal 文件。
3. 复制完整 server 数据目录，包括数据库、仍存在的 WAL 及对应产物，记录版本和校验值；按恢复需求另备份 Worker 的本地会话、工作区和 worker.db。
4. 在隔离目录校验备份，运行完整性检查，并做一次恢复演练后记录证据。

不能在活动服务运行中仅 `cp server.db` 充当一致备份。若未来需要不停机备份，单独实现并验证 SQLite Backup API 与产物清单快照的一致性，不在首版同时维护两套复杂备份机制。[S5]

恢复流程先确认旧 server 已停止且不可继续调度，以维护模式启动恢复副本，再与所有相关 Worker 对账。旧备份缺失的活动执行必须导入核查，不能忽略后直接重派。备份时间之后的 Task/事件可能无法找回；报告实际恢复点和未决执行，不声称全量恢复。

## 6. 何时再考虑扩展

先测量 DB 写等待、调度延迟、事件量、队列长度、磁盘增长和人工恢复耗时。优先通过增量合并、短事务、索引及保留策略处理瓶颈。没有实测之前不设“SQLite 支持多少节点”的拍脑袋阈值。

只有出现明确的控制平面高可用需求、单机持久写入瓶颈或复杂长流程需求，才重新评估数据库服务/工作流引擎。多执行节点本身不构成引入外部数据库的必要条件。未来迁移需要独立 ADR；首版不承担多 server 共享 SQLite、自动主备或跨后端热迁移。

## 7. 官方依据

- [S1：SQLite WAL、并发与版本修复说明](https://www.sqlite.org/wal.html)
- [S2：SQLite transactions](https://www.sqlite.org/lang_transaction.html)
- [S3：SQLite PRAGMA](https://www.sqlite.org/pragma.html)
- [S4：modernc.org/sqlite 驱动文档](https://pkg.go.dev/modernc.org/sqlite)
- [S5：SQLite Backup API](https://www.sqlite.org/backup.html)

资料核查日期为 2026-09-22。表结构、单实例、默认配置和阶段范围是 computecloud 的设计决定，不是 SQLite 对任意工作负载的性能承诺。
