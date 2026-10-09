# Relay-assisted P2P 传输实施计划

- 对齐 ADR：[ADR-021：Server–Worker Relay-assisted P2P 传输](../adr/0021-relay-assisted-p2p-transport.md)
- 状态：v0.4.x Experimental foundation；RLY-1 已实现于传输分支，RLY-2～5 仍待实现/验收
- 产品边界：Agent-aware Distributed Job Execution Platform / Agent Job Executor

## 0. Version Placement

本特性分阶段进入长期路线图：

- **v0.4.x**：交付 Relay foundation / experimental transport，包括 Transport seam、无状态 Relay fixture、短期配对、直连优先回退、连接 fencing、背压、恢复测试与独立 CI Gate；`direct` 仍为默认模式。
- **v0.5.x**：只做 Agent-aware Scheduler 与移动端对 Relay 观测能力的兼容验证，不把 Relay 变成调度事实源，也不把手机客户端变成通用 Worker。
- **v0.6.x**：交付设备身份、OIDC/RBAC、审计、企业 Lease，以及基于证据决定是否需要 Blind E2EE Relay；这些不属于 v0.4.x foundation。

v0.4.x 的 Relay 仍是实验性、显式 opt-in 能力。没有真实双机/NAT/断网/长任务验证之前，不改变生产默认路径，不在版本号中暗示公网 Relay 已经生产认证。

## 当前进度

- RLY-0 设计、ADR-021、路线图归属：已完成并进入 `main`。
- RLY-1 Transport seam：PR #102 已合入；直接路径保持原 gRPC dial 行为，替代路径强制内层 TLS。
- RLY-2：PR #103 提供实验性配对/转发 fixture；RLY-3 提供 opt-in 连接器及本地双 Worker 重启恢复测试。RLY-4 已增加真实 Relay、内层 TLS 与 HTTP/2 Bulk 背压下的 control progress Gate；RLY-5 的固定字段指标与运维手册正在收尾。真实 NAT/故障/长任务验收仍待完成。
- Schema：无 SQLite migration；Relay 不保存 Job/Task/Attempt，不成为第二事实源。

## 1. Current State

- Worker 主动建立 gRPC 双向流，Server 维护唯一执行事实。
- 直连使用 TLS 1.2+ 与独立 Worker Bearer Token；容器部署使用 Server `7443`/`7444`。
- 已有 `worker_epoch`、`connection_epoch`、持久 command/result、`worker_seq` 和 generation fencing 语义。
- 当前没有 Relay listener、候选地址交换或 Relay 配对票据。
- 当前不需要 SQLite migration；Relay 设计阶段不增加持久执行状态。

## 2. Target State

~~~text
direct gRPC/TLS
       ↓ failed/unreachable
short-lived pairing ticket
       ↓
stateless Relay byte forwarding
       ↓
inner Server↔Worker TLS + existing bearer identity
~~~

直连和 Relay 共用同一 Runtime/Worker 协议、命令幂等和恢复语义。Relay 只改变网络路径，不改变 Job、Task、Attempt、Artifact 或调度结果。

## 3. Scope

### In scope

- Transport dialer/listener abstraction；
- direct-first / relay-fallback state machine；
- Relay registration、pairing、expiry、quota 和 cleanup；
- 内层 TLS 端到端验证；
- connection epoch fencing；
- Control/Event/Bulk 背压与优先级；
- restart、duplicate delivery、stale generation、断网和 Relay 故障测试；
- 运行指标和部署运维文档。

### Out of scope

- 调度算法、Worker capability 语义或 SQLite Job schema；
- Relay 侧任务状态、Artifact 缓存或消息队列；
- WebRTC、完整 ICE、QUIC 和移动端通用 Worker；
- OIDC/RBAC、企业设备治理和 Blind E2EE 的完整交付；这些仍归 ACP-7/C4。

## 4. Contract Changes

第一阶段应优先使用现有 gRPC wire contract，不修改 Job/Task/Attempt Proto。必要的传输元数据只在连接建立和观测层增加：

~~~text
TransportMode = direct | relay
ConnectionInfo = connection_id, connection_epoch, mode, relay_id, rtt_ms
PairTicket = signed, single-use, short-lived, server/worker-bound
~~~

若必须修改公共 Proto，先提交独立 Contract Change PR，说明兼容性和受影响 Workstream；不能夹带在 Relay Feature PR 中。

配置字段建议为：

~~~yaml
transport:
  mode: direct_then_relay
  relay_address: relay.example.com:7445
  issuer_url: https://relay.example.com:7446
  token_file: /etc/computecloud/relay-issuer.token
  ca_file: /etc/computecloud/relay-ca.pem
~~~

此配置已支持实验性显式启用；Server 和 Worker 分别使用自己的 issuer token，Worker 可设置 `direct_timeout_ms`。真实双机、NAT、长任务与流量优先级尚未验收，不能作为生产默认配置。

实现后配置兼容约束为：`mode: direct` 保持现有行为；`mode: direct_then_relay` 才启用 v0.4.x 实验性回退。未知模式、缺少 relay 地址或票据校验失败必须 fail closed，不得隐式降级或隐藏重试。

