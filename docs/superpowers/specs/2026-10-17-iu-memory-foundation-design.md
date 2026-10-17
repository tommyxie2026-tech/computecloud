# IU Memory Computation — M1 Design

日期：2026-10-17。状态：待用户审阅；分支feat/iu-memory-computation，基线34eeace47320d9b79e90ff449a74cd9bf7cdfd95。

## 目标与分批边界

用户已要求从研究转向computecloud分支实现。完整目标是固定记忆读取→算元执行→候选增量→接纳回执→下游消费→独立验收。采用三批实现，不能把第一批基础库宣称为完整执行图闭环：

- **M1 本计划**：不可变记忆记录/视图/增量契约、可重复的输入编译、基于当前已完成Attempt的持久接纳和授权读取、重启幂等性。
- **M2 后续独立计划**：多阶段有限执行图、按边输入授权、节点等待必需回执、真实Worker产出/消费记忆、可信command节点。
- **M3 后续独立计划**：业务PASS/FAIL/UNKNOWN、全局验收、Guard图修订及显式历史复用。

M1在真实SQLite执行数据上可独立验证来源、接纳、读取与编译，但不改变v0.2 Job执行模式、不实现任意DAG、不允许上层伪造Task状态。M1的验收必须准确写成“记忆接纳基础层”，不能以fake Runtime测试推断模型效果。

设计依据：[devdocs记忆闭环规范](https://github.com/tommyxie2026-tech/devdocs/blob/ceee21fee0945d0c1f84210c0e4b0e198fbb6f52/docs/research/intelligence-unit/memory-computation-closure.md)及[受控执行图演化](https://github.com/tommyxie2026-tech/devdocs/blob/ceee21fee0945d0c1f84210c0e4b0e198fbb6f52/docs/research/intelligence-unit/computecloud-graph-evolution.md)。

## 必须保持的不变量

1. Task/Attempt、租约、cleanup、generation、owner/project仍是唯一执行事实。
2. execution accepted、memory admitted和outcome accepted分开；M1不创建真实业务“正确”保证。
3. M1全部内容型记录初始为unchecked；supported/refuted需M3可信证据协议，不信任模型自标。
4. 只接纳本Goal当前Plan/Graph中、当前Task成功且released的Attempt及其ACCEPTED Artifact。失败、ORPHANED、旧generation、未知来源拒绝。
5. 同接纳键同内容返回同receipt，不同内容冲突。语义重复另按显式source_ids检测。
6. 固定View只读，必需缺失或冲突不能自动跳过；不读取latest或跨Goal隐式导入。
7. 并行Delta分别接纳，View显式列出所选receipts；没有共享可变全局记忆头。
8. 无新执行状态机、模型Registry、向量数据库、通用Workflow或自动Re-plan。
9. 功能默认关闭；旧Job/报告/Artifact/HTTP接口行为不变。

## 纯契约

版本为memory.v1。Decode使用internal/jsonutil，拒绝重复键、未知字段、多文档、非法UTF-8。最大单Delta 64KiB、32条Record、每Record payload最多8KiB，source_ids/depends_on各至多64；ID为1..128可打印ASCII且不含空白。超限拒绝而非截断。

Record字段：id、kind(observation/hypothesis/decision/constraint/unresolved/result)、payload(JSON对象)、source_ids、depends_on、validation(仅unchecked)。

CanonicalDelta使用固定内部JSON表示：schema_version/read_view_hash/output_port及按Record ID排序的records；每条Record以payload_utf8字符串保存解析时的原始payload对象文本，其余字段及集合固定顺序。该内部hash表示不是提交Schema，数字词法和payload空格变化会改变hash；原始Delta bytes另存。避免json.RawMessage重新marshal压缩payload后误称原文保全。

View字段：schema_version、snapshot_hash、goal_spec_hash、receipt_ids(显式集合)、required_record_ids(显式集合)。hash均为小写SHA256。集合字段按ID排序并拒绝重复；不重写payload中的Unicode、空格或数字词法。固定序列化规则与版本，不宣称实现RFC8785。

Delta字段：schema_version、read_view_hash、output_port、records。完整Source在服务端由Task/Attempt/Artifact关系解析，不接受payload自行声明的owner或Worker身份。

Compile只生成可观测输入消息文本与hash/read-set报告，接受最大UTF-8 bytes；默认不声称精确Token预算。后续Provider提供固定Tokenizer才可附真实Token计量。

## 接纳与读取

M1先提供Server内部service，不开放外部写记忆HTTP端点，避免缺少可信Artifact解析时由客户端任意写事实。

AdmitMemoryProjection(ctx, jobID, artifactID, outputPort, deltaBytes, expectedViewHash)：
- 解析/检查在事务外有界执行；
- 短事务内按现有Job、Goal、Task、Attempt、Artifact重新核对权限归属、来源、当前版本、成功终态和固定hash；
- records、依赖、receipt、artifact_refs pin原子写入；
- 不调用模型或执行shell，不改变Task执行终态。

M1调用入口限Server集成测试与后续可信Worker/projection接入；必须在README明确“尚未自动接收任意真实Runtime输出”。没有M2不得声称已完成产品闭环。

提供GET /v1/jobs/{id}/memory/receipts/{receipt}和GET /v1/jobs/{id}/memory/views/{view_hash}，使用jobs:read及现有Job owner/project授权，跨Goal与跨Job不可见。M1没有公开创建View端点：内部CreateMemoryViewTx使用明确receipt集合形成不可变View。

## 持久化

新增Server migration（以当前19为基线下一版20；实施前若main改变则重新分配），Worker schema不变：

- memory_receipts：id、goal_id、job_id、task_id、attempt_id、artifact_id、artifact_hash、output_port、plan_revision、graph_generation、delta_hash、read_view_hash、created；接纳键唯一。
- memory_records：receipt_id、record_id、kind、payload、source_ids、validation；复合主键。
- memory_dependencies：record引用与确切源记录关系，禁止未解析依赖。
- memory_views：view_hash、goal_id、job_id、canonical_json、created；immutable。
- 继续使用artifact_refs pin；有receipt/view引用不得被GC清掉。

没有持久化“是否正在运行IU”字段。receipt/view immutable触发器拒绝UPDATE/DELETE；未来合法留存删除需专门协议，M1不新增自动清理。

重启时无需重复执行Task；重复调用service返回同receipt。M2自动projection的reconcile扫描和节点激活不在M1内，不能把缺少调用触发器描述为已自动恢复。

## M1验收

在真实store.Open(ServerSchema)及现有Server执行fixtures上测试：合法已完成Attempt接纳、重复/冲突、事务回滚、关库重开后重读、ORPHANED/失败/陈旧/跨租户拒绝、必需记录缺失、UTF-8及预算拒绝。所有负例不得改变Task或创建新Attempt。

测试不调用真实模型，不消耗账号额度。完整基线make test和相邻现有测试通过后才声称代码通过；真实Runtime/多阶段图效果另做M2验收。当前工具链获取失败，因此尚未运行基线。

## 后续阶段的完整闭环

M2必须产生真实Artifact→Server projection→receipt→下游固定View消费，并通过Server重启/取消竞态测试；M3必须对原始目标独立判分、区分业务FAIL与基础设施失败，并走Guard发布新generation。未实现这些前，保持能力标注为memory foundation。
