# IU graph v4：上下文证据与有界回源

状态：代码/定向验证中，CI/最终审查待验收。Server schema v24；Worker 输出和 Usage 不变。默认关闭。

v4 继承 v3，新增 node.context_policy：

```json
{"context_policy":{"version":"iu-context-policy-v1","max_bytes":65536,"required_evidence":[{"source":"message","start":0,"end":12,"sha256":"<来源全文摘要>"}]}}
```

必需证据必须由实际选中的 rules/spans 完整覆盖（可以重叠），并核对来源、hash、UTF-8 边界。声明证据不等于自动回源；缺少则 CONTEXT_INSUFFICIENT，不能靠摘要顶替。字节预算包含序列化 Context，最多 64 KiB，不能删除全局/局部规则来凑限额。

InputCounter 契约与 receipt 绑定 model、tokenizer、template、Adapter 包装版本及实际输入 hash。只有完整请求包装 contract test 通过才可执行精确输入 token 限制；当前生产 CLI 包装不可完整计数，max_input_tokens 和 Goal max_tokens 仍拒绝，token_count 保持 null/unknown。fixture 计数器只证明绑定/CAS/超限检查。实际 Usage 沿用 Attempt 现有账本，缺失值不是 0。

Worker 复核实际 prompt 的版本、模型、hash 和完整输入 fingerprint；模型切换、包装变化不沿用旧计数或 token IDs。token 序列不跨模型组装。

POST /v1/jobs/{id}/graph/source-reads，CLI job read-source --id JOB --file read.json：

```json
{"id":"read_a","node_id":"extract","plan_sha256":"<规范化计划摘要>","expected_reads":0,"span":{"source":"message","start":0,"end":12,"sha256":"<来源全文摘要>"}}
```

jobs:read 权限在事务重查（含 managed key 撤销）；每节点每代 2 次，每次 16 KiB UTF-8，expected_reads CAS。相同 ID/内容幂等、冲突拒绝，持久回执保留 byte span、来源 hash、切片 hash、角色和消息序号，重启不重置限额。输入原文在冻结 Plan 保留；读取不修改 Context，也不创建 Task。客户端根据结果，经既有失败评估/Guard 发布新的证据选择、拆分或模型升级；Worker 不能任意回源。

只支持嵌入 UTF-8 text，附件/二进制未知字段拒绝。未来附件 snapshot/转换器版本/授权/大小与 GC pin 设计见阶段规格，尚未实现。原文重建可逐字节验证，任意拆分语义完整性仍是待验证假设。
