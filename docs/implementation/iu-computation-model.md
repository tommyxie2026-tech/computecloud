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

当前分支 Server schema v26（上一轮 v25；本轮追加覆盖回执/恢复提案与发布事实）（原基线 v20，顺序迁移 v21～25），Worker schema v6；新增能力通过明确合同/capability 协商。默认 graphs_enabled=false。图请求 256 KiB、32 节点/128 边/并行 8、Context 64 KiB、delta 64 KiB 等既有上限未扩大。

## 阶段证据

| 阶段 PR | 已核实的阶段提交 CI |
| --- | --- |
| [IU-2 #159](https://github.com/tommyxie2026-tech/computecloud/pull/159) | b3bef39：[39048549103](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39048549103) 成功 |
| [IU-3 #160](https://github.com/tommyxie2026-tech/computecloud/pull/160) | 7f44072：[39049321358](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39049321358) 成功 |
| [IU-4 #161](https://github.com/tommyxie2026-tech/computecloud/pull/161) | 286492e：[39052829931](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39052829931) 成功，含真实双 Worker fixture 的增量/全量对照 |
| [IU-5A #162](https://github.com/tommyxie2026-tech/computecloud/pull/162) | 0df966e：[39053295586](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39053295586) 成功 |
| [IU-5B #163](https://github.com/tommyxie2026-tech/computecloud/pull/163) | 9e3e76e：[39053802124](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39053802124) 成功 |
| [IU-5C #164](https://github.com/tommyxie2026-tech/computecloud/pull/164) | b21a4cb：[39055052816](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39055052816) 成功 |

[集成 PR #165](https://github.com/tommyxie2026-tech/computecloud/pull/165) 包含全部七项代码。独立审查发现的冲突深拷贝、Artifact 间接来源、来源 role/order 和 CLI 控制租约问题均有失败复现与回归覆盖；是否可合并仍以最新 SHA 的完整 CI 为准。

这些是各阶段对应 SHA 的证据。最终运行代码为 `52f20bb7be885656067b82b48f7a0c20c1d2ec1b`，[完整代码 CI](https://github.com/tommyxie2026-tech/computecloud/actions/runs/39083897816)包含慢速恢复、Linux/macOS 全量回归、镜像和 package 门禁，其结果以该运行记录为准。其 IU 报告已实际通过：93 项必测、278 个测试 PASS，0 FAIL/0 SKIP；18 个语义/故障模拟场景及 80 个 pilot 样本臂通过，真实 Provider 调用为 0。

原生恢复报告分别保存 memory/node_result 的实际撤权拒绝、重启前 0 Attempt、来源 pin 和身份链。记忆导入恢复后为 1 Task/1 Attempt，结果复用为 0 Task/0 Attempt；没有将复用伪装成执行。原始命令、SHA、环境、JSONL 与样本存放于 CI artifacts。

后续纯文档回填由 PR 最新常规 CI 检查；安装包与镜像证据始终引用实际构建的代码 SHA，不将文档提交误标成已单独打包。

本机定向 race、全仓编译、vet 和 Python gate 测试已运行。完整 Agent 子进程测试在桌面沙箱出现 CLEANUP_UNCONFIRMED，本机不能宣称全绿，原生进程/平台回归以 CI 为准。fixture Provider 调用为 0；215 次 pilot fixture invocation 不是真实模型用量。

## 明确保留未完成的验收与后续范围

- 真实 Provider 的 20 样本、实际模型 revision、usage/费用与独立主机/真实网络/长任务验收；不据 fixture 推导智力、成本节约或分布式加速。
- aicloud 真实服务与 Task→Goal/project 授权映射、发布点撤权/刷新的一致性合同及生产路由接入。只读快照不是原子许可或费用账本。
- 生产 Adapter 完整包装的精确 tokenizer、附件/二进制快照与转换器、自动语义依赖发现/检索。未知不记零，摘要不替原文，不保证任意拆分语义无损。
- 外部签名 verifier/信任 key registry、通用长期记忆、任意循环、模型内部 token/KV 状态拆分、新执行引擎。
- workspace patch DAG 与不可回滚外部写入的 effect/outbox/补偿/批准合同。Task retry/reuse 不证明外部 exactly-once。

运行入口与对照见 [订单示例](../../examples/iu-graph/README.md)、[CI 方案](../validation/iu-ci-simulation.md)、[本机模型试运行](../validation/iu-local-quality-experiment.md)。


## 下一轮 v5：约束覆盖与受限修正

本轮实现 [v5 合同](../contracts/iu-graph-v5.md)：声明约束到来源/节点/最终检查，缺少已声明原文时发布前阻断；管理员有限订单域 checker 独立检查，生成可追溯回执；v5 Goal PASS 强制引用当前完整覆盖证据，验证实际 Artifact 可取回。不能由最终金额碰巧正确掩盖身份字段损坏，也不把未知语义自动判成完整。

可信业务 FAIL 后，上层 CLI 使用幂等回源回执构建受限提案，经原 Guard 发布新代。每 Goal 最多两次，原有更严格预算、相同证据拒绝和审批合同继续有效；成功次数持久化，旧输入不改写，普通重规划入口不能绕过 v5 限制。

新实验协议是六场景 × 三种变体 × 四臂，共 72 条结果，与旧 20/80 pilot 分开。当前本机确定性模拟实际执行 210 次 fixture invocation，保留 90 条轮次结果及失败/未知，Provider 调用 0、token/费用 unknown；这些不是质量或节约结果。CI 门禁另核对实际 Server/双 Worker 的恢复、重启和重放事实。验收状态须查看本轮集成 PR 当前 SHA，不引用上一轮 CI 冒充本轮已过。

真实 Provider、生产 aicloud 与独立主机继续未完成。本机原生 Worker 探测仍受 CLEANUP_UNCONFIRMED 约束；未绕过进程安全检查，完整平台门禁由 CI 取证。
