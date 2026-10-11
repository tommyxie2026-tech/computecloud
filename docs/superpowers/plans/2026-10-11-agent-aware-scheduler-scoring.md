# Agent-aware Scheduler 确定性软评分实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为合格 Worker 增加默认关闭、确定性、可解释的 readiness 软评分，并把 v0.4.7 stable release 门禁调整为精确候选 SHA 的必需 CI 全绿即可放行，同时如实保留未执行的真机验收状态。

**Architecture:** 调度器继续先运行现有硬过滤。评分仅作用于其输出，并以当前最小活跃数和轮换逻辑作默认/异常回退。发版聚合器改为验证精确候选的 CI 证据；真机报告保留为独立生产验收材料，不再作为发版授权前置条件，release metadata 和文档不得把未运行的真实环境测试标记为通过。

**Tech Stack:** Go、Python 3、unittest、GitHub Actions、Markdown。

**Spec:** `docs/superpowers/specs/2026-10-11-agent-aware-scheduler-scoring-design.md`; release policy decision: CI-green exact-SHA release gate, real-host tests documented as pending/non-claim.

## Global Constraints

- `prepared_workspace_score_enabled` 默认 `false`；未配置时关闭，YAML 类型错误时拒绝启动。
- 硬过滤先于软评分，软评分不能覆盖能力、凭据、安全、并发和资源限制。
- 使用确定性 0–100 整数因素分及 35/30/20/15 权重；评分失败回退原 least-active + tie rotation。
- readiness 沿用双端新鲜度检查和 30 秒有效期；stale/unknown 不得获得正向加分。
- 精确候选 SHA 的必需 CI jobs 全成功是 release authorization 的依据；真机验收状态单独报告为未执行/待验收，不得伪称完成。
- Relay 继续 `direct` 默认；本次 release gate 不改变 Relay 部署默认值。
- 产品保持 Agent-aware Distributed Job Execution Platform / Agent Job Executor。

## Review Focus

- 未配置评分必须完整保持旧排序；非法 YAML 必须拒绝启动。测试：配置解析默认关闭/非法输入拒绝。
- 候选遍历顺序及 Peer 到达顺序不得改变评分平手结果。测试：洗牌输入重复选择一致。
- readiness 有效期边界、不同 Runtime 的 P50 混用。测试：过期、future skew、Runtime 不匹配按 unknown。
- Worker slots 与当前 active 计数决定容量比例。测试：slots/active 边界及容量单调性。
- release CI 绿但真机报告缺失时可授权发版，同时 gate/report/docs 不能声称真机通过。测试：缺失真机报告的成功 gate 与 pending 状态。

---

### Task 1: 固定并测试 CI-only release authorization 契约

**Files:**
- Modify: `scripts/v047_stable_gate.py`
- Test: `scripts/test_v047_stable_gate.py`
- Modify: `scripts/validate_v047_release_transition.py`
- Test: `scripts/test_validate_v047_release_transition.py`

**Interfaces:**
- `validate_ci(candidate_sha, ci)` 仍要求 exact candidate push、completed/success、所有 `REQUIRED_CI_JOBS` 成功及 Relay CI contract 成功。
- `evaluate_gate(candidate_sha, reports, ci)` 变更为 CI-only authorization；reports 可缺席，若提供则验证并标记为 supplemental，不作为 release 条件。
- Gate schema 更新至 v2，含 `release_basis: "EXACT_SHA_CI"`；无真机报告为 `NOT_RUN_OR_PENDING`，有补充报告为 `SUPPLEMENTAL_PRESENT_NOT_GATING`。

- [x] **Step 1: 写失败测试**
  - 缺失 runtime/integration/production/performance reports，但 exact-SHA CI 全绿时 gate 允许 release。
  - CI 有任一失败/缺 job/wrong SHA/wrong event 时仍拒绝。
  - 可选真机报告若提供且有效则只列入 supplemental evidence；缺失报告不得产生真机 PASS。
  - release transition 按新 schema 重算 gate，且不要求四份真机 evidence 文件。
