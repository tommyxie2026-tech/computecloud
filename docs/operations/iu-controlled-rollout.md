# IU 受控上线、恢复与回滚

当前范围：本轮只完成代码/CI 取证。真实 Provider、aicloud 服务、两台独立 Worker 主机验收均 NOT_RUN；所有新图功能默认关闭，不因 CI fixture 成功扩大开关或发布范围。

## 冻结运行输入

记录 computecloud commit/二进制 SHA256、Server schema v26、Worker schema v6、模型/CLI/Runtime/模板/权限/环境摘要、样本来源与业务 verifier。v1～v4 图请求合同、memory-view-v2 的独立 capability 与当前范围见 contracts。IU 仍是现有 Task/Attempt 契约，不新建执行引擎；Registry/Evaluation/Router 属于 aicloud。

实际独立主机清单使用 [模板](../validation/iu-independent-host-inventory.example.json)：

```sh
python3 scripts/iu_host_inventory.py inventory.json
```

只检查声明的 host/Worker 身份是否完整且不同，始终输出 acceptance_status=NOT_RUN，不证明物理独立或模型正确。不同 PID/容器名/端口不替代独立主机；主机身份、网络恢复和环境来源需要实际部署取证。模板无环境信息，不能当作验收通过。

`computecloud templates --config worker.yaml` 返回模板以及实际规范化 Runtime/Policy 摘要，可用于匹配 replay binding；environment_sha256 仍需管理员核实不可变工具/模型部署。配置/模型未固定时不开放结果复用。

## 提交与权限

先保持 graphs_enabled=false。只给独立主体授予所需 jobs:read、goals:invalidate/import/verify/evaluate/propose，发布新代还需要 jobs:submit、jobs:control 与相应 credential；CLI propose-graph 按现有控制协议获取短期写租约、带 X-Control-Lease 提交并释放。模型输出不授予这些权限。原 submitter 不因可提交任务获得自评或记忆授权。

准备目标图时可离线计算规范化摘要：

```sh
computecloud job compile-graph --file target.json --config client.yaml
```

按 v2/v3 合同创建一次性 grant，再用 job propose-graph --id SOURCE --file proposal.json，经现有 Guard 发布。grant 绑定精确目标计划/代，消费后反证使用 IU-2；不把一次性许可当持续租约。当前完整闭包/上下文证明不足时全量重算。

## 故障取证矩阵

| 边界 | CI 必测证据 |
| --- | --- |
| 失效提交/应用/激活/准入/PASS | InvalidationSurvivesRestart、LateAdmissionAndPass、LinearizesWithPass、ProjectionBatches、旧评估 Replan 拒绝；真实 Server 硬重启和 Attempt 不增加 |
| 授权消费/撤权/过期/失效竞争 | ImportRechecksRevokedGrantAtCommit、StaleRevisionAndChangedContext、Managed issuer、完整闭包/幂等/GC pin；原生进程拒绝 revoked grant，无半发布 |
| 结果复用/来源/重启 | ReuseSurvivesRestartWithoutNewAttempt、PinsOriginArtifacts、原生进程重复发布/关闭提交后恢复；REUSED 无 Task/Attempt，原文身份链 inline 保留 |
| record 验证/旧证据 | 逐命题 scope、伪造回执、managed key 撤销、profile 修订、IU-2 失效、Goal proof 引用 CAS；UNKNOWN 不变 PASS |
| Context/token/回源 | 必需证据不可截断、中文/emoji、模型/包装漂移、有界回源及重启限额；未知用量不变零 |
| 升级/回滚 | 真实 v20 表边界→v25、历史 Job 保留、FK/integrity、原始快照未修改、备份恢复后已接受导入/pin 与关闭提交后的恢复；未来 schema 拒绝 |
| 真实网络/主机/模型 | 未运行。需要两台独立 Worker、断网/离线/长任务、实际 Provider 与 aicloud 联合合同；本 CI 的同 Runner 两进程不替代 |

## 升级与回滚

1. 关闭新图提交，等待/停止在飞尝试并确认 cleanup；已接受任务继续恢复/排空。不要直接杀未释放 Attempt 后宣称 drained。
2. 停止 Server，使用既有 `computecloud backup --data-dir DATA --out OUT` 离线备份。目录锁、未释放 Attempt、DB integrity 门槛继续有效；归档包含数据库与 Artifact。保存归档和二进制 hash。
3. 对备份副本演练升级，核对 schema、FK、历史事实、来源文件/pin、授权消费和审计。不支持的 wire/Runtime/capability/schema 明确拒绝。
4. 需要回滚时恢复升级前归档到**新目录**，保留升级后目录供审计；用对应旧二进制核对版本后运行。禁止给升级后 DB 改 user_version 或丢弃表来假装降级。回滚快照之后的新事实需另行处理，不能宣称零数据损失。

Task/Attempt 恢复不保证外部 exactly-once。当前只验证只读/纯计算。workspace patch DAG 需另定义基线 CAS、冲突/测试/合并；外部写入先定义 effect ID、幂等键、outbox/补偿或批准边界，再单独立项，不能由 retry/reuse 推导安全。

## CI 构建与发布边界

`make ci-iu-graph` 保存 SHA、环境、原命令、原始样本/JSONL、原生进程证据；任何必测缺失/SKIP/失败或缺原生导入/复用恢复证据都失败。fixture Provider 调用为 0，80 样本臂的调用数不是实际模型用量。

GitHub Actions workflow_dispatch 的 run_slow=true 运行慢速恢复/长任务/多架构镜像和 package 构建；手动验证强制 release=false，publish 仍仅限原 main/tag 发布路径。包和镜像 gate 通过只证明该 SHA 可构建，不等于目标环境已验收或已获扩大开关的决定。真实验收未完成前保持实验性/default-off。


## v5 的受控接入

v5 追加 Server schema v26；升级前备份 v25 DB 与 Artifact，回滚使用未被新进程打开的原快照，不把改写 user_version 当产品降级。Worker v6/输入 v2 保持，只有 Server 接受新合同后才按现有 capability 调度。

按 [v5 合同](../contracts/iu-graph-v5.md)注册只读 coverage checker，并为独立评估身份配置 jobs:read/goals:evaluate；提案身份还需 jobs:submit/goals:propose 及现有控制租约权限。提交者不能自写 verdict。默认图开关不扩大，真实服务与主机验收仍独立开展。

UNKNOWN、证据文件丢失、profile 漂移、CAS 冲突、需要审批或 Guard 拒绝时停止；禁止手改 DB、重复新建 Goal、改模型或放宽 budget 绕过限制。重启或丢响应后复用原 recipe/proposal ID；Server 的持久发布事实决定是否已经发布，CLI 重启不重置次数。
