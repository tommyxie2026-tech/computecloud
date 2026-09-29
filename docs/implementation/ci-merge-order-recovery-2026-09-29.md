# CI 合并顺序恢复记录（2026-09-29）

- 项目：computecloud
- 范围：2026-09-28 ～ 2026-09-29 main / release 合并窗口
- 当前结论：v0.4.5 release CI 已稳定；后续按顺序合入 ACP-4b Resume、multi-arch packaging、ACP-5 Gemini。当前 main@602bb4d080753b1c43f32ada88521d67222492a2 的 workflow 36591386173 全功能 Gate、verify、package、container-image、container-publish 全部 PASS。

## 1. 当前最终状态

最终稳定合并顺序：

~~~text
PR #51  WS-E Approval ACK integration
  -> 1d8513a2bea9ff4f15af871ec8edfd9a22076ca2
PR #54  release/v0.4.5
  -> 607d1d417b3bf0fc9da5c0d5faef42b16026d045
~~~

最终证据：

- run 36574965332：PR #51 合并后 main PASS；
- run 36575512360：v0.4.5 release merge 后 main PASS；
- prepared-workspace-contract / prepared-workspace-recovery / workspace-flow PASS；
- agent-control-approval / dispatch / fencing / negative / read / schema PASS；
- Runtime / Tool / Environment / Retry / Artifact / Long-running / Fair / Task Gate 全部 PASS；
- package PASS；
- release PASS。

因此当前不存在仍未解决的 main CI 红灯。

## 2. 第一段失败窗口：Prepared Workspace 初始集成

### 2.1 run 889 — Prepared template 权限 / 清理失败

run 36496036923 的 verify 失败，核心错误：

~~~text
rename .../prepared/.prepare-.../template .../prepared/<fingerprint>: permission denied
TempDir RemoveAll cleanup: unlinkat .../.git/objects/...: permission denied
~~~

根因：Prepared Workspace 冻结模板内容时把目录权限收紧得过早，导致 atomic rename 和测试/GC 删除都失败。

修复链：

- 09ec9426e9df59859bafd38012778c4cb61ecb00 — keep prepared workspace directories removable
- 6d164f8ad45cd842a8741c62e8593f4833126f3b — 增加 cache removable 回归测试
- 0ec2b5e91ea7e353f1227eac63c0c8785f3ee5d3 — keep prepared workspace cache removable

固定原则：Prepared template 内容可以 immutable，但 cache/root 必须保留可遍历和可回收语义。

### 2.2 runs 890–898 — Worker 编译接口漂移

代表 run 36496167700：

~~~text
internal/worker/workspace_lifecycle.go:33:13:
assignment mismatch: 1 variable but requiredEnvironment returns 2 values
~~~

这是共享 helper contract 改动造成的全矩阵编译失败，不是 18 个独立故障。

修复：

- 32e3f269deb47b71b7b314c1b30b0cbcc49ea11e — handle prepared template environment resolution

固定原则：跨 Workstream helper signature 改动必须同步所有消费者并先跑 go vet ./... 和 go test ./...，再合并专用 Gate。

### 2.3 runs 899–900 — 只读模板污染共享生命周期测试

代表 run 36496434574，workspace / runtime / environment / read / verify 等 Gate 同时失败，仍指向只读 Git object 无法删除。

这证明：Prepared Workspace 专用 Gate 通过，不代表共享 Workspace 生命周期可安全合并。最终由 2.1 的 removable 修复链解决，并由全量 CI 验证。

## 3. 第二段失败窗口：Runtime Version Fencing

失败 runs：36567952264 ～ 36568098476（949–962）。

共同失败 Gate：

- verify
- workspace-flow
- prepared-workspace-contract
- prepared-workspace-recovery

代表错误：

~~~text
workspace template runtime/version is unavailable
~~~

### 3.1 新 fail-closed 语义本身正确

Prepared Workspace fingerprint 必须绑定：

~~~text
runtime profile
runtime version
tool versions
environment version
repository/base
policy/template inputs
~~~

相关实现：

- 575e6e0ab90f183c1d12a81e1f82ee14908dd805 — execution component version identity
- d71c330ba4aa54b9458008b993741c3799362103 — fencing tests
- de1146fe05f0720aba7cd2f831797bd68ba0eb40 — CI coverage
- 11e34b9b0e6ea780a61bbd658e6be90209ed2b73 — missing runtime version fail closed
- bb915f76f7a51e09ed486e7a05f2e626d050b197 — negative test
- 79c1eec52605115acb087d29db0734921a615893 — Gate requirement
- 8a2a1fcadb73cca36c33459db6a43c52c74c9d1f / 78d1293d9614e9c67b6c4e5fb61740adbf149b54 — concurrent creation idempotency

### 3.2 真正遗漏的是旧 fixture

旧 Worker lifecycle fixture 没有配置 canonical Runtime profile/version，所以被新 contract 正确拒绝。

修复：

- 50cac0ebcbaf1f81f82b7f11ed28f77dc759f0b8
- 81991e75589ea074c5ecd88d8cddec837659cb8a
- 07f281d1ec2eee8d8e6aea023d58e381bbe8d7b6

随后进入 release integration：

- b3657b7f23268eb7c7faa251e15c930c1c53a91b
- 3f7da952adf30f3d2967081aebc08ab4a2aa7c41
- ed217dcdf54c6a2ad6363b74222fb55ecc5fa754