- [x] **Step 2: 运行指定 unittest 确认新用例失败**
  - Run: `python3 -m unittest scripts.test_v047_stable_gate scripts.test_validate_v047_release_transition`
  - Expected: 新的 CI-only 契约测试失败，旧测试结果显示待更新的 schema/evidence 前置约束。
- [x] **Step 3: 实现 v2 gate 与 transition validator**
  - 每份真机报告独立验证并标记 `SUPPLEMENTAL_REPORTED` 或 `SUPPLEMENTAL_REJECTED`；缺失、损坏或失败报告均不阻断由 CI 决定的 release authorization。
  - release transition 必须校验 gate 的 CI source SHA 与候选一致，并验证 evidence hash；只要求 CI evidence 与 gate 文件。
  - 删除固定 release changed-file 集合对真机报告的强制要求，仍限制其他任意代码漂移。
- [x] **Step 4: 更新测试并运行指定 unittest，全部通过**
- [x] **Step 5: 用 `make ci-v047-stable-gate` 和仅含 CI evidence 的临时目录验证可生成 release-allowed gate。**

### Task 2: 更新 CI 工作流和 release 文档口径

**Files:**
- Review: `.github/workflows/ci.yml`
- Modify: `docs/implementation/v0.4.7-stable-tracker.md`
- Modify: `docs/implementation/v0.4-closeout-plan.md`
- Modify: `docs/implementation/p0-p2-execution.md`
- Modify: `README.md` (若包含 release 认证/验收声明)
- Test: `scripts/test_v047_stable_gate.py`, `scripts/test_validate_v047_release_transition.py`

**Interfaces:**
- 必需 CI jobs 名称仍由 `REQUIRED_CI_JOBS` 单一常量定义。
- release gate 结果必须同时显示 release authorization 与真实环境验收独立状态。

- [x] **Step 1: 加测试，验证 gate report 对真实环境只标记 pending/not run，不能因 CI 成功产生真实环境 PASS。**
- [x] **Step 2: 运行上述测试并确认失败。**
- [x] **Step 3: 核实 workflow 发布依赖**：当前 `package` 已等待 `REQUIRED_CI_JOBS` 同一组 jobs，`release` 等待 package 与容器发布；记录因此无需改 workflow。更新文档移除真机报告作为 release 前置输入，明确 CI 只证明自动化契约/模拟范围，不得宣称真机待办已完成。
- [x] **Step 4: 运行 `make ci-v047-stable-gate-contract ci-v047-release-transition-contract` 并通过。**

### Task 3: 校准 readiness 信号语义和固定评分归一化

**Files:**
- Modify: `internal/readiness/signal.go` (仅在现有字段不足时)
- Modify: `internal/server/readiness.go`
- Modify: `docs/implementation/readiness-contract.md`
- Test: `internal/readiness/*_test.go`, `internal/server/*readiness*_test.go`

**Interfaces:**
- 评分器输入必须使用 Server 已验证的新鲜 snapshot；具体结构从现有 `ExecutionSignal` 投影复用。
- 配置字段为 `prepared_workspace_score_enabled bool`，默认 false；YAML 类型错误时拒绝启动。

- [x] **Step 1: 测试并确认字段语义。实现裁定：Job 的 `required_capabilities` 含 `environment:<name>`，使用 RuntimeReady 命中 50 分、Job 所需 environment 命中 EnvironmentReady 再得 50 分；Worker 当前 prepare P50 是粗粒度汇总，首版仅作 Worker 级粗估且默认关闭。容量分由 Server 当前 active/Worker Hello slots 计算，不使用可能过期的 free-slots 提示。**
- [x] **Step 2: 运行针对性 Go tests，确认缺少语义的测试失败。**
- [x] **Step 3: 只补齐评分所需的有限解释投影；不增加 wire protocol 或数据库 schema。**
- [x] **Step 4: 固定并测试 0–100 子分映射、四舍五入和 stale/unknown=0 的确定性边界。**

### Task 4: 实现默认关闭的 deterministic scorer

**Files:**
- Create: `internal/server/worker_scoring.go`
- Modify: `internal/server/worker_selection.go`
- Modify: `internal/server/*scheduler*` 中实际分配路径（以 `rg` 定位）
- Test: `internal/server/worker_scoring_test.go` (new)
- Test: `internal/server/fair_scheduling_v35_test.go`

