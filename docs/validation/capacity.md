# 轻量容量测试与目标环境验收

容量工具为 Python 标准库脚本，测试对象仍为现有 Go 单二进制与 SQLite。不引入监控服务、数据库服务或队列。默认只在临时目录运行本机 fixture，不连接已部署集群、不使用模型凭据。

## 1. 可复现命令

```sh
make build
make capacity-check
# 完整矩阵：1/2/4/8 Workers × 每 Worker 1/2/4 slots。
python3 scripts/capacity.py --jobs 32 --output dist/capacity-run-001.json
# 自定义有限范围；结果文件必须不存在，防止覆盖原验收证据。
python3 scripts/capacity.py --workers 2,4 --slots 1,2 --jobs 64 --submitters 4 --timeout 300 --output dist/capacity-run-002.json
```

需要 Linux `/proc`、Python 3.12+、Git 和构建后的二进制，无 pip 依赖。`make capacity` 使用完整默认矩阵，写入 `dist/capacity.json`，已有文件时明确拒绝覆盖。`capacity-check` 使用 1/2 Worker、每节点 1 slot、每组 4 Job，适合 CI 功能门槛，不设易受共享运行环境影响的性能阈值。CI 同时保存 JSON 报告。

每组建立独立临时仓库、令牌、配置和本机数据库。每一对 Job 包含一个 single、一个两分片 `report_merge_v1`，Map 混合 Codex/Claude 协议，每 Task fixture 暂停 200ms。32 Job 对应 64 Task。Server 调度 tick 为 50ms、租约 15s；账号和项目额度均等于总槽位数，避免默认额度干扰槽位维度。

脚本等待所有 Worker 注册后计时，四线程默认并发提交，轮询至终态；全部成功后分页读回所有 Job 事件并验证连续性，下载每个最终产物核对 SHA-256，最后只读检查 SQLite 完整性、Task 数与派发时间。停止 Worker 后停止 Server，清理临时目录。失败返回非零并保留已完成组及失败组的 JSON/有限日志尾部；第一组失败即停止剩余矩阵。报告不保存 Token 和完整配置。

## 2. 指标口径与限制

| JSON 字段 | 含义与边界 |
| --- | --- |
| `environment` | OS/Python/二进制版本及 SHA-256、可见 CPU 数与 cgroup v2 限制；不是专属资源保证 |
| `submit_http_ms` | HTTP 提交往返 p50/p95/max，含客户端、鉴权、解析、排队和事务；不是纯数据库写延迟 |
| `database.task_queue_ms` | Task 创建至持久化 STARTING 的毫秒差，包含实际排队；Reduce 从屏障创建时开始计时 |
| `job_completion_ms` | Server 持久化 Job 创建至终态的耗时；不是仅模型推理耗时 |
| `jobs_per_second` / `tasks_per_second` | 从首次提交前至最后一次终态被观察到的吞吐，不含初始化和随后读回 |
| `resources.processes` | Server/各 Worker 的 CPU 秒和 100ms 采样 RSS 峰值，覆盖执行及读回；不含 Git、fixture、verifier 子进程 |
| `resources.wal_peak_bytes` | 采样观察到的 WAL 文件最大长度，可能漏掉采样间峰值；不是累计写入量 |
| `readback` | 执行结束后事件与最终产物顺序读回的共同区间吞吐；不是各接口独立峰值带宽 |
| `database` | 成功状态数、每 Worker Task 数、事件总数、包括 Map 输入在内的产物总字节数 |
| `database.sqlite_write_latency_ms` | 当前无事务计时埋点，明确为 null，不伪造数值 |

percentile 使用 nearest-rank。每格只运行一次、使用新库、短小报告和固定延时 fixture，不是稳态、长期保留或饱和测试；不据此承诺最大节点数或生产 SLO。单次/失败组可能只有部分字段。所有资源指标只覆盖当前进程命名空间可见的测试进程。

为了比较结果，请固定机器/容器资源、二进制哈希、参数，单独运行矩阵，避免与构建或其他压测并行。至少重复三次，保留原 JSON；增加模型延时、产物大小、任务数或节点时不能把本轮数值线性外推。SQLite 单次事务计时及生产存储负载仍需另行测量。

## 3. 本机完整矩阵记录

执行日期 2026-09-22；Linux 6.18.44 x86_64、Python 3.12.14、`computecloud 0.2.0`，二进制 SHA-256 为 `c8d943a4413c0a44c2d68c4fe6bc937394bdc1bce4f27e86a4f2aa084dae0cd1`。运行环境可见 9 个 CPU，cgroup v2 quota 为 8 CPU、memory limit 为 8 GiB。每格 32 Job/64 Task，全部成功，SQLite `integrity_check=ok`；分页读取 384 个 Job 事件并校验 32 个最终产物，共约 0.141 MiB。

