# UI-04a C3 Mobile Foundation

- 项目：computecloud
- 日期：2026-09-30
- 状态：实施中
- 前置：UI-03a/UI-03b C2 Operate
- 对齐：[Agent Control Protocol 实施计划](agent-control-protocol-plan.md)

## 1. 目标

UI-04a 把已经共享 Expo/React Native 的 Control 客户端从 Web-first 提升为可正式构建的 iOS/Android 基线。

本切片只完成：

- iOS / Android application identity；
- custom URL scheme / deep link；
- 原生端 SecureStore connection profile；
- 稳定 device_id；
- 原生恢复最近一次 Server/profile；
- Web 继续 memory-only；
- Deep Link 安全约束；
- mobile contract CI。

本切片不实现：

- QR camera scanner；
- Server pairing code；
- APNs / FCM push；
- remote notification action；
- Enterprise Device RBAC；
- Relay / E2EE。

这些进入 UI-04b/UI-04c。

## 2. Security Boundary

长期 Bearer token：

~~~text
Web
 -> memory only
 -> refresh/page close 后消失

iOS / Android
 -> OS-backed SecureStore
 -> AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY
 -> 不进入 AsyncStorage/localStorage
~~~

Deep Link：

~~~text
computecloud://connect?server=https%3A%2F%2Fcontrol.example.com
~~~

允许：

- server endpoint；
- 后续一次性 pairing id/code。

禁止：

- token；
- Authorization；
- write lease；
- credential；
- prompt/code payload。

任何包含 `token` 或 `authorization` query 的 connect URL 都 fail closed。

## 3. Device Identity

UI-04a 的 device_id 是客户端本地稳定标识，用于：

- single-writer holder identity；
- 后续 QR pairing；
- 后续 Push registration；
- 后续设备撤销映射。

它当前不是认证凭据，也不能替代 Bearer token。

正式 server-side Device Identity / revocation 属于 UI-04b + ACP-7。

## 4. Platform Contract

~~~text
shared React Native UI
        |
        +-- web: token memory-only
        |
        +-- iOS: SecureStore + computecloud://
        |
        +-- Android: SecureStore + computecloud://
~~~

客户端继续只使用 Agent Control API，不新增 mobile-only execution API。

## 5. CI

新增 `control-mobile-check`：

- Expo config 必须声明 iOS/Android identity；
- custom scheme = computecloud；
- SecureStore dependency 存在；
- 禁止 AsyncStorage/localStorage 保存 token；
- connect deep link 不允许 token/authorization；
- TypeScript / Web export 继续 PASS。

## 6. 下一切片

UI-04b：

1. Server durable pairing request；
2. QR / pairing URI；
3. device public identity registration；
4. revoke/list devices；
5. pairing code 短 TTL + one-time use；
6. 不把用户长期 Bearer token编码进 QR。

UI-04c：

- APNs/FCM registration；
- opaque event notification；
- deep link 回 App 后重新认证拉取正文；
- Push payload 不含 prompt/code/approval 内容。
