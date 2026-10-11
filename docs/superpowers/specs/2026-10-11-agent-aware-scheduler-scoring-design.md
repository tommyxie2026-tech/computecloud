# Agent-aware Scheduler 确定性软评分设计

## 状态

待用户审阅。本文定义 v0.5.x Agent-aware Scheduler 的第一阶段评分行为；在规格获批前不实施代码变更。

## 背景

Worker 已通过 `ExecutionSignal` 提供有界、短时有效的 readiness 提示，Server 会记录候选解释，但这些信号当前不影响选 Worker。现有调度器先按资格和容量筛选，再在合格 Worker 中选择活跃任务数最少者，并对并列候选轮换。长期路线图要求增加 Environment readiness、Prepared Workspace/Repository affinity、Runtime warmness、负载与空闲槽位等可解释因素。

本变更只把已存在的观测提示用于合格 Worker 之间的软排序，不建立新的事实源或调度产品边界。

## 目标

- 在所有既有资格检查通过后，优先选择预计准备成本较低、工作区/仓库可复用且容量充足的 Worker。
- 评分结果在相同候选、信号、配置和调度状态下完全确定，并能解释每个主要分值来源。
- 通过显式配置开关启用；默认关闭时保持现有 least-active 与并列轮换行为。
- 旧 Worker、缺失/过期信号及未知字段保持兼容并安全退化。

## 非目标

- 不改变 Agent-aware Distributed Job Execution Platform / Agent Job Executor 的定位。
- 不建设 AI Execution OS、通用 Workflow Engine、LLM Gateway、Model Serving Platform 或 GPU Cloud。
- 不放宽在线、Runtime/能力、模板、凭据/账户、项目/安全、并发、Worker pool 或资源硬约束。
- 不引入机器学习、跨租户全局画像、持久 Worker 亲和性、GPU 拓扑放置或新的队列公平策略。
- 不把 Relay 状态用于调度评分；Relay 只负责传输。

## 决策流程与不变量

1. 先执行现有全部硬过滤。评分器只接收剩余候选；候选为空时，沿用当前阻塞原因。
2. `prepared_workspace_score_enabled=false`（默认值）时，评分器不改变决策，继续使用 least-active 与现有并列轮换。
3. 开关启用时，对合格候选计算确定性整数分值，分数越高越优。排序键依次为：分值降序、活跃任务数升序、既有 Server 轮换游标顺序。Worker ID 只用于构造稳定候选顺序，不得由 Peer 到达顺序决定结果。
4. 只有成功创建 Attempt 后才推进并列轮换游标；失败分配和预选不得推进游标。
5. readiness 提示只提供偏好，不改变资格。stale、未来时间异常、格式不合法或缺失的信号按 unknown 处理，unknown 不得比已知良好信号获得更高分。
6. 分值不得跨越租户/项目授权边界，也不得把一个项目的仓库或模板身份暴露给其他项目。

## 评分输入与归一化

评分使用 Server 已验证且仍新鲜的 readiness snapshot。沿用现有双端时间校验和 30 秒有效期；不增加未有数据支撑的历史数据库。单次调度决策使用一个固定 snapshot，禁止在评分中读取会变化的外部状态。

建议首版只纳入以下四类输入，所有子分归一化到 0–100 的整数范围：

| 因素 | 子分定义 | 权重 |
| --- | --- | ---: |
| Runtime/Environment warmth | 有效信号中 runtime profile 命中 50 分；Job 要求的 environment name 命中 EnvironmentReady 再加 50 分；无对应信号记 0。它只提供近期预热偏好，不替代 capability/template 硬过滤 | 35% |
| 工作区/仓库亲和 | 有效信号中的 repository ref 命中记 100，未命中或 unknown 记 0；当前信号不含可和 Job 直接比较的 Prepared Workspace fingerprint，因此首版不使用 template fingerprints | 30% |
| 准备成本 | 有效的 workspace prepare P50 在 1–60,000 ms 内按 `floor((60000-ms)*100/60000)` 线性递减；0/缺失/过期记 0，超过范围夹为 0 | 20% |
| 容量余量 | 使用 Server 当前活跃 Attempt 和 Worker Hello slots 计算 `floor((slots-active)*100/slots)`；这是硬过滤后的实时 Server 状态，不依赖可过期的 free-slots hint | 15% |

