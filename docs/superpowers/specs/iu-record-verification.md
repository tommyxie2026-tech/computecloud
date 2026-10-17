# IU-5B 独立 record 验证与概率证据

状态：设计/实施中。首版使用管理员注册的只读 source_json_equal 验证器：对冻结来源 JSON 的指定 pointer 与 record payload 指定 pointer 做严格比较。只验证该 source-field 作用域，不宣称任意业务正确或语义真理。另有 unavailable profile 明确返回 UNKNOWN。不接收调用者提交的 verdict/verified，不运行模型生成的命令。

jobs:read + goals:verify 独立权限在写回执事务重查；绑定 Goal/generation/node、原 View/record hash、validity revision、来源 ID/hash、profile/version/config hash、scope 和实际证据 hash。只支持管理员配置的命题种类，JSON 重复键、缺失 pointer、超时/不可用都 UNKNOWN。验证结果不可变，证据可从冻结原文与原 record 重建，保留来源 Artifact pin。

信任根为管理员 registry 与经过身份鉴权的 Server 自身计算；首版不接受外部签名回执，不能用任意签名字符串绕过。未来远程验证器必须另行定义可信 key registry/签名格式/撤销。profile 配置变更、来源失效、revision/View/record 变化使旧回执不再适用；IU-2 仍传播失效。

验证投影独立包装 iu-verified-memory-v1，带 scope 与逐条 applies 状态，只把确切被当前 PASS 覆盖的 VALID record 在该作用域视图中标为 verified。历史 MemoryView 不改写，原 GraphMemory/跨代导入/Worker 输入继续原始 unchecked 合同，导入始终重置 unchecked。验证不自动改变 Goal PASS 或原 Task/Attempt。

概率证据单独描述 Evaluation profile/version、dataset hash、样本分母、成功数与 Wilson 95% 区间；n=0 保持 unknown。经验覆盖范围不是 Hoare 确定性证明，不能由模型置信度赋予 verified。对照报告包含误接纳、漏检、额外 verifier 调用、实际延迟和重试，fixture 不冒充真实模型评估。
