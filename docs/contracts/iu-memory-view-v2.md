# IU memory view v2：随计算传递原始身份链

状态：代码/定向回归通过，集成 CI/最终审查待完成。没有 origin 的旧视图保持 iu-memory-view-v1 原格式和 hash。

授权导入/结果复用产生的记录使用 iu-memory-view-v2，新增 origin（iu-record-origin-v1）：原 Goal/generation/job/node、源 Context hash、View hash、record ID/hash 和 previous origin。依赖/supersedes 使用新代稳定 ID，payload 不改写；模型可通过 inline origin 找回原始 record 身份，并结合 Source/span/hash 与有界回源核对原文。原始信息不是只留在 Server 回执中。

origin 由 Server 在授权发布事务中读取冻结来源 Context 后生成，模型 candidate 不能携带 origin。链最多 8 层，Goal 相同、代数严格递减；超过限制明确拒绝。来源 record hash 绑定包含上次 origin 的原始 record， previous 链不原地改写。导入仍为 unchecked，origin 不是 verified 或授权本身。

具有 origin 的 view 会随 MergeViews / ApplyDelta / Invalidate / 显式 join 继续传递，编译后的 Context.text 包含完整来源链。每次仍向目标模型重新编码序列化文本，不传递/拼接源 tokenizer 的 token IDs，也不存模型隐藏思维链。原文可回溯与任意拆分语义完整是不同命题；遗漏的语义依赖仍需 RequiredEvidence/独立验证发现。

Server 给实际消费该 view 的 Task 添加 iu_memory_origin_v1；新 Worker 宣告该 capability 并在执行前校验 view 版本/hash/来源链及协商位。旧 Worker 不接收新记忆 wire。原 v1 输出/旧图没有 origin 时不增加该要求。Context 字节预算包含新增来源链，超限返回不足，不截断。

查询来源仍需当前 jobs:read，跨代使用仍需一次性 goals:import 和完整闭包；内容摘要不能代替授权。GET /graph/record/{node}/{record}（CLI job graph-record --id JOB --node NODE --record RECORD）返回 Server 规范化 record hash、view hash 和 validity revision，避免客户端因 JSON key 顺序差异自行算错 identity。

## 来源覆盖与失效

新候选在 Worker 与 Server 两侧绑定冻结 Context 实际包含的全部直接 source refs，不能由模型只选择有利来源。去重前校验候选来源，超过每条 64 个来源则明确失败，不能截断 provenance。间接记忆仍绑定完整 read-view 依赖。升级前漏报来源的记录，在失效投影时额外核对冻结生产 Context；找不到生产上下文时保持 NEEDS_REVALIDATION。该保守处理可能扩大重算范围，不构成任意语义拆分正确性的证明。

Artifact 输入尚无完整可证明的内容依赖链；存在这类输入的生产 Context 不因直接 spans 为空被视作独立。源或 record 失效时，其派生记录保守投影为 NEEDS_REVALIDATION，除非已有直接来源/记忆依赖足以判 INVALID。跨代 import 还必须保持实际所读来源的 Role 与 MessageIndex，一致文本 hash 不能替代角色和顺序匹配。