## 5. Schema Changes

首阶段 **无 SQLite migration**。Relay 不持久化 Job/Task/Attempt，也不成为第二事实源。

如果后续必须记录 Relay 观测数据，优先写入 Server 现有 connection/telemetry 结构；新增表或字段必须由 schema owner 单独分配版本、迁移和回滚测试。

## 6. Implementation Steps

### RLY-1 Transport seam

- 将当前 `rpcutil.Dial` 和 Server listener 接入可替换 Transport seam；
- 保证 direct 模式行为和现有 TLS/Token 测试完全不变；
- 不引入 Relay 默认依赖。

### RLY-2 Stateless Relay

- 增加轻量 `computecloud relay` 运行模式或同仓库 Relay 组件；
- 支持双端 outbound registration、pair_id、一次性票据、过期和连接清理；
- 只转发内层 TLS 字节，不解密、不落盘、不重放业务帧；
- 增加每连接、每 Worker、全局容量和速率限制。

### RLY-3 Direct-first fallback

- Worker 先尝试 Server 直连，超时后进入 Relay；
- 连接恢复后具备受控 direct re-probe；
- 使用 `connection_epoch` 排除旧连接写入；
- 不自动重放 StartAttempt，重放继续依赖持久 command inbox。

### RLY-4 Flow control

- Control/Lease/Cancel 高优先级；
- Event 有界批量；
- Artifact/Input 使用 Bulk 流和分片；
- 流量超限时 fail closed，不丢弃控制确认，不无限增长内存。

### RLY-5 Observability and operations

记录不含业务正文的指标：

~~~text
transport_connection_total{mode,result}
transport_fallback_total{reason}
transport_rtt_ms{mode}
relay_active_connections
relay_bytes_in/out
relay_rejected_pair_total{reason}
relay_backpressure_total
~~~

### v0.4.x Release Gate

v0.4.x 实现按 RLY-1 至 RLY-5 分小版本合入；每一步都必须保持现有 Runtime/Tool/Environment contract 和 direct path 不变。最终 release gate 为：

- `direct` 回归与现有完整 CI 通过；
- `relay-contract`、`relay-security-negative`、`relay-direct-fallback`、`relay-restart-recovery` 可独立执行并纳入 main CI；
- Server/Worker/Relay restart、duplicate delivery、stale generation 和 connection epoch fencing 有自动化证据；
- 无 SQLite migration、无 Relay 业务事实源、无真实 Codex/Claude 额度依赖；
- 双机/NAT/断网/长任务手动验收记录完成，且具备显式回退开关和运维文档。

## 7. Tests

### Unit

- ticket signature/expiry/nonce/worker binding；
- pair state machine；
- direct-first fallback；
- old connection epoch rejection；
- byte-forwarding frame limits；
- quota/backpressure。

### Integration

- Server/Worker direct path；
- Server/Worker through Relay；
- direct failure → Relay fallback；
- Relay restart → reconnect；
- Server restart / Worker restart；
- duplicate delivery and retry；
- stale generation result rejection；
- artifact hash and event waterline preservation。

### Security negative tests

- expired/replayed/mismatched ticket；
- wrong relay or protocol downgrade；
- invalid inner TLS CA/SAN；
- Relay cannot submit or complete a Job；
- Relay cannot read plaintext or bearer token；
- oversize frame, connection flood and quota exhaustion；
- old `connection_epoch` mutation rejected。

## 8. CI Gates

新增独立 Gate，不能依赖真实 Codex/Claude：

~~~text
relay-contract
relay-security-negative
relay-direct-fallback
relay-restart-recovery
~~~

PR 默认运行 Contract、Security Negative 和 fixture Relay；真实多主机/NAT/长时间吞吐列为手动或 main/nightly Gate。发布依赖不得因为 Relay 可选能力而绕过既有完整矩阵。

## 9. Rollout

1. 默认 `direct`，先合入 Transport seam 和测试。
2. `direct_then_relay` 只在显式配置下启用。
3. 记录直连成功率、回退率、RTT、Relay 重连和内存峰值。
4. 真实双机、NAT、断网和长任务验收通过后，才考虑默认开启回退。
5. QUIC/ICE 和 Blind E2EE 必须基于实测瓶颈另行 ADR/Contract Change。

## 10. Definition of Done

~~~text
无 schema 事实源分裂
直连回归通过
Relay 安全负向测试通过
重启/重连/重复投递通过
generation fencing 保持
控制流不被 Bulk 阻塞
main CI 与手动 Gate 可定位
部署和回退文档完成
~~~


## RLY-1 validation boundary

`Dial` keeps existing gRPC resolution, proxy, TLS and token behavior. Only explicit
`DialWithConnector` replaces byte transport; certificate verification and bearer
identity stay inside gRPC against the original Server authority. `ServeTunnel`
reuses the existing listener/Server lifecycle but rejects plaintext configuration.
TLS/token negative tests exercise both default and substituted byte paths.

No Relay address/config mode is enabled by this seam. Pairing tickets, forwarding,
fallback, quotas/backpressure and real NAT/fault/recovery acceptance remain RLY-2–5.
The seam must not be presented as a working or production-certified Relay.
