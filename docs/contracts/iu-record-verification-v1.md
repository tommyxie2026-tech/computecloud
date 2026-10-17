# IU record verification v1

状态：代码/定向验证中，CI/最终审查待验收；Server schema v25。独立接口，不改变已有 v1～v4 Worker 输入或自动授予 Goal PASS。

管理员在 jobs.graph_record_verifiers 注册 profile：version、kind=source_json_equal、scope、source_pointer、claim_pointer、record_kinds、timeout_ms（1～1000）。只运行内置只读比较器，不执行客户端/模型命令。kind=unavailable 明确 UNKNOWN。来源及 claim 必须是无重复键的 JSON，pointer 缺失/超时为 UNKNOWN。比较保留精确 JSON number 表示，不能因浮点舍入误判相等；1 与 1.0 的不同表示也保守为 FAIL。

POST /v1/jobs/{id}/graph/verifications（CLI job verify-record --id JOB --file request.json）：

```json
{"id":"proof_a","node_id":"extract","record_id":"raw","record_sha256":"<原record摘要>","view_sha256":"<原view摘要>","expected_revision":0,"profile":"order","profile_version":"1","source_id":"order"}
```

需要 jobs:read、goals:verify，同事务检查 managed key、项目/owner、当前代、revision、View/record、VALID、无冲突、profile/version、record 声明的精确来源。调用方不能提交 verdict；同键不同内容拒绝，每代最多 128 个回执。Server 计算 PASS/FAIL/UNKNOWN，持久保留完整 profile hash、scope、来源及命题摘要、证据 hash、实际耗时，并 pin 来源 Artifact。证据可由冻结 Source/record 与 profile 重建。信任根是管理员 profile 和认证 Server；首版不接受外部签名回执。

GET /v1/jobs/{id}/graph/verified-memory/{node}?scope=source-paid 返回 iu-verified-memory-v1 包装：原 View hash、scope、逐回执 applies 与独立投影。只有当前 profile/hash/revision/View/record 仍匹配的 PASS 才覆盖该条记录；冲突及所有候选保留。其他记录保持 unchecked，历史 View 不修改。普通 GraphMemory、Worker 和授权导入继续使用原始输入，跨代导入重置 unchecked。旧证明在 IU-2 反证、profile 修订或换代后不再适用。

GraphEvaluation 可选 record_proofs 列表（node_id、verification_id、receipt_sha256、scope），评估事务复查每个回执当前适用性。PASS 不能引用 FAIL/UNKNOWN、旧版或伪造回执；未声明 record_proofs 的原有独立评估合同保持不变。一次 source-field PASS 不等于全部业务命题成立。

EmpiricalEvidence 单独记录 Evaluation profile/version、dataset hash、样本/成功数和 Wilson 95% 区间；0 样本保持 null。该区间只属于对应抽样任务分布，不能当作 Hoare 确定性证明，也不参与模型自授 verified。fixture 对照保留错误/未知样本、误接纳/漏检、调用/实际耗时/重试，不宣称真实模型效果。
