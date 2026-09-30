# 移动端 Control 部署指南

- 适用版本：v0.4.6
- 形态：Expo React Native companion control client（iOS / Android / Web）
- 产品边界：移动端只做状态查看、通知、审批和安全控制，不运行 Worker，不执行 Agent Job。
- 当前状态：UI-04a foundation 已通过 CI；移动发布流水线会生成独立的 Control Release。当前流水线资产是 Expo bundle/source，**不是签名 IPA/APK**。

## 1. v0.4.6 移动端产物在哪里

本版 GitHub Release 只发布 Server/Worker Linux 二进制和容器元数据。移动端验证产物保存在发布 CI 的 Actions artifact：

- [v0.4.6 发布 CI #36659335062](https://github.com/tommyxie2026-tech/computecloud/actions/runs/36659335062)
- artifact：control-mobile-check-36659335062
- artifact API 下载：[下载地址](https://api.github.com/repos/tommyxie2026-tech/computecloud/actions/artifacts/11073671977)
- artifact digest：sha256:33a8dab63c6264974121734bdfdfbbd3af4345e21619d5a152535ad02711ec0c

artifact 内的 dist-ios/ 和 dist-android/ 是经过 typecheck、Expo config 和 export 校验的 JavaScript/native bundle；它们不是 Apple notarized IPA 或 Android signed APK/AAB。artifact 的保留期由 GitHub Actions 设置，不能作为长期制品仓库。正式 bundle/source 发布应使用独立的 Control Release。

## 2. 发布 Control 移动端 bundle

在仓库根目录手动运行：

~~~sh
gh workflow run mobile-release.yml -f version=0.4.6 -f publish_release=true
~~~

流水线通过 typecheck、iOS/Android Expo export 和 mobile security contract 后，会生成：

- GitHub Actions artifact：computecloud-control-mobile-0.4.6；
- GitHub Release：control-v0.4.6；
- 资产：computecloud-control-mobile_0.4.6.tar.gz、SHA256SUMS；
- 包内：iOS/Android bundle、源码、app/eas 配置、RELEASE-MANIFEST.json、本文档。

control-v0.4.6 是不可覆盖的 bundle/source Release。它不表示 App Store/Google Play 已上架，也不包含签名 IPA/APK/AAB。若 Release 已存在，流水线会拒绝覆盖。

## 3. 使用源码启动开发版

需要 Node.js 22.14+ 和 npm：

~~~sh
cd clients/control
npm ci
npm run typecheck
npx expo start
~~~

使用 Web 控制端：

~~~sh
npm run export:web
npx serve dist
~~~

生产访问必须使用 Server 的 HTTPS 7444，例如 https://10.20.0.10:7444。移动端不要把 Server 暴露为明文 HTTP；证书必须由设备信任的 CA 签发，或在受控测试设备上安装测试 CA。

## 4. iOS / Android 本地预览

Expo bundle 校验通过后，可在有原生工具链的开发机生成本地工程并运行：

~~~sh
cd clients/control
npx expo prebuild
npx expo run:ios
npx expo run:android
~~~

prebuild 会生成本地原生工程；生成物是开发/测试用途。正式分发仍需组织自己的 Apple Team、Android signing key、隐私清单、审核和签名流程。v0.4.6 不承诺 App Store/Play 商店包。

## 5. 连接与凭据安全

移动端连接 URL 只允许传递服务地址：

~~~text
computecloud://connect?server=https%3A%2F%2F10.20.0.10%3A7444
~~~

禁止在深链 query 中放置 token 或 authorization。首次连接后：

- Server URL 必须是 https://（仅本机受控开发可用回环 HTTP）；
- Token 通过受保护的配对/人工输入进入设备，存储在 iOS Keychain / Android Keystore（Expo SecureStore）；
- device ID 与连接 profile 写入 SecureStore，不写入 AsyncStorage、localStorage 或日志；
- Push、离线缓存和 Relay 尚未作为 v0.4.6 的移动生产能力；
- Token、提示词、代码和审批正文不放入深链、二维码、普通日志或推送 payload。

Server 侧仍需按 v0.4.6 部署指南配置 IP + 7444 HTTPS、TLS CA 和最小权限 Token。移动端使用的 Token 至少需要对应的 Control read/approve scope；不要复用 Worker Token。

## 6. CI 验收

在仓库根目录执行：

~~~sh
make ci-control-mobile-check
~~~

Gate 会检查 npm lock、TypeScript、Expo iOS/Android export、bundle identity、computecloud: 深链、SecureStore 持久化，以及禁止把凭据放入 URL/浏览器存储。该 Gate 不调用真实模型、不发布签名安装包。

## 7. 从 foundation 到正式移动发布

正式 IPA/APK/AAB 发布前还缺：

1. 固定移动版本与 changelog；
2. Apple/Android 签名和 CI secret 管理；
3. EAS 或原生 Xcode/Gradle 可复现构建；
4. 安装包 SHA-256、SBOM/签名、灰度和回滚；
5. 真机 HTTPS/CA、深链、SecureStore、失联恢复验收。

这些属于后续移动发布工作，不应把 v0.4.6 的 CI bundle 宣称为正式移动安装包。
