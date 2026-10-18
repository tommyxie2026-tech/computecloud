# 订单 IU 图示例

两笔同客户订单各 6000 分，累计满 10000 分减 1000 分一次，应付 11000。图为两个 agent 提取 → Server memory join → agent 应用全局规则 → 固定只读 command 独立重算。修改规则为减 2000 分后，预期为 10000；必须更新 source 文本、摘要与所有引用 span 后提交新图，不能复用旧记忆。

## 配置并运行

使用仓库现有 Server / Worker / Client 配置和 TLS/Token 流程。将 orders.json 的模型、仓库映射和 base_commit 换成实际配置；base_commit 必须存在于所有目标 Worker 的仓库中，示例值只是 computecloud 的基线提交。来源中的订单与规则已附有效摘要。

在 Server 的 jobs 下启用并配置以下内容：
```yaml
jobs:
  enabled: true
  graphs_enabled: true
  graph_commands:
    orders-proof:
      execution:
        runtime_profile: command_exec
        model: orders-proof
        credential_ref: verifier-account
        policy_ref: inspect
        acceptance_profile: basic
      parameters_schema: '{"type":"object","additionalProperties":false}'
```
在 Worker 的 runtimes 下加入：
```yaml
command_exec:
  executable: /absolute/path/to/computecloud/examples/iu-graph/verify_orders.py
  version: iu-orders-verifier-1
  models: [orders-proof]
  credentials: [verifier-account]
```
程序必须可执行，Worker PATH 中必须有 python3。所有运行该 profile 的 Worker 使用相同可信程序版本。若有多个程序，应由运维提供固定只读 dispatcher，不能由图传入 shell 命令。

Server credentials、用户和 Worker 的 credentials allowlist 均加入 verifier-account；设定所需并发额度。Worker policy inspect 和 verifier basic 沿用 examples/worker.yaml 的只读设置。通过现有 `computecloud templates --config worker.yaml` 生成并更新 Server jobs.templates，必须包含 agent 与 command 的实际组合及摘要。不要手填伪摘要。Server 上显式授予独立验收身份 goals:evaluate / jobs:read 及项目权限，执行身份无需该 scope。

CLI 语法（按部署的 Client 配置执行）：
```sh
computecloud job submit-graph --config client.yaml --file examples/iu-graph/orders.json --key orders-v1
computecloud job graph --config client.yaml --id JOB_ID
computecloud job result --config client.yaml --id JOB_ID
```
观察节点任务完成、输出准入后，Job 为 SUCCEEDED，Goal 仍为 EVALUATING。查看 verify/result=PASS 与 verify/expected_total=11000，并独立核对真实输出和来源。使用契约文档的评估请求模板，填入 graph 查询返回的 output_sha256、当前 evaluation_version、真实证据 hash 以及必要的 supersedes_evaluation_id：
```sh
computecloud job evaluate-graph --config evaluator-client.yaml --id JOB_ID --file evaluation.json
```
可信校验程序对原始 local 订单和 rules 重算，不使用模型记忆作为事实。错误金额得到 FAIL；证据缺失、重复 ID 冲突或非法值得到 UNKNOWN；两者仍正常输出 JSON 并退出 0，交由业务评估处理。

## 验证证据

`python3 -m unittest discover -s examples/iu-graph -p 'test_*.py'` 覆盖 11000、规则修订 10000、错误 12000、缺失/冲突、重复订单去重、不同客户和非法 bool 值。

`make ci-iu-graph` 包含真实 Server + 两 Worker 进程 fixture、memory join、独立固定 command、延后业务验收，以及契约/记忆/迁移/授权测试。fixture 使用确定性模拟 agent；不代表真实模型质量或跨主机生产验收。

## 先测真实模型语义效果

[本机对照实验](../../docs/validation/iu-local-quality-experiment.md) 提供单节点与 IU 组合的小样本入口，最多 8 次 CLI 调用尝试，输出正确性、耗时与已测用量。该实验复用真实图契约，尚不代表分布式调度性能。

优先验证协议与模拟效果可运行 make ci-iu-graph，详见 [CI 验收与报告](../../docs/validation/iu-ci-simulation.md)。该入口不需要真实模型账号。

增量复用示例与限制见 [v3 契约](../../docs/contracts/iu-graph-v3.md)。CI 的 TestGraphIncrementalOrdersMatchesFullRecompute 使用同一订单、规则 1000→2000、实际双 Worker Task/Attempt 与独立 command 验证；打印 IU_INCREMENTAL_EVIDENCE，保留实际来源与未知 token/费用。模型均为 fixture，不能作真实收益结论。未配置 replay binding 的 orders.json 继续全量重算。
