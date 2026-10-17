# IU 本机小样本效果测试

这轮先回答一个可测问题：在同一 CLI、模型标识、effort 和原始证据下，多个 IU 提取并保留结构化记忆后，能否像单节点完整处理一样正确应用跨分片规则？测量正确性、CLI 调用次数、实际输入字节、总耗时，以及 Provider 确实报告的 token 和费用。

这是一项本机语义/契约实验，复用真实 CompileContext、BuildOutput、ApplyDelta、MergeViews 和冻结 JSON Schema；不启动第二套生产执行引擎，也不代表 Server/Worker 的端到端性能。原生 Task/Attempt、租约和恢复另由现有 ci-iu-graph 验证。

## 对照与样本

| 对照 | 输入和执行 |
| --- | --- |
| monolithic | 一个模型调用拿到完整原始订单和全局规则，直接返回最终整数金额 |
| iu | 每条订单单独提取完整 order_id / customer_id / paid_cents；确定性 join 组装 JSON 和 unchecked 来源记忆；汇总调用拿到具名 orders、MemoryView 和全局规则，不再重放全部原文 |

只传金额会丢失客户归属与订单身份。本实验明确保留这两个字段，独立 oracle 则从原始证据去重、分客户计算。模型或记忆的自我声明不能代替 oracle。

| 样本 | 正确金额（分） | 检验点 |
| --- | --- | --- |
| cross_threshold | 11000 | 同客户两笔 6000，合计满 10000 减 1000 一次 |
| rule_revision | 10000 | 原订单不变，规则修订为减 2000 |
| separate_customers | 12000 | 不同客户不能跨客户凑满优惠 |
| duplicate_order | 11000 | 同一订单重复出现不能重复计费 |

rule_revision 是全新计算，不声称验证了跨代记忆 import 或动态失效。IU 的 provenance record 由实验工具从每个模型提取输出和真实 source 引用组装，始终为 unchecked；不保存隐藏思维链。该对照尚不能单独归因“记忆字段带来的收益”，后续需加入移除记忆、固定模型 token 预算等消融组。

## 运行

仓库需要 Go 1.27.2+ 和已可用的 Codex 或 Claude CLI。不要向测试提供真实订单或密钥。先在普通宿主终端确认 CLI 可完成一个极小请求，再运行：

```sh
go build -o bin/iu-experiment ./cmd/iu-experiment
bin/iu-experiment --runtime codex_exec --model gpt-6.1-sol --effort low --output dist/iu-experiment/quality-codex.json
```

gpt-6.1-sol 来自本次主机的已有配置；其他主机应换成实际可用的精确模型标识。工具显式固定模型和 effort，版本随报告记录；不会修改用户配置。报告中的 requested_model 不证明代理底层最终路由已冻结。

默认两个样本、每组一次，共最多 8 次 CLI 尝试，提取并发最多 2。每次调用默认 60 秒，复用 computecloud 现有进程组监管；超时或中断时取消该组，清理无法确认则拒绝接纳输出。工具不做自动修复或重试；调用上限只限制 CLI 尝试次数，不能约束 CLI/上游内部的网络重试次数。先预检总调用上限再执行，已有报告不会覆盖。每个样本交替先运行 monolithic 或 iu，减少固定顺序偏差；小样本不能消除缓存和服务负载影响。

运行四个样本需要 17 次尝试（重复订单样本多一次提取）：
```sh
bin/iu-experiment --runtime codex_exec --model gpt-6.1-sol --effort low --cases cross_threshold,rule_revision,separate_customers,duplicate_order --max-model-calls 17 --output dist/iu-experiment/all-cases.json
```

使用已配置的 Claude CLI，明确给出其配置的模型标识：
```sh
bin/iu-experiment --runtime claude_print --model YOUR_CONFIGURED_MODEL --effort low --output dist/iu-experiment/quality-claude.json
```

