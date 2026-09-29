# ADR-021：Server–Worker Relay-assisted P2P 传输

- 状态：Accepted（设计冻结，尚未实现）
- 日期：2026-09-30
- 影响范围：Worker 连接、容器部署、网络安全、Control/Remote 路线
- 实施计划：[Relay/P2P 传输实施计划](../implementation/relay-p2p-transport-plan.md)
- 相关决策：[ADR-008：客户端控制面](0008-client-control-plane.md)、[ADR-020：多架构容器镜像](0020-multiarch-container-packaging.md)

## 1. 背景

当前 computecloud 使用 Worker 主动建立 gRPC 双向流，Server 是 Job、Stage、Task、Attempt、租约、Artifact 和审计的唯一事实源。容器部署已经支持 Server IP + 7443/7444，并使用 TLS + Bearer Token。

在 NAT、私网、防火墙或企业网络场景下，Worker 可能无法直接访问 Server。需要一个可选的 Relay 传输层，但不能因此引入第二个调度器、第二套状态事实源或新的重型基础设施。

## 2. 决策

采用 **直连优先、Relay 回退、内层 TLS 端到端** 的 Relay-assisted P2P 模式：

1. Worker 仍然主动连接，不开放 Worker 入站控制端口。
2. 可达时优先建立 Worker → Server 直接 gRPC/TLS 连接。
3. 直连失败时，Server 与 Worker 分别主动连接无状态 Relay，由 Relay 配对并转发不透明字节流。
4. Relay 不参与调度、租约、Attempt、Artifact 状态判断、凭据管理或终态提交。
5. Relay 外层连接使用 TLS；Server 与 Worker 之间保留现有内层 TLS 和 Bearer Token，Relay 不获得 Server Token。
6. Relay 不保存 Job、Task、日志、Prompt、代码、Artifact 或 SQLite 状态。
7. Relay 作为同一 Go 项目的可选运行模式/轻量边缘组件，不演进为独立控制面或微服务体系。

“P2P”在本 ADR 中表示 direct-first 的对等连接；NAT 无法穿透时是 Relay-assisted，而不是强行实现不可靠的 TCP 打洞。

## 3. 信任边界

~~~text
Server ──内层 TLS + Worker Token── Worker
   \                                  /
    └──外层 TLS + 短期配对票据── Relay
~~~

Relay 可以看到连接元数据、时间、方向和流量大小，但不能读取或修改内层业务内容。Relay 不能被视为执行事实源；断开 Relay 只能导致连接不可用或状态待核查，不能自动创建新 Attempt。

## 4. 配对与安全规则

Server 签发短期、单次使用的配对票据，至少绑定：

~~~text
pair_id
server_id
worker_id
connection_nonce
protocol_version
expires_at
relay_id
server_public_key_hash
~~~

Relay 必须拒绝过期、重复、跨 Worker、跨 Server 或降级协议的票据。Relay 认证使用独立的 Relay 凭据；Server/Worker 的业务 Bearer Token 只在内层 TLS 中传输。

Server 证书校验对象是 Server 身份，不是 Relay 地址。通过 IP 访问时证书 SAN 必须包含该 IP；通过 Relay 时仍按 Server 的证书身份校验。

第一阶段不强制应用层 E2EE；因为 Relay 不终止内层 TLS。只有在未来存在不可信 TLS 终止点、跨组织合规或客户端盲中继需求时，才在 ADR-008/ACP-7 范围内增加可选 Blind E2EE。

## 5. 连接和执行语义

连接模式：

~~~text
DIRECT → RELAY → DRAINING → DISCONNECTED
~~~

- Server 通过 `connection_epoch` 只承认一个当前连接；旧连接进入 draining，不能写入当前控制流。
- `worker_epoch`、`connection_epoch` 与 Attempt `generation` 互不替代。
- Relay 不自动重放业务命令；重连时由 Server/Worker 依据持久 command inbox、`command_id` 和 `worker_seq` 恢复。
- 连接断开映射为 `UNKNOWN`/`UNVERIFIABLE` 或既有租约恢复流程，不等价于失败、取消或成功。
- Control、Event、Bulk 逻辑流必须有界并支持优先级，取消和租约续期不能被日志或 Artifact 阻塞。

## 6. 性能取舍

直连是默认最低延迟、最低带宽路径；Relay 只承担无法直连的连接。Relay 不压缩已经加密的数据，不落盘大文件，按连接/Worker 限流并实施背压。第一阶段不引入 WebRTC、完整 ICE 或 QUIC，避免为尚无真实瓶颈证据的场景增加协议和依赖复杂度。

如果真实验收证明 TCP Relay 的队头阻塞或 NAT 成功率不足，再单独提交 QUIC/ICE Contract Change，不在本 ADR 中隐式引入。

## 7. 非目标

- Relay 不成为调度器、消息队列、Artifact 仓库或认证中心。
- 不增加 PostgreSQL、Redis、MQ 或 Relay 专用数据库。
- 不允许手机成为通用 Worker。
- 不因 Relay 引入新的 Job/Task/Attempt 状态机。
- 不把客户端、Relay 或推送系统作为 Server 正确性的依赖。

## 8. 验收门槛

必须通过：

- 直连优先与 Relay 回退；
- 配对票据过期、重放、错配和协议降级拒绝；
- Relay 无法读取业务明文；
- 旧 `connection_epoch` 写入拒绝；
- duplicate delivery、retry、restart、断网恢复；
- 控制流优先和 Bulk 背压；
- Relay 重启不产生重复 Attempt；
- Server/Worker 仍可在无 Relay 时独立运行。

