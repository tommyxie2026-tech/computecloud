# Control Web 首版与用户认证规格

## 目标与阶段

客户端按 Web、iOS、Android 的顺序交付。首阶段复用现有 Expo Control 客户端，将 Web 产物与 Server 一起发布，实现中文登录、任务提交、执行跟踪、Worker 查看、结果审阅、取消与有限重试。

项目定位保持 Agent-aware Distributed Job Execution Platform / Agent Job Executor。Server 是任务、权限、租约和状态的唯一事实源，客户端不参与调度。

本规格只覆盖 Web 首版。iOS、Android 后续复用 API 与组件，分别补设备凭据、配对、平台构建与验收。多用户管理、注册、找回密码、OIDC、移动通知不进入本轮。

## 现有基础与方案选择

已有 `clients/control` Expo 应用、Bearer API 客户端、Job/Worker 查询、控制租约、任务提交及有限重试。Server 已有 owner/project/credential/scope 授权。

采用 Server 同域静态页面与不透明 Cookie 会话。相比浏览器保存长期 API Key，该方案支持服务端立即注销，且密钥不暴露给前端；相比独立前端服务，不增加部署进程、跨域配置或独立 BFF。

## 配置契约

```yaml
server:
  web:
    enabled: true
    auth:
      username: admin
      password_file: /run/secrets/web_admin_password
      identity_owner: operator
      session_ttl_seconds: 28800
```

- `web.enabled` 默认 false，已有部署必须显式启用。
- 账号默认 `admin`；`password` 和 `password_file` 均未设置时密码默认 `admin`。
- 支持直接配置 `password`，推荐受保护的 `password_file`。二者同时设置时报配置错误，不静默覆盖；指定空文件、不可读文件或空密码时报错，不回退默认密码。密码文件仅移除结尾换行。
- `identity_owner` 必须唯一匹配现有 Server user 身份；缺失、匹配多个或不存在时拒绝启用 Web，避免隐式继承过大权限。
- 会话有效期默认 8 小时，可配置 5 分钟至 24 小时；不自动无限续期。
- 密码仅启动时读取和派生校验值，不记录明文；修改配置或密码文件后重启生效。
- 默认密码允许按需求登录，启动时输出不含凭据的默认密码警告。公网部署指南要求先修改默认密码。

## 部署与路由

- Web 入口为 `/ui/`；前端资源由 Server 提供，根路径引导至 `/ui/`。
- 复用已有 HTTP/HTTPS 监听，不新开端口；已有 API、gRPC 和 Worker 通信保持既有接口。
- Docker 独立 Node 构建阶段生成静态产物，复制进入 Server 镜像；运行时不需要 Node。Worker 镜像不包含 Web。
- 本地 Go 构建保留不依赖 Node 的开发路径；缺少前端资源但显式启用 Web 时返回清晰的启动错误，不能运行成空白页面。
- SPA 回退只适用于 `/ui/` 页面路径，未知 API 路径不能返回 HTML。静态服务禁止目录遍历与目录列表。

## 登录与会话

接口为 `POST /v1/web/auth/login`、`GET /v1/web/auth/session`、`POST /v1/web/auth/logout`。

登录仅接受受限大小的 JSON 用户名和密码。错误统一返回认证失败，不区分账号是否存在。密码使用带随机盐的标准密码派生算法校验，复用依赖中可用的成熟实现，不自行设计密码算法。

成功后生成至少 256 bit 随机会话令牌；服务端只保存令牌摘要、身份引用、CSRF 校验值和绝对过期时间。会话存储在有上限的内存集合，过期回收；Server 重启清空会话，因此配置改密重启同时撤销旧会话。登出立即删除当前会话。

Cookie 设置 HttpOnly、SameSite=Strict、Path=/；HTTPS 设置 Secure。HTTP Cookie 仅允许服务端明确配置的 loopback 开发监听；外部 TLS 终止场景默认仍发送 Secure Cookie，不凭任意 X-Forwarded-Proto 降级。

浏览器 API 请求同域携带 Cookie，登录状态查询返回身份显示信息、有效期和 CSRF token，不返回密码、会话令牌或长期 API Key。前端不把这些凭据写入 localStorage。

## 授权与请求防护

- Cookie 仅用于列明的 Control Web API；不用于 Worker、模型 Gateway、原生对话接口或 API Key 签发/撤销接口。
- 每次请求从有效会话恢复配置身份，复用既有 owner/project/credential/scope 校验。Web admin 名称不代表全局权限，也不绕过 Job 所有权、控制租约、generation 或 capability。
- 显式 Authorization 请求沿用既有 Bearer 行为；错误 Bearer 不回退 Cookie，以免身份混淆。
- Cookie 写请求，包括登出，要求匹配的 CSRF token 和同域 Origin 校验。登录也进行来源校验；源判断采用固定配置或当前请求的实际外部来源规则，不无条件信任转发头。
- 登录按直连来源 IP 限流，同时限制全局并发密码校验和会话总量；所有限流集合必须有界。反向代理后不依据任意客户端自报 IP 绕过限制。
- 页面与 API 设置合适的 CSP、nosniff、frame-ancestors 和敏感响应 no-store。CSP 以实际导出资源验证，避免启用过宽脚本权限。
- 日志只记录结果、请求标识和受控身份字段，不记录密码、Cookie、CSRF token 或请求正文。

## 页面和交互

Web 默认使用当前同域 Server，无需输入 API Key 或服务器地址。登录后展示任务列表、Worker 列表、任务详情、任务与 Attempt 状态、事件和产物；沿用既有受控 Job spec 提交能力，使用中文字段说明。

取消和重试显示作用对象及确认提示，按服务端广告的能力开放；重试保持现有 single-mode frozen Job 边界。输入、审批及其他操作只在对应 Runtime contract 已支持时展示。

刷新页面后查询当前会话并恢复观察；会话失效后停止观察、清空敏感页面状态并返回登录。断线后按已有游标和快照语义恢复；离线不排队重放写操作。重复提交复用幂等键，只有创建新任务意图时才更换键。

## 验收与交付

1. 配置测试覆盖默认账号、两种改密方式、冲突、空文件、权限身份匹配和期限边界。
2. Server 集成测试覆盖正确/错误登录、限流、Cookie 属性、过期、退出、重启失效、CSRF、Origin、错误 Bearer 不回退和跨 owner 拒绝访问。
3. 原有 Bearer、Worker、Gateway、Control lease 与 capability 测试继续通过。
4. 浏览器 E2E 覆盖默认登录、配置改密、刷新、退出、失效返回登录、任务提交至终态、取消/有限重试和断线恢复。测试用 fixture，不调用付费 Provider。
5. 通过类型检查、Web 导出、静态路由测试、Server 相关 Go 测试以及多架构 Server 容器构建；检查 Worker 不引入前端依赖。
6. 中文 README、配置示例与部署指南只使用通用占位信息，不写入本地或线上环境资料、凭据及时间信息。

交付为代码、测试、文档和 CI 证据。线上镜像升级、真实任务验证与防火墙修改属于后续单独确认的操作。