| Workers | slots/Worker | Task/s | 队列 p95 ms | Job 完成 p95 ms | 提交 p95 ms | Server RSS MiB | Worker RSS 合计 MiB | WAL 峰值 MiB |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1 | 0.63 | 78979 | 99381 | 17.4 | 30.5 | 26.9 | 3.99 |
| 1 | 2 | 1.71 | 30262 | 37260 | 53.5 | 30.0 | 26.4 | 3.97 |
| 1 | 4 | 3.25 | 14846 | 18602 | 19.9 | 29.8 | 27.0 | 3.97 |
| 2 | 1 | 1.81 | 30266 | 35246 | 40.5 | 30.7 | 51.1 | 3.98 |
| 2 | 2 | 2.30 | 21324 | 27246 | 15.5 | 30.5 | 52.1 | 4.01 |
| 2 | 4 | 4.45 | 10654 | 13686 | 43.3 | 30.3 | 52.2 | 4.02 |
| 4 | 1 | 3.24 | 14659 | 19523 | 16.1 | 30.6 | 100.3 | 4.02 |
| 4 | 2 | 4.73 | 10520 | 13279 | 15.1 | 30.5 | 101.3 | 4.00 |
| 4 | 4 | 8.60 | 4483 | 7381 | 11.9 | 30.4 | 101.0 | 4.01 |
| 8 | 1 | 5.16 | 8388 | 12263 | 12.3 | 30.4 | 194.9 | 3.98 |
| 8 | 2 | 8.56 | 4398 | 7284 | 19.3 | 31.9 | 197.0 | 3.95 |
| 8 | 4 | 9.92 | 2478 | 6203 | 48.3 | 30.5 | 198.0 | 4.02 |

本轮在 16 个总 slots 后收益趋缓：4×4 为 8.60 Task/s，8×2 为 8.56 Task/s，8×4 为 9.92 Task/s，与 8 CPU quota 下的 CPU/进程开销相符。Server RSS 约 30–32 MiB，Worker RSS 约每进程 25 MiB；WAL 长度约 4 MiB，未随 Worker 数明显增长。提交 p95 只有每格 32 个样本且运行环境共享，不能据此设置 SLO；队列延迟下降表明槽位和节点扩展确实提高了本 fixture 的消化速度。

原始 JSON 位于本次执行工作区的 `dist/capacity-20260922.json`，`dist/` 不进入 Git；后续 CI 的小矩阵 JSON 作为 workflow artifact 保存。表中数值取自原始报告，脚本不会将其硬编码为通过阈值。

## 4. 真实部署验收门槛

这一部分须由具备受信目标主机和合法账号的操作者执行，不由本机 fixture 代替。沿用 [运行指南](../implementation/v0.2-runbook.md)，按 [实施矩阵](../implementation/v0.2-plan.md) 记录：

1. 固定 Server/Worker 二进制哈希、Linux 版本、CPU/内存/磁盘、CLI 完整版本、模型、执行模板摘要；主机使用各自本机数据目录和独立节点 Token。记录 TLS 域名/证书信任方式，不记录私钥或 Token。
2. 使用至少两台独立主机，确认注册/重连与错误 TLS/节点身份拒绝。保持 G1 关闭，先完成真实 Codex/Claude single、跨节点两分片报告和补丁合并；核对真实验收命令退出码与产物来源。
3. 用真实 Codex 的 MCP 配置提交、轮询、读取、取消 Job；同幂等键重投 ID 不变。保留客户端版本与脱敏错误，不能只验证手工 HTTP 请求就标为 Codex 接入通过。
4. 在专用测试任务执行时停止 Worker、停止 Server、断开测试节点网络，核对租约后进程停止、额度保留/释放与恢复结果；按 V08–V13 保存故障时间与 Job/Task/Attempt ID。不要在生产节点随意杀进程或改防火墙。
5. 单独启用 G1 测试配置，使用合法 API Key，验证真实 Responses/SSE/工具/compaction、取消撤销及供应商用量对照。未知用量保持 unknown；失败请求不自动重放或切换账号。
6. 排空并备份，在测试数据副本完成升级/还原；确认旧二进制拒绝新库。保留配置摘要、事件页、产物 SHA-256、验收结果和未通过项。

真实账号、节点或账单数据缺失时保持“待验收”，不开放未经验证的网关组合。自动重试 R1、GC 和 HA 不属于这轮容量工具的交付范围。