**Interfaces:**
- 新函数 `scoreWorker(input workerScoreInput) workerScore` 返回四因素分和加权整数总分。
- 选取入口读取 `prepared_workspace_score_enabled`；关闭时调用现有 `chooseLeastLoadedWorker`。纯评分函数无错误返回路径。

- [x] **Step 1: 写失败测试**：默认关闭行为等同旧实现、硬过滤候选不进入 scorer、单因素单调性、确定性、unknown/stale 与错误回退。
- [x] **Step 2: `go test ./internal/server -run 'TestWorkerScore|TestChooseLeastLoaded'` 确认新行为失败。**
- [x] **Step 3: 实现整数评分与固定排序键**：score desc、active asc、server cursor order；候选输入排序变化不影响结果。
- [x] **Step 4: 跑专项测试和公平调度测试通过；仅成功 Attempt 提交后推进 tie cursor。**

### Task 5: 有界解释证据与隐私隔离

**Files:**
- Modify: `internal/server/readiness.go`
- Modify: 当前 scheduler decision evidence 构造/持久化文件（定位 Attempt transaction 写入点）
- Test: `internal/server/*explain*test.go`, `internal/server/*integration_test.go`

**Interfaces:**
- 解释包含 enabled、score version、总分、至多四个子分、unknown/stale 类别和选中原因。
- 解释字段采用现有 evidence 存储；不增加数据库 schema，除非现有 projection 无法兼容表达。

- [x] **Step 1: 测试 evidence 字段界限、跨项目数据隔离、路径/secret 不出现。**
- [x] **Step 2: 确认测试失败。**
- [x] **Step 3: 在 Attempt 创建事务中持久化有界 evidence，评分关闭时保留兼容字段。**
- [x] **Step 4: 跑 readiness contract、explainability 和 integration tests。**

### Task 6: 完整验证和跟踪文档更新

**Files:**
- Modify: `docs/implementation/readiness-status.md`
- Modify: `docs/implementation/parallel-development-plan.md`
- Modify: `docs/implementation/long-term-roadmap.md`
- Modify: `docs/implementation/p0-p2-execution.md`

- [x] **Step 1: 运行** `go test ./internal/readiness ./internal/server`。
- [x] **Step 2: 运行** `make test race vet capacity-check ci-v047-stable-gate-contract ci-v047-release-transition-contract`。
- [x] **Step 3: 检查文档**：CI-only release authorization 与真机验收 pending 两者表述一致；scheduler score 默认为 off、尚无生产收益声明。
- [x] **Step 4: `git diff --check` 并汇总每项测试的真实输出，再提交审阅。**

## 自审

- 覆盖规格中的默认关闭、硬过滤不变、确定性排序、unknown/stale、解释性、隐私、性能、CI/release gate。
- Review Focus: Worker Hello slots 和当前 Server 活跃 Attempt 是容量评分的权威实时输入；过期 free-slots 不参与。
- Ruling: 配置 bool 类型错误时拒绝配置加载和 Server 启动，而不是静默按关闭处理；这避免错误配置掩盖运营问题，代价是格式错误会中断服务启动。
- Ruling: Worker Hello slots 和当前 Server 活跃 Attempt 直接计算容量比例；这些是硬容量选择依赖的权威实时值，避免推测总槽位或使用过期 free-slots。
- Ruling: 当前 prepare P50 不按 Job Runtime/环境分桶；保留为粗粒度 Worker-level cost hint，规格已明确边界，开关默认关闭；成本是忽略分桶精度的风险。
- Ruling: `.github/workflows/ci.yml` 无需改动；`package` 已依赖 CI 必需矩阵，stable release job 依赖 package 和容器发布，因此 tag 发布已受 exact candidate CI 成功约束。
- CI release authorization 和真实环境验收是两个独立状态；文档、gate JSON 与 release transition 测试都要求不夸大验证范围。
- 本计划存在跨多个文件的行为变化，但都属于同一调度与发布 gate 切片；Task 1–2 可独立审查，Task 3–5 实现评分，Task 6 收口验证。
