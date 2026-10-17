# IU-5A 上下文、计数与有界回源

状态：设计/实施中。v4 图新增可选 node.context_policy：最大序列化字节数、显式必需证据 spans。原来源是不可变 UTF-8 字节快照，保存角色、消息顺序、hash；上下文选择是来源的有证据子集，不把摘要当原文。全局 Rules 与局部 Rules 始终保留，超限显式失败。跨片段指代依赖必须由上层声明 required_evidence；系统不声称能自动发现任意语义依赖。

Token Counter 契约绑定 model、tokenizer/version、template/version、Adapter/wrapper version、精确序列化输入 hash、覆盖范围。计数器只提供输入预算证据，实际 Usage 沿用现有 Attempt 用量。更换任意绑定就必须重新计数，禁止拼接跨 tokenizer token IDs。没有通过完整 Adapter 包装 contract test 的计数器时 TokenCount=nil/coverage=unknown；生产 CLI 内部包装不可观测，不开启精确 token 硬预算。fixture 计数器只验证合同，不能拿 UTF-8 字节或 rune 数冒充真实模型 token。

回源由有 jobs:read 的上层客户端显式请求 Server 的冻结来源 span，绑定 Goal/generation/node 与 plan hash、幂等 ID。每节点每代最多 2 次，每次最多 16 KiB，角色/消息序号与来源 hash 一并返回并持久审计。相同 ID 同内容幂等，冲突拒绝；Server 重启不重置限额。Worker 没有绕过权限的任意文件/URL 读取入口。读取不会修改正在执行的 Context；上层只可通过既有失败评估/Guard 提交新代 span 选择、重新拆分或升级模型，沿用总 deadline/重算预算。

附件另案：未来快照需 media_type、byte_size、sha256、immutable Artifact pin、project/Goal 授权、转换器 name/version、转换后来源和原二进制来源的映射。读取有大小限制与 GC pin，文本转换摘要不替代原始附件。首版不接受二进制、URL或附件摘要冒充 source.text，未知字段/媒体类型拒绝；此版本只支持嵌入式 UTF-8 text，原文保存在 immutable frozen plan，不另建不受控存储。

计数器超时/不可用保持 unknown；必需证据缺失为 CONTEXT_INSUFFICIENT。任何回源、摘要或分区都不能保证任意任务语义无损；只有原文重建可以逐字节验证。
