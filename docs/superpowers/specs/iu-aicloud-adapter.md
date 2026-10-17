# IU-5C aicloud 适配器与试运行评测

状态：代码/模拟契约已实现，真实服务及 Provider 验收未完成；未开启 Server 生产路由。

## 已核实的上游事实

核实 aicloud main commit `97506084408eb9e77c60dcaeea059580469be2f4`：

- [OpenAPI v0.1.0](https://github.com/tommyxie2026-tech/aicloud/blob/97506084408eb9e77c60dcaeea059580469be2f4/docs/implementation/contracts/openapi-v1.yaml) 声明 `/api/v1/models/{model_id}`、`/tasks/{task_id}/routes`、`/tasks/{task_id}/evaluations` 的 GET。路由 POST 和模型执行 POST 是副作用入口，本适配器不调用。
- [RouteDecision / Model](https://github.com/tommyxie2026-tech/aicloud/blob/97506084408eb9e77c60dcaeea059580469be2f4/internal/domain/types.go) 提供 modelVersion、deploymentId、policyVersion、evidenceVersion、artifactDigest、evaluationVersion 等字段。
- [Evaluation](https://github.com/tommyxie2026-tech/aicloud/blob/97506084408eb9e77c60dcaeea059580469be2f4/internal/evaluation/evaluation.go) 提供 Config/ConfigDigest/Gate，Config 绑定模型、prompt/workflow/tool/permission、预算、dataset、evaluator 版本。DigestConfig 是带 `sha256:` 前缀的规范化摘要，map 转排序 key/value 列表；本地 adapter 做兼容核对，不重实现评估策略。
- [Router.Plan](https://github.com/tommyxie2026-tech/aicloud/blob/97506084408eb9e77c60dcaeea059580469be2f4/internal/router/plan.go) 与持久发布有明确边界。没有发现可直接称为 IU 专用原子授权租约的 API；不能假设远程只读快照等于事务内撤权证明。

OpenAPI 审计文件 hash 为 `f80ac1ae9054ad867dc9871c00e1dff579fd83d144262c1e3f9c0124465b2ce9`。这标识已核实的合同，不证明被访问服务实际部署的是同一 commit。

## 代码边界

internal/aicloudadapter 提供只读 Client.Fetch 与 Freeze：指定精确 Task/model/route/evaluation ID、模型版本/Artifact digest、deployment、policy/evidence、Evaluation Config digest。HTTPS（测试只允许 literal loopback HTTP）、总超时 3 秒、单响应 1 MiB、分页最多 4 页每页 32 项；重复 ID、分页环、限流/不可用/预算错误/重定向均拒绝，不调用 fallback 或换模型。

冻结快照核对 active/approved/healthy、精确版本/Artifact、deployment、评估 Gate 与规范化 Config digest、完整 evaluator/dataset 版本及路由新鲜度（最多 15 分钟）。保存原响应摘要和所有决策来源，绑定是独立 `iu-aicloud-binding-v1` 本地 envelope，不冒充 aicloud 已有 IU API。

Registry/Evaluation/Router 仍属于 aicloud；本包不承担选择或评估政策，不替代 computecloud principal、credential、project、Worker capability、deadline 或预算硬过滤。aicloud 的模型评估 PASS 也不等于当前 Goal 业务 PASS。

## 生产接入前仍需的联合合同

真实 aicloud 配置/认证及授权的 Task→computecloud Goal/project 映射、部署 commit/合同协商、撤权/模型下线与发布点的一致性、失联处理和刷新/过期语义仍未验收。当前 Client 未接入 Server 自动路由；没有可线性化的远端授权凭据时，不能把远程读结果作为事务内永久许可。将来由上层先得到明确决策，在新代冻结为执行绑定，执行中不得静默重路由，失效走 IU-2/Guard。远程接入另案 PR，默认关闭。

## 试运行协议

`iu-experiment --pilot-plan --model MODEL --runtime codex_exec --expected-cli-version VERSION --effort low --output pilot.json` 仅生成冻结计划，无模型调用。4 个固定场景×5 次重复=20 样本，4 组：monolithic、iu、iu_no_memory、iu_no_verifier，共 80 个样本臂、最多 215 次 CLI 尝试；按样本旋转组顺序。不是 20 个独立任务分布样本。

`--protocol pilot.json --output report.json` 核对协议 hash、prompt/Adapter/模型声明、实际 CLI 版本，拒绝覆盖关键参数；每次调用 60 秒、总试运行 1200 秒。被取消/预算耗尽未运行的样本保留在分母，单独标记 not_attempted，不编造延迟。精确费用上限不支持，FeeHardLimit 必须 null；实际成本来源保留，缺失不记零。声明模型标识不证明 CLI 背后 Provider revision 已核实。

探索性非劣界限 0.05、成本比目标 0.8 是实施时的可审阅试运行默认假设，随协议冻结，不是实测结果；正式功效分析未完成，不作显著性或普遍收益结论。正式扩样前另行预注册功效/样本量/置信区间。原始失败样本、总分母/有效输出、业务通过/误接纳、字段保留分母、P50/P95、调用、验证开销、实际 usage/cost 来源都进入报告。费用口径须包含失败/重试/检索/验证；未知则不判收益达标。

增量 IU 使用原生 Task/Attempt 对照证据（IU-4），与本机模型语义实验分开保存，不通过本地缓存伪造授权或原生 Attempt 节省。真实 20 样本、较大模型对照、远程 aicloud 和独立主机运行按用户决定保留未完成。