总分为四个整数子分按权重加权后四舍五入为整数，权重和为 100%。公式、时间窗、区间边界及配置版本作为代码常量/版本化配置固定，不允许任意用户请求传入自定义权重。首版不纳入历史延迟、失败率或 credential availability：前两者尚无稳定且可比的窗口统计，credential availability 是硬约束/敏感授权状态，不能作为偏好分。

> 准备成本 P50 当前是 Worker 的近期汇总样本，并非按 Runtime 或 Repository 分桶；首版只把它作为粗略 Worker 级成本提示，不能据此声称特定仓库/Runtime 的性能收益。未来若要做分桶评分，应先扩展并版本化 signal contract。本评分首版保持实验性、默认关闭。

## 公平性与饥饿保护

软评分可能使固定的高亲和 Worker 长期胜出。为限制这一点，最高分候选只在相同分值组内按 least-active 和轮换决胜；不同分值仍由分值优先。首版不保证跨不同分值的严格公平，因此启用实验模式前必须有双 Worker 负载回归：持续提交相同任务时，记录各 Worker 份额、分值及活跃数。若证据显示单一 Worker 被持续垄断，需先调整评分上限/权重或增加明确的 fairness bound，再扩大启用范围；不能静默改写排序语义。

## 配置、兼容和回退

- 配置名：`prepared_workspace_score_enabled`，默认 `false`；未配置和旧配置保持关闭。YAML 类型无效时配置加载失败，Server 不启动，不能静默改写用户配置。
- 不改变 Server/Worker wire protocol 或数据库 schema；只消费现有 optional readiness signal。
- 关闭开关立即恢复原有 least-active + tie rotation 决策。
- 新版本兼容未上报 readiness 的旧 Worker；其硬资格仍按既有 capability 规则判定，评分信号视为 unknown。
- 评分器异常或产生非法结果时应 fail closed 回退至原排序，并记录有界错误类别；不能拒绝本来合格的任务。

## 决策解释与隐私

沿用现有 scheduler decision evidence，并增加有界字段：是否启用评分、评分版本、每个候选的总分、至多四个因素子分、缺失/过期信号标记、最终排序理由与回退理由。限制候选数和字段长度；不记录提示词、凭据、绝对仓库路径或其他项目的身份数据。解释结果应能回答“为何选中该 Worker”，同时不泄露不相关项目的 readiness 内容。

## 验收标准

- 默认关闭、未配置和非法配置下，选择结果与当前 least-active + tie rotation 规则一致。
- 硬过滤先于评分；能力、凭据、安全、并发或容量不满足者无论分数多高都不能被选中。
- 同样输入在重复运行及候选输入顺序变化时结果一致；成功 Attempt 才推进 tie cursor。
- 高分因素单调性：其他输入相同，环境/模板命中、亲和命中、较低准备成本或更高余量不得降低总分。
- stale/unknown 信号不提供正向加分；未来时间、超限/畸形信号按 unknown 安全处理。
- 多项目解释数据有隔离，字段与候选列表有界，且不包含敏感内容。
- 评分开启时验证评分排序与 tie fairness；评分关闭时通过现有公平调度回归。
- 基准覆盖评分开关关闭/开启的调度 CPU/延迟开销，CI 采用相对回归检查或契约测试，不设置未经基线支撑的绝对耗时门槛。
- 文档说明实验开关、观测指标、回退步骤和不得据合成数据宣称生产收益的限制。

## 风险和待实施计划核实项

- 容量余量由 Server 的 Worker Hello slots 和当前活跃 Attempt 数计算，不使用可能过期的 free-slots 信号。
- prepare/startup P50 只有在样本新鲜且语义一致时可比；对缺失/不同 Runtime 的样本必须按 unknown，不跨 Runtime 混算。
- 评分优先于负载会改变开关开启后的派发结果；先在 CI/fixture 和可控环境验证，生产默认仍关闭。
- 用户显式选择 Worker（若未来提供）属于独立需求，不能由本设计隐式加入。

## 自审结论

规格保持了“硬过滤负责安全与可执行性，确定性软评分只负责合格候选偏好”的边界；默认关闭可保留现有行为。首版输入限定在当前 readiness observer 能提供的信号，并排除历史失败率等尚无稳定统计的数据。需要实施计划在改代码前核实 free-slot 与准备成本的真实字段语义，并以测试固定归一化边界。本文通过自审，等待用户审阅批准。