### 3.3 后续强制合并顺序

~~~text
1. 新 contract/schema
2. 正向/负向 unit test
3. canonical fixture/helper 同步
4. 所有旧 lifecycle/integration fixture 同步
5. 专用 Gate PASS
6. verify 全量 PASS
7. package PASS
8. release branch merge
9. main release Gate PASS
~~~

禁止 contract 先合并、fixture 稍后补。Fixture 是公共契约消费者，不是测试附属品。

## 4. Approval 与 Prepared Workspace 的合并关系

WS-E Approval 专用 Gate 曾通过，但共享 Prepared Workspace Gate 红，因此仍应视为 BLOCKED。

最终：

- PR #51 merge：1d8513a2bea9ff4f15af871ec8edfd9a22076ca2
- main CI 36574965332 PASS

固定原则：Workstream 可以并行编码，但 Release Truth 只有一个；任何共享 Gate 红灯都必须阻止 release。

## 5. v0.4.5 Release Gate

PR #54：release: v0.4.5 Prepared Workspace / Safe Control

merge：607d1d417b3bf0fc9da5c0d5faef42b16026d045

main workflow：36575512360

最终 23 个 Gate 全部 PASS。

仓库级发布证据：

~~~text
Prepared Workspace
+ version fencing
+ concurrency/idempotency
+ recovery
+ ACP-3 Safe Control
+ ACP-4a Approval
+ ACK idempotency/generation fencing
+ package
+ release
= PASS
~~~

该证据不替代 Production Baseline 的真实 Codex/Claude、多机、网络故障和 24h+ 测试。

## 6. 合并纪律

1. 任意共享 Gate 红灯，状态就是 BLOCKED，不能用“无关失败”跳过。
2. Contract change 必须在同一合并序列同步 canonical fixture、lifecycle fixture、migration test 和 negative test。
3. Release branch 只有在专用 Gate、verify、package、release 全部 PASS 后才能合并。
4. 文档必须以 main/PR/CI 事实为准，不保留已关闭 PR 或已解决 blocker 的旧状态。

## 7. 当前待办

CI 当前无红灯。剩余补齐项：

1. roadmap 稳定基线更新到 v0.4.5；
2. 清理 ACP-3b 待 CI 等过时描述；
3. ACP-4b PR #50 已关闭未合并，不能继续作为在途实现；
4. 下一 ACP-4b Resume 从 v0.4.5 main 重新建立实现基线；
5. Resume compatibility 必须复用 Prepared Workspace 的 Runtime/Tool/Environment fingerprint，不建立第二套兼容判断。


## 8. 第三段恢复：ACP-4b / Packaging / ACP-5（2026-09-29 ～ 2026-09-30）

### 8.1 ACP-4b Resume

旧 `feature/agent-control-acp4b-resume` 分支和 PR #50/#56 保留了失败 CI，但均已关闭且未合并，不能继续作为主线状态依据。

最终 main 实现：

- `ca2264e9d026b6cd2c2c002e41191041f533d185` — complete ACP-4b session resume on v0.4.5；
- `agent-control-resume` Gate 已进入 main 并持续 PASS。

结论：历史失败属于被 supersede 的旧实现，不应回头逐 commit 修复。

### 8.2 Multi-arch Packaging

唯一开放前置 PR #65 先于 ACP-5 合并：

- PR #65 -> `43bc4c1d23f6cb94ce1c42bc1f52e5687ec1b8ed`；
- PR-head CI PASS；
- 合并后 main CI `36590126802` PASS。

这样保证 ACP-5 的最终 merge-result CI 在最新 packaging main 上验证。

### 8.3 ACP-5 Gemini stale branch

原 `feature/acp5-gemini-runtime`：

- 只领先 1 个功能提交；
- 落后 main 44 个提交；
- run `36572067673` 出现 21 个 Gate 同时失败。

这不是 21 个独立缺陷，而是旧基线导致的 contract/fixture/compile 漂移。

恢复策略：

1. 不 hard-rebase 旧分支；
2. 从最新 main 新建 `feature/acp5-gemini-runtime-refresh`；
3. 只移植 Gemini 功能语义；
4. 同时修正旧提交遗漏的 Job JSON Schema provider-neutral contract；
5. PR #66 merge-result CI 全绿后再合并。

最终：

- PR #66 -> `602bb4d080753b1c43f32ada88521d67222492a2`；
- main CI `36591386173`：
  - all runtime/control/workspace gates PASS；
  - verify PASS；
  - package PASS；
  - container-image PASS；
  - container-publish PASS；
  - release 在普通 main push 按设计 skipped。

### 8.4 当前失败分类规则

后续看到 GitHub Actions 红灯时按以下顺序判断：

~~~text
1. 是否为当前 main / 当前开放 PR？
   no -> 判断是否 superseded，不修历史分支
   yes
    ↓
2. 是否为 merge-result CI？
   no -> 先同步最新 main
   yes
    ↓
3. 是否为共享 contract 变更？
   yes -> 同步 fixture / migration / lifecycle consumer
   no
    ↓
4. 修复单一真实 Gate
    ↓
5. verify
    ↓
6. package / container
    ↓
7. merge
    ↓
8. main CI
~~~

截至 `36591386173`，当前 main 无未解决 CI 红灯。
