# IU 计算模型：本次实现范围与验收

分支 feat/iu-computation-model，从 main 的 006792b0994f7fcc67588684053b8f8e6a965785 建立。对应 [设计规格](../superpowers/specs/2026-10-17-iu-computation-model-design.md)、[实施计划](../superpowers/plans/2026-10-17-iu-computation-model.md)、[运行契约](../contracts/iu-graph-v1.md) 和 [订单示例](../../examples/iu-graph/README.md)。

本次交付可运行的实验性闭环：冻结 DAG → 就绪节点绑定 Task → Worker 执行冻结上下文 → Artifact 驱动记忆准入 → 显式 join → 独立业务评估 → 现有 Guard 发布下一代。计算模型由固定 map_reduce 扩展到有限多阶段图；没有建立第二套执行引擎。

## 实际交付

| 范围 | 实现及证据 |
| --- | --- |
| 算元与图编译 | agent / command / join、具名冻结 Schema、artifact / memory / control 边、三值分支、环与限额拒绝、确定性拓扑 |
| 输入与 token | 原始 UTF-8 文本、来源 hash、角色/顺序元信息、字节 span；全局规则注入、实际上下文 hash、超限显式失败；token 数未知 |
| 记忆 | 固定 read-view、来源约束、保守完整 read-set、不可变 record、冲突候选保留与传递失效投影；模型记录保持 unchecked |
| 执行与恢复 | 原 Task / Attempt fencing、iu_graph_v1 capability、节点激活事务、队列背压等待、停止排空 |
| 产物准入 | 接受 Artifact 同事务 PENDING + pin，后续有界解析及 ADMITTED / REJECTED；Server 重开恢复不重复 Task |
| 查询与 CLI | submit-graph / graph / evaluate-graph；节点 Task、receipt、重试时间、有效并行度及评估版本可查 |
| 评估与修复 | 独立 goals:evaluate 权限、PASS / FAIL / UNKNOWN、CAS、跨代隔离、deadline / cancel、FAIL 经现有 Guard 发布新代 |
| 费用边界 | 仅受支持 claude_http 估算费用串行模式；明确拒绝 max_tokens、command 混合费用与 Gateway route |
| 兼容性 | 默认关闭、已接受图继续恢复、旧 single / map_reduce 执行器不切换；Server schema v20，Worker v6 |

后续执行见 [IU 后续迭代实施计划](../superpowers/plans/2026-10-18-iu-follow-up-iterations.md)，包含 IU-2～IU-6 的阶段依赖、验收门槛和代码落点；当前仅完成规划，未开始这些功能实现。

后续迭代以[长期路线图中的 IU 台账](long-term-roadmap.md#iu-计算模型迭代台账)为准：先持久失效事件，再授权跨代 import 和依赖增量重算；真实模型基线单独取证。

## 设计中尚未交付的部分

设计规格保留完整目标，不能把上述切片误认为全部设计已完成：

- 跨代显式授权 Memory import，以及持久规则修订/反证事件在激活和业务评估时统一重查的 API。当前新代全量重算，旧结论不隐式导入。纯 Invalidate / MergeViews 已实现，但不等于运行时动态修订服务。
- 验证器签名、证据对象存储/独立 record 的 verified 提升服务。当前评估提交受权限约束，hash 绑定调用方证据，真实性由独立评估者负责。
- 自动上下文检索、tokenizer 计数、附件/二进制快照、模型内部状态拆分；没有语义无损保证。
- 工作区 patch DAG、不可回滚外部副作用、任意循环、新执行引擎和 aicloud 远程 Registry/Router 集成。
- 真实 Provider 质量、成本节约、跨主机或不可信隔离运行验收。fixture 只验证协议与状态机。

后续应先实现持久失效事件和授权 import 的事务约束，再开放按依赖范围的增量复用。当前全量重算是明确的功能边界。

## 验证方式与边界

`make ci-iu-graph` 使用 Go 1.27.2+，包括 JSON Schema/源重建/记忆投影、SQLite v19→v20、Server/Worker/command/CLI，以及两 Worker 订单 fixture 的 race 检查。Python 独立验证器测试另覆盖规则从 1000 修订为 2000 后的 10000 结果；不冒充跨代 import 验收。

本地主机已取得 Go 1.27.2。定向纯逻辑/迁移/图协议测试、全包编译与 vet 的结果记录于本次 PR；本地完整进程测试受到桌面沙箱进程身份检查/进程组操作限制，出现 CLEANUP_UNCONFIRMED，不能称为本地全绿。完整进程与平台回归以 PR 对应 SHA 的 GitHub Actions 为准。

首轮 CI 39018578316 的 iu-graph-contract（含真实两 Worker 进程 fixture）通过；其余失败定位为新增 command Provider 后旧枚举断言和 schema 版本断言未更新，已修正。最新提交的最终结论以 [PR #156](https://github.com/tommyxie2026-tech/computecloud/pull/156) checks 为准，不将早期 SHA 的结果算作最终验证。

## 效果测试入口

新增 [本机质量对照实验](../validation/iu-local-quality-experiment.md)：同模型单节点与 IU 结构化拆分，复用实际 context/output/memory 契约。CLI 启动失败和无有效输出单独记录，不虚构真实模型收益；后续原生 Worker 和跨代复用验收仍按上方边界推进。

## CI 模拟验收

[CI 模拟与原生执行方案](../validation/iu-ci-simulation.md) 增加 18 个语义/故障场景，以及独立 Server / 双 Worker OS 进程的四图验收、授权评估和 Server 硬重启证据。必测项必须 PASS，报告与 JSONL 日志作为 Actions Artifact 保存；不将模拟通过写成真实模型收益。

代码提交 `32547db` 的 [CI 39035469279](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39035469279) 已完成，35 项运行检查成功（含 Linux/macOS 全量回归）；IU 专项 7 阶段、26 项必测检查及 18 个场景通过。[Artifact](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39035469279/artifacts/12101479992) 保存对应报告和日志。此记录绑定该次代码，后续修改仍须通过最新 CI。

## IU-2 实施进度

持久失效事件、独立权限、有效性 revision、不可变投影、激活/准入/PASS/新代发布事务检查已进入实现分支；Server schema v21。源事件提交后保守停止该代计算，按每轮最多 4 个事件恢复投影；旧 Task/Artifact/Memory 历史不修改。终结 Goal 仅接收 audit_only 反证。CI 验收完成前保持“实施中”，不表示 IU-3 以后已交付。阶段合同见 [持久失效规格](../superpowers/specs/iu-durable-invalidation.md)。