CLI 在临时空目录运行。Codex 使用 read-only / ephemeral，Claude 关闭工具、MCP、会话持久化和用户定制功能；Codex 输出中出现工具执行则该调用判为协议失败。这里不绕过宿主系统的权限检查。

## 看报告

- samples：同时保存 extracted_orders，并逐字段比较原始订单得到 extraction_preserves_original_fields；最终总额碰巧正确，也不能掩盖客户或订单身份被改写。该评分只进入报告，不反馈给汇总模型。每个对照的 expected_cents / actual_cents / correct、错误码、调用尝试数、wall_ms、实际序列化输入字节、上下文摘要、汇总节点读取的记忆记录数。
- summary：按对照统计样本数、有效输出数、正确数和执行失败数。模型/CLI 未成功启动时 accuracy_among_valid_outputs 为 null，不伪造为模型正确率 0%。
- input_tokens / output_tokens / reported_cost_usd：只有所有调用都报告时才汇总，否则为 null。输入 token 按原 Provider parser 口径包含缓存读/写；不是字节估算。Claude CLI 报告的费用不等于代理最终账单。
- cases 和 source_sha256：完整合成证据及内容摘要；不包含认证配置或 CLI stderr 原文。CLI 失败只保存预定义错误码。
- execution=local_cli_no_server_worker_or_task：明确本次不测分布式调度开销。wall_ms 包含本机 CLI 启动、等待及契约处理，不能作为纯模型推理时间。

成功标准首先是两组都有有效输出、金额与 oracle 一致。之后才比较单位正确样本的总用量和延迟。IU 更慢、调用更多或结果更差也应保留；不能由两个算术样本推出“小模型优于大模型”或“分布式更省钱”。

## 本次主机实际状态

已探测 Codex CLI 0.162.1、Claude CLI 2.1.301。Codex 的 SQLite 运行文件权限补齐后，当前工具沙箱仍在 in-process app-server 初始化处返回 Operation not permitted；这是模型调用前的环境失败。Claude 的已配置 computecloud-codex 路由对极小请求返回 Request too large (max 32MB)，精简 system prompt 和 effort 后仍失败。Ollama 守护进程未运行。

正式实验工具还会经过现有进程身份检查；当前沙箱不能完成该检查时，报告 CLI_CLEANUP_UNCONFIRMED 并停止接纳该次输出。

因此当前没有可报告的真实模型正确率或节约率。本机启动失败仅证明环境阻塞，不是 IU 效果结论。正式模拟验收转到 [CI 方案](iu-ci-simulation.md)。可在宿主终端用上方命令生成新报告，再继续分析模型质量与原生 Worker 的测试环境。

## 后续顺序

1. 获得上述真实模型小样本报告，检查提取错误、规则遗漏、重复订单与实际用量。
2. 扩充固定数据集与重复次数，加入无记忆消融和等 token/时间预算对照；保留失败样本。
3. 将相同样本搬到实际 Server/Worker，测调度、Artifact、准入和恢复开销。
4. 再实现持久规则失效事件与跨代授权 import，用旧规则结论不可直接 PASS 的回归来开放增量复用。

## IU-5C 代码扩展

已增加 `iu_no_memory`（保留相同结构化 artifact inputs）、`iu_no_verifier`（保留 unchecked memory，但不执行 pipeline oracle）消融。所有组仍由同一外部原始证据计算 ground truth；执行/契约失败、字段损坏、最终金额与 pipeline acceptance 分别记录，不能用正确最终金额隐藏身份信息损失。

[冻结试运行协议和 aicloud 适配边界](../superpowers/specs/iu-aicloud-adapter.md) 给出 `--pilot-plan` / `--protocol` 入口。默认原有双臂/8-call 入口保持；四臂需预检更高调用 cap。`--simulate` 同时生成 20 样本×4 组的 fixture 报告，Provider 调用为 0、token/费用仍 unknown。真实调用尚未执行。
