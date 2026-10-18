# IU CI 模拟与原生执行验收

运行入口：make ci-iu-graph。GitHub Actions 的 iu-graph-contract 自动执行，无模型账号、无真实模型调用。结果写入 Actions Summary，并上传 iu-graph-simulation-RUN_ID Artifact，保留 30 天。

## 第一层：18 个确定性语义与故障场景

四组原始证据分别测试单节点处理和 IU 拆分组合，共 8 个正常场景：

- 同客户两笔 6000，满 10000 减 1000 一次：11000。
- 原订单不变，规则修订为减 2000：10000。
- 两笔属于不同客户：12000。
- 同一订单重复出现，去重后：11000。

额外 10 个故障场景覆盖客户归属丢失、身份被改写但总额碰巧正确、全局规则遗漏、单节点/IU 错误金额、重复计费、Schema 错误、损坏 JSON、模拟超时和模拟 Provider 不可用。报告中的 check_passed 表示观察到预期检测，不代表故障输入被当成正确答案。

模型桩从实际 Context 读取来源、具名输入和规则，采用排序/相邻分组计算；不调用评分 oracle，不把预期金额放进 prompt。实际 CompileContext、BuildOutput、ApplyDelta、MergeViews 及冻结 Schema 负责数据与记忆契约。未知 token 和费用保持 null。

## 第二层：真实进程执行链路

scripts/ci_task_flow.py --iu-graph-check 在 CI 启动独立 Server OS 进程和两个 Worker OS 进程，使用本地确定性 Agent 程序及独立只读订单校验器。它执行四个图：

| 场景 | Agent 输出 | 独立校验 | 最终 Goal |
| --- | --- | --- | --- |
| 减 1000 | 11000 | PASS | SUCCEEDED |
| 修订为减 2000 | 10000 | PASS | SUCCEEDED |
| 新规则仍输出旧 11000 | 11000 | FAIL | FAILED |
| 遗漏优惠规则 | 12000 | FAIL | FAILED |

每个图核对两个根任务分配到两个不同 Worker、4 个实际 Task、4 个 ADMITTED receipt，且 Job 执行完成时 Goal 仍为 EVALUATING。提交者不具备 goals:evaluate，必须由单独验收身份提交评估。命令验证器直接读取原始订单和规则，不使用模型的自我声明。

第一张图在执行结束、等待业务评估时，对 Server 做 SIGKILL 并启动新进程；检查 PID 改变、输出与回执不变、Task ID、Attempt ID、代次和总数不变，并在两个 Worker 重新上线后持续检查，幂等提交仍返回原 Job，再完成授权验收。这里不声称在 PENDING 准入窗口注入了 OS 崩溃；该窗口另外由 DB 关闭/重开测试覆盖。

Go 原生集成测试还以两个 Worker 实例及真实 CLI 子进程重复上述四种规则路径；它与独立 Worker OS 进程测试分开记录。

## 第三层：恢复、权限与拒绝路径

CI 必测名单要求实际 PASS，关键项缺失或被 SKIP 均不能通过：

- Artifact 已接受但未准入时关闭/重开 DB；禁止新提交后仍恢复，不重跑已完成 Task。
- 永久拒绝准入能够失败终结；队列满时等待，不阻塞 Scheduler。
- 上下游端口名不同仍正确绑定，保留来源端口。
- 记忆冲突与多跳失效不丢失，模型不能自称 verified。
- UTF-8 区间完整重建、全局规则不遗漏、超长输入明确拒绝而不截断。
- 取消、deadline、旧代评估与独立评估权限；FAIL 经 Guard 发布新代。
- CLI 真正超时后清理整个进程组。该项在 CI 不允许因本机沙箱原因跳过。

## 报告

Artifact 内包含：

- report.json：提交 SHA、各阶段退出码、关键测试结果及模拟汇总。
- summary.md：可读场景表、原生进程证据和关键测试名单；同时写入 Actions Summary。
- run-*/simulation.json：18 个场景的输入摘要、实际输出、提取字段完整性、预期检测与实际结果。
- run-*/*.jsonl：Go test -json 事件及每阶段日志，包括失败输出。
- run-*/processes/report.json 和 logs/：独立 Server/Worker 进程、分配、回执、业务评估与硬重启证据。

任何阶段失败、缺少原生进程证据、缺少必测项、必测项被跳过或模拟检测不符，都会使 CI 失败；仍上传已生成的报告。CI 不需要安装或调用 Codex、Claude 或 Ollama。

## 能回答与不能回答的问题

这些测试回答“图契约、信息传递、拒绝错误、调度和恢复是否按预期工作”。它们不能证明真实模型智力、真实正确率、语义普遍无损、分布式加速或费用节约。

后续集成门禁增加持久失效、跨代授权/复用、逐 record 验证与回源的真实 Server 进程恢复证据，并强制保留原始身份链与 Artifact pin；缺失即失败。后续真实模型效果测试可使用 [本机实验入口](iu-local-quality-experiment.md)，并与本 CI 的确定性证据分开。

新增20样本四臂 pilot 的80个样本臂与215次fixture invocation，真实Provider调用0，token/费用保持unknown。独立主机/Provider/aicloud验收在报告中始终明确NOT_RUN。慢速 workflow_dispatch 包/镜像验证与正式publish分开，详见 [操作手册](../operations/iu-controlled-rollout.md)。


## v5 覆盖与补证门禁

旧 80 臂 pilot 保持。新增 iu-coverage-protocol-v1 的 18 个固定变体、72 个样本臂，分别保留初次与修正后结果、每轮失败/未知、原字段保留、错放、实际调用与验证/字节开销；缺失用量不记零。通过样本不能掩盖未运行样本，旧报告不重标。

本机可运行 `go run ./cmd/iu-experiment --coverage-plan --simulate --output /tmp/iu-coverage-new.json`，输出路径必须不存在。该模式不联系 Provider。无 --simulate 时，--coverage-plan 配合 --model/--runtime/--expected-cli-version/--effort 仅冻结协议；--coverage-protocol FILE 是真实 CLI 入口，本轮未执行。原始失败保存在 samples.attempts/invocations，实际模型 revision 未核实不能推导 H0～H9。

ci-iu-graph 的 required_checks 包含覆盖编译、严格源字段、独立回执、物理证据丢失、配额、事务回滚、审批、排空、并发/重放、CLI CAS/租约和 v25→v26 恢复。缺失或 SKIP 仍失败；业务 UNKNOWN 是可以被正确检测的结果，不是跳过测试。

进程报告 native_iu_graphs.coverage_recovery 必须保存初次 FAIL、修正 PASS、真实 Task/Attempt、generation=2、单次 publication、读次数不变、旧 Context 不变、来源 pin 和 Server 进程重启后 CLI 重放。仅保存内存对象、手写报告、单元 fixture 或文档不满足该门禁。普通 Runner 的两个 Worker 不替代独立主机验收。
