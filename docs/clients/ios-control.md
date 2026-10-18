# iOS Control 首版

## 范围

iOS 复用 Expo Control 页面和 Server Control API，提供任务提交、状态与 Worker 观察、产物审阅、取消及有限重试。Server 仍是唯一事实源；手机不承担 Worker 执行。

本切片采用手动配置 HTTPS Server 地址与既有原生任务 API key。Web 的 `admin/admin` 登录和 Cookie 会话不作为移动端模型 key 或原生任务 key。二维码配对、设备注册撤销、Push、应用商店分发属于后续交付。

## 连接

1. 在 Server 配置已有身份及最小任务权限，签发该身份可用的原生任务 API key。不要使用 Provider key，也不要向手机复制管理凭据。
2. 使用受信任的 HTTPS 入口，例如 `https://control.example.com`。地址不包含 `/ui`、`/agent`、用户名、查询参数或片段。本机回环调试允许 HTTP，局域网明文连接拒绝。
3. 打开客户端，填写地址与 key，点击“连接”。验证成功后才将连接配置写入本设备 Keychain；密钥输入框使用遮罩。
4. 自签证书需要先由设备信任。客户端不提供跳过证书验证开关。
5. 点击“切换 Server / 退出连接”清除原连接。新地址必须重新填写 key；Deep Link 只接收地址，不能携带 token。

存储使用 `expo-secure-store` 的 `AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY`。重新启动时读取已保存配置，仍需点击连接验证。退出仅清除本机配置，不等同于在 Server 撤销 API key。

## 恢复与边界

- 进入非活动状态时卸载任务视图并终止事件观察；返回前台重新建立连接并读取所选任务。
- 同一连接的提交草稿和幂等标识保留在内存中；切换连接时清除。进程被系统终止后不恢复草稿。
- 后台、断网不排队提交、审批、取消或重试；已被 Server 接受的任务继续执行，操作结果以重新同步后的 Server 状态为准。
- 当前凭据收到 401 后使 API 实例失效，停止其后续请求并清除保存的凭据。旧会话迟到的响应不能清除新连接。
- 原生 Bearer 请求拒绝 HTTP 重定向，避免把凭据发送到另一入口。请直接配置最终 HTTPS 地址。
- API key 仍是持有者凭据；本切片不是独立设备授权或 QR pairing。设备遗失时应在 Server 撤销相应 key。

## 开发与验证

使用支持当前 React Native 的 Node 版本（例如 Node 24）。

```sh
cd clients/control
npm ci --ignore-scripts
npm run typecheck
npm run test:mobile
npm run test:web:api
npx expo export --platform ios --output-dir dist-ios
```

`make ci-control-mobile-check` 将执行移动安全检查、类型检查、移动连接测试及 iOS/Android JS bundle 导出。JS bundle 导出不等于 Xcode 原生编译或真机验收。

组件状态测试模拟 AppState、Keychain 和网络，覆盖地址切换、迟到 bootstrap/401、前后台切换与重新连接；不冒充真实 Keychain 或模拟器端到端测试。

原生验收还需验证：受信任 HTTPS、自签证书信任、真实 Keychain 重启读取、前后台 SSE 恢复、窄屏与安全区、提交取消闭环。结果必须分别记录，不以 CI bundle 检查替代真机结果。

## 本切片验收状态

- 已通过：TypeScript、地址/Deep Link/配置校验、连接切换和存储竞态测试、真实 React StrictMode 重放、API 失效与卸载取消测试。
- 已通过：iOS/Android JS bundle 与 Web 导出；共享 Web 的隔离 Server、fixture Worker、真实 Chromium 主流程及密码轮换验收。
- 已通过：临时 iOS 原生工程生成。
- 阻塞：CocoaPods 下载 React Native 原生预编译包时连接被对端重置，原生依赖安装未完成；Xcode 编译尚未开始。
- 待完成：重试原生依赖安装、Xcode 原生编译、模拟器与真机任务闭环。不得据此宣称 iOS Beta 已完成。

测试不调用付费 Provider；公共文档不包含实际部署地址、密钥或环境证据。
