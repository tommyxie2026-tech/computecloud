# 分布式执行性能基线

此工具用于在同一提示、Runtime、模型、仓库基线和验收条件下记录本机直跑与 ComputeCloud Job 的结果。它不自动裁剪提示，也不读取或写入模型凭据；ComputeCloud 凭据由 CLI 的受保护客户端配置读取。报告格式见 [distributed-performance-report.schema.json](distributed-performance-report.schema.json)。

## 运行前准备

1. 准备一个不含密钥的提示文件，并确定同一 Runtime、模型、仓库基线及验收条件。
2. 为 Job 场景准备有效的 single Job JSON 模板和受保护的 ComputeCloud 客户端配置。模板需包含正确的项目、仓库、凭据引用、策略及验收配置；脚本只替换 `input.text`，并可覆盖 `execution.model` 和 `execution.runtime_profile`。
3. `direct_local` 的命令由调用方提供，例如本机已有的 `codex exec --json -`。提示通过标准输入提供；命令不经 shell 执行。确保直跑和 Worker 使用相同模型及权限设置。
4. `single_worker` 运行期间确保恰有一个合格 Worker 可接单；`dual_worker_concurrent` 需要两个合格 Worker，并会同时提交全部样本。分别使用 `--single-worker-count 1` 或 `--dual-worker-count 2` 声明场景 Worker 数，并用对应 `--confirm-*` 参数确认已通过 `worker list` 核对在线状态和槽位。声明值及确认状态会写入每个场景报告；脚本无法自行核实远端状态。
5. `workspace_cold` 和 `workspace_warm` 使用相同 Job 提交流程，但准备条件由操作者控制：冷样本前清理/重建测试用 Prepared Workspace；热样本前先完成一次不计入样本的预热。`workspace_cold` 每次运行强制 `--samples 1`，每次重置后重新运行并使用不同报告路径采集下一个样本。使用对应 `--confirm-workspace-*` 参数声明已完成准备；不要对生产工作区执行清理操作。

## 示例

```sh
python3 scripts/benchmark_distributed_performance.py \
  --scenarios direct_local single_worker dual_worker_concurrent workspace_warm \
  --samples 5 \
  --prompt-file ./benchmark-prompt.txt \
  --runtime codex_exec \
  --model configured-codex-model \
  --single-worker-count 1 \
  --dual-worker-count 2 \
  --confirm-single-worker \
  --confirm-dual-workers \
  --confirm-workspace-warm \
  --direct-command 'codex exec --json -' \
  --computecloud-bin ./bin/computecloud \
  --client-config ./client.yaml \
  --job-template ./benchmark-job.json \
  --timeout-seconds 3600 \
  --output ./dist/performance/report.json
```

如果只跑 Job 场景，可省略 `--direct-command`；如果只跑 `direct_local`，可省略客户端配置和 Job 模板。脚本会在报告中保留超时、取消和失败样本的耗时与稳定错误码，并继续剩余样本。提交请求的临时 JSON 文件在每次 Job 结束后删除。

冷 Workspace 单样本示例：每次执行前先重置隔离测试工作区，再执行一次并指定新的报告文件；重复此流程采集多份冷样本。

```sh
python3 scripts/benchmark_distributed_performance.py \
  --scenarios workspace_cold --samples 1 --confirm-workspace-cold \
  --prompt-file ./benchmark-prompt.txt --runtime codex_exec --model configured-codex-model \
  --computecloud-bin ./bin/computecloud --client-config ./client.yaml \
  --job-template ./benchmark-job.json --output ./dist/performance/cold-01.json
```

## 报告解释

- `latency_ms.p50/p95` 对所有已计时样本计算，包括失败样本；空样本返回 `null`。
- `success_rate` 分母是该场景全部样本。失败列表只保留稳定错误码，不包含 Job 正文、提示、命令输出或异常文本。
- `request_bytes` 是单样本请求大小的 P50。直跑场景按 stdin 提示字节计；Job 场景按实际提交的 JSON Job 请求体计，因此它们表示不同传输边界，不能把差值解释为纯调度开销。
- Token 数为可用 Worker Trace 指标的样本合计；缓存 Token 独立统计，不代表请求字节减少。缺失的 Token 或 RSS 指标为 `null`，不伪造为 0。
- `peak_rss_bytes` 是可用 Attempt 峰值 RSS 中的最大值。没有采集到时为 `null`。
- `condition_status` 表示操作者是否确认场景条件，`worker_count_declared` 是操作者声明值；`operator_confirmed` 不是远端事实证明，执行前仍须人工核对。脚本拒绝重复场景名称，避免结果覆盖。
- 环境只允许报告平台、Python 版本、Runtime 和模型；Worker 数按场景单独记录为操作者声明值。配置路径、工作目录、仓库绝对路径、凭据及提示不会写入报告。

至少重复运行三轮，并保留每轮 JSON、样本数、Worker 在线/槽位配置及冷/热准备方式。首次结果仅作为诊断基线；没有可比的重复样本前，不据此声称性能提升，也不设置绝对耗时 CI 门槛。

本分支未连接真实模型凭据或专用双 Worker 环境，因此尚未采集性能样本；这里没有性能结论。场景标签本身不会验证远端 Worker 数或 Prepared Workspace 缓存状态，报告明确记录操作者声明的确认状态。若无法为每个冷样本独立重置隔离的测试 Worker/工作区，或无法在热样本前完成预热，请不要确认相应条件；测试工具不会自行清理 Worker 数据目录。

`direct_local` 只有在命令退出后输出单个 JSON 对象时才会读取其中受支持的 Token/RSS 数值；Codex JSONL 或普通文本输出只用于成功状态判断，其正文不保存，指标显示为 `null`。
