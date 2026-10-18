# Control Web 部署与认证

Control Web 是 Agent Job Executor 的任务控制台，与 Server 镜像一起发布。客户端按 Web、iOS、Android 顺序交付；当前本文覆盖 Web。

## 启用

复用现有 Server HTTP/HTTPS 监听，不需要新开端口。在 Server 配置增加：

```yaml
server:
  web:
    enabled: true
    auth:
      username: admin
      password_file: /run/secrets/web_admin_password
      identity_owner: operator
      session_ttl_seconds: 28800
  users:
    - owner: operator
      token_file: /run/secrets/operator_api_token
      projects: [demo]
      credentials: [agent-account]
      scopes: [jobs:read, jobs:submit, jobs:control, jobs:cancel, jobs:retry]
```

`identity_owner` 必须唯一匹配 `server.users` 中一个身份的 `owner`。Web 操作继承该身份的项目、凭据和 scope，不因为用户名为 admin 获得额外权限。任务写操作还受 `jobs:submit`、`jobs:control`、`jobs:cancel`、`jobs:retry` 等权限和控制租约限制。身份仍保留既有 `token_file` 配置，原 CLI/API 使用方式继续有效。

使用完整的 Server 镜像启动后，浏览器访问 `https://control.example/ui/`。页面自动连接同域 Server。Web 不需要配置浏览器 API Key，也不需要独立 Node 服务。

## 账号和改密

未设置账号和密码时为 `admin/admin`。公网部署前必须修改默认密码，启动日志会提示默认密码仍在使用。

可在 `auth.password` 直接配置密码，或使用推荐的 `auth.password_file`。两者互斥，不能同时配置。密码文件只去除末尾换行，不去除密码中的空格。空文件或读取失败会使启动失败，不会回退默认密码。

密码文件应只读挂载到容器，确保 Server 运行用户可读取，其他非授权用户不可读；不要写入镜像、仓库、命令行参数或截图。修改密码文件或账号配置后重新启动 Server，所有旧 Web 会话随之失效。

Web 默认关闭。关闭 `server.web.enabled` 并重启可撤销 Web 入口，不影响原有 CLI/Bearer 身份的配置。

## TLS 与反向代理

公网使用 HTTPS 并正常验证证书。自签 CA 应由客户端明确配置可信根，不关闭证书验证。

Nginx 等代理需把 `/ui/`、`/v1/web/auth/`、Control `/v1/` API 转发到同一个 Server，保留原始 Host；SSE 路由关闭响应缓冲并配置足够的读取超时。如果 HTTPS 在代理终止、代理到 Server 使用明文 loopback HTTP，必须配置以下外部来源，即使代理保留原始 Host 也需要；外部域名与内部 Host 不一致时同样需要：

```yaml
server:
  web:
    external_origin: https://control.example
```

`external_origin` 只接受 HTTPS origin，不包含路径、用户名或查询串。Server 不依据任意转发头判断来源或降低 Cookie 安全属性。即使代理到 Server 使用内部 HTTP，公网 Cookie 仍保持 Secure。明文开发模式只沿用明确配置的 loopback 监听。

## 登录与任务操作

登录成功使用 HttpOnly、SameSite=Strict 会话 Cookie；HTTPS 下带 Secure。浏览器不保存密码或长期 API Key。会话默认 8 小时，允许设置 300 至 86400 秒；退出、到期或 Server 重启后重新登录。

登录失败按直连来源 IP 限流，经过反向代理的多个用户可能共享额度。写操作检查 CSRF token 和同域 Origin。Cookie 只支持 Control 接口，不支持模型 Gateway、Worker、MCP 或 API Key 管理。

控制台提供任务 JSON 提交、任务与执行尝试查询、Worker 状态、事件、产物审阅、取消和有限重试。操作能力由服务端权限及 Runtime capability 决定。失败任务重试仍限于服务端支持的 frozen single-mode Job，并重新检查安全重放和尝试预算。

重复点击提交使用同一幂等标识；明确创建另一任务时点击“新建提交标识”。离线期间不自动重放写操作。取消任务前会显示确认。

## 本地构建与验证

```sh
cd clients/control
npm ci
npm run check
cd ../..
go build -o bin/computecloud ./cmd/computecloud
```

本地显式设置 `server.web.assets_dir` 指向导出的 `clients/control/dist`；容器内默认路径为 `/usr/share/computecloud/control`。启用 Web 但静态产物缺失会使启动失败。普通 Go build/test 不要求先安装 Node。

浏览器验收使用隔离 Server、fixture Worker 和 Chromium，不调用付费模型：

```sh
cd clients/control
npx playwright install chromium
cd ../..
python3 scripts/ci_control_web_auth_e2e.py
```

报告位于 `dist/control-web-auth/report.json`，包含实际断言结果；失败截图用于排查 fixture 页面，不应将真实生产凭据用于该测试。
