# IU 计算模型：实现范围与验收证据

本轮执行 [IU 后续迭代计划](../superpowers/plans/2026-10-18-iu-follow-up-iterations.md)，从 main `b38e4c408310464bd1a74d4e82362736bb405d55` 延续。代码已覆盖 IU-2～IU-6 的本轮代码/CI 范围；合并依据是集成 PR 当前 SHA 的 CI 与最终审查，不能只凭本文认定验收。真实 Provider、aicloud 服务、独立主机验收按用户决定保留未完成。

IU 是现有 Task/Attempt 上的算元契约：冻结有限 DAG → 节点就绪 → Task/Attempt → Worker 冻结输入 → Artifact 准入 → Memory/依赖传递 → 独立业务评估 → Guard 发布下一代。复用是单独的持久事实，不创建假 Attempt；没有第二套执行引擎。aicloud 保留 Registry/Evaluation/Router 职责。

## 已实现的代码范围

| 阶段 | 行为与合同 |
| --- | --- |
| IU-0/1 基线 | agent/command/join、具名 Schema、三值分支、有界 DAG、Artifact 准入与恢复；原 single/map_reduce 不切换 |
| IU-2 持久有效性 | [失效规格](../superpowers/specs/iu-durable-invalidation.md)：独立权限、幂等事件、revision/CAS、重启重放、激活/准入/PASS/Replan 同事务复查；已终结 Goal 仅审计，不自动重开 |
| IU-3 跨代 Memory | [v2 图合同](../contracts/iu-graph-v2.md)：同项目/Goal 一次性授权、完整闭包、目标计划/代绑定、撤权/过期/issuer 复查、不可变快照与来源 Artifact pin；导入仍 unchecked |
| IU-4 增量结果 | [v3 图合同](../contracts/iu-graph-v3.md)：管理员冻结只读 replay binding，Worker runtime/policy/capability 复核，实际输入/Schema/算元/模板/环境/工作区 key 相同才 REUSE；不确定则普通 Task；当前代仍独立验收 |
| IU-5A Context/token | [v4 合同](../contracts/iu-graph-v4.md)：必需证据覆盖、UTF-8/字节预算、固定 Counter/receipt 契约、Worker 输入版本/模型/hash 核对、有界回源；生产 CLI 的精确 token 上限仍拒绝/unknown |
| IU-5B record 验证 | [独立验证合同](../contracts/iu-record-verification-v1.md)：只读管理员 profile、逐命题/scope 回执、来源 pin、保留冲突的受限投影、Goal 显式 proof 引用复查；不改变原始输入或自动 PASS |
| IU-5C 评测/aicloud | [适配/试运行规格](../superpowers/specs/iu-aicloud-adapter.md)：20 样本四臂协议与消融、全分母/未知成本报告、80 臂 fixture；按核实上游合同提供只读证据 adapter，未开启 Server 生产路由 |
| IU-6 代码门禁 | [操作手册](../operations/iu-controlled-rollout.md)：v20→v25/备份/回滚快照测试，真实 Server 进程导入/复用恢复、关闭提交后排空、必测缺失/SKIP 即失败、手动慢速包/镜像构建 |

含原始身份链的导入/复用记忆使用 [memory-view-v2](../contracts/iu-memory-view-v2.md)：源 Goal/job/generation/node/Context/View/record 与 previous origin 随 Context 传给 Worker，payload 不改写；元数据依赖映射到新 ID，模型不能伪造 origin。需要 iu_memory_origin_v1；无 origin 的旧 v1 格式/hash 保持。主文保存在冻结 Sources，可按 byte span/hash/角色/顺序回源，不传递模型隐藏思维链或跨 tokenizer token IDs。

当前分支 Server schema v25（原基线 v20，顺序迁移 v21～25），Worker schema v6；新增能力通过明确合同/capability 协商。默认 graphs_enabled=false。图请求 256 KiB、32 节点/128 边/并行 8、Context 64 KiB、delta 64 KiB 等既有上限未扩大。

## 阶段证据

| 阶段 PR | 已核实的阶段提交 CI |
| --- | --- |
| [IU-2 #159](https://github.com/tommyxie2026-tech/computecloud/pull/159) | b3bef39：[39048549103](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39048549103) 成功 |
| [IU-3 #160](https://github.com/tommyxie2026-tech/computecloud/pull/160) | 7f44072：[39049321358](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39049321358) 成功 |
| [IU-4 #161](https://github.com/tommyxie2026-tech/computecloud/pull/161) | 286492e：[39052829931](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39052829931) 成功，含真实双 Worker fixture 的增量/全量对照 |
| [IU-5A #162](https://github.com/tommyxie2026-tech/computecloud/pull/162) | 0df966e：[39053295586](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39053295586) 成功 |
| [IU-5B #163](https://github.com/tommyxie2026-tech/computecloud/pull/163) | 9e3e76e：[39053802124](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39053802124) 成功 |
| [IU-5C #164](https://github.com/tommyxie2026-tech/computecloud/pull/164) | b21a4cb：[39055052816](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39055052816) 成功 |

这些是各阶段对应 SHA 的证据；集成阶段新增 origin wire 与恢复门禁，需以[集成分支 CI](https://github.com/tommyxie2026-tech/computecloud/actions?query=branch%3Afeat%2Fiu-recovery-release-gates)当前 SHA 的结果为准。报告包含命令、SHA、环境、JSONL 与原始样本。

本机定向 race、全仓编译、vet 和 Python gate 测试已运行。完整 Agent 子进程测试在桌面沙箱出现 CLEANUP_UNCONFIRMED，本机不能宣称全绿，原生进程/平台回归以 CI 为准。fixture Provider 调用为 0；215 次 pilot fixture invocation 不是真实模型用量。

## 明确保留未完成的验收与后续范围

- 真实 Provider 的 20 样本、实际模型 revision、usage/费用与独立主机/真实网络/长任务验收；不据 fixture 推导智力、成本节约或分布式加速。
- aicloud 真实服务与 Task→Goal/project 授权映射、发布点撤权/刷新的一致性合同及生产路由接入。只读快照不是原子许可或费用账本。
- 生产 Adapter 完整包装的精确 tokenizer、附件/二进制快照与转换器、自动语义依赖发现/检索。未知不记零，摘要不替原文，不保证任意拆分语义无损。
- 外部签名 verifier/信任 key registry、通用长期记忆、任意循环、模型内部 token/KV 状态拆分、新执行引擎。
- workspace patch DAG 与不可回滚外部写入的 effect/outbox/补偿/批准合同。Task retry/reuse 不证明外部 exactly-once。

运行入口与对照见 [订单示例](../../examples/iu-graph/README.md)、[CI 方案](../validation/iu-ci-simulation.md)、[本机模型试运行](../validation/iu-local-quality-experiment.md)。
