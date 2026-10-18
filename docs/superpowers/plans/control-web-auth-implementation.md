# Control Web 与用户认证实施计划

> **执行要求：** 使用 superpowers:executing-plans 按任务顺序实施；如用户选择委派，再使用 superpowers:subagent-driven-development。以复选框跟踪进展。

**目标：** 交付随 Server 发布、支持用户名密码登录的中文 Web 首版。

**架构：** 复用 Expo Web 与现有 Control API。Server 提供静态文件、不透明 Cookie 会话和 CSRF 防护，登录身份映射现有 user，继续由既有授权和控制租约执行检查。

**技术栈：** Go net/http、现有配置与身份模块、标准密码派生库、TypeScript、React Native Web/Expo、浏览器 E2E、Docker 多阶段构建。

**规格：** [Control Web 首版与用户认证规格](../specs/control-web-auth-design.md)。

## 全局约束

- 交付顺序为 Web、iOS、Android；本计划只实现 Web。
- `web.enabled` 默认 false；账号密码默认 `admin/admin`，支持配置改密。
- 明文配置与密码文件互斥；错误密码文件不能回退默认密码。
- 必须唯一绑定现有 `identity_owner`，不能因 admin 名称提升权限。
- 会话默认 28800 秒，可配置 300 至 86400 秒；服务端重启撤销会话。
- Web 与 Server 同域、同监听；HTTPS 使用 Secure Cookie；HTTP 仅用于明确配置的 loopback 开发入口。
- 保持原有 Bearer、Worker、Gateway 和原生对话协议；不把 Cookie 扩展到模型调用或密钥管理。
- 中文文档不记录环境具体信息、真实凭据或时间信息。
- 不修改线上部署、防火墙或现有客户端配置；验证使用隔离 fixture。

## 重点审查

- HTTPS 代理前的内部 HTTP：Secure Cookie 不能因伪造转发头被降级，任务 2、3 测试。
- 原有 user owner 重复：启用 Web 必须拒绝歧义绑定，任务 1 测试。
- 浏览器刷新与流式连接过期：终止旧观察并重新登录，任务 4、6 测试。
- 页面回退吞掉 API 错误：未知 `/v1/` 保持 API 错误，任务 5 测试。
- 密码文件挂载错误或空值：明确失败而非开启默认密码，任务 1 测试。

## 任务 1：配置与身份绑定

**文件：** 修改 `internal/config/config.go`；新增 `internal/config/web.go`、`internal/config/web_test.go`；修改 `internal/server/server.go`。

**接口：** `config.Web`、`config.WebAuth`；`config.ValidateWeb(web Web, users []Identity) error`；`config.WebPassword(auth WebAuth) (string, error)`。Server 持有独立 Web auth 状态，初始化失败返回启动错误。

- [ ] 写 `TestWebDefaults`、`TestWebPasswordSource`、`TestWebIdentityBinding`、`TestWebSessionTTL`：断言 admin 默认值、互斥/空/不可读文件失败、重复 owner 失败、期限端点通过与越界失败。
- [ ] 运行 `go test ./internal/config -run TestWeb -count=1`，确认新增行为尚未实现导致失败。
- [ ] 实现配置字段、路径解析、默认值与身份验证，密码只在启动初始化时读取。
- [ ] 重跑上述测试并运行 `go test ./internal/config -count=1`，要求全部通过。
- [ ] 提交配置与测试，提交信息 `feat: configure Control Web authentication`。

## 任务 2：登录与有界会话

**文件：** 新增 `internal/server/web_auth.go`、`internal/server/web_auth_test.go`；修改 `internal/server/server.go`。

**接口：** `newWebAuth(cfg config.Web, users []config.Identity) (*webAuth, error)`；`(*webAuth).login(w http.ResponseWriter, r *http.Request)`；`session`、`logout` 同签名；`authenticate(r *http.Request) (config.Identity, error)`。内部会话记录保存摘要、身份、CSRF 值和到期时间。

- [ ] 写 `TestWebAuthLogin`、`TestWebAuthLifecycle`、`TestWebAuthCookieTransport`、`TestWebAuthLimits`：断言统一 401、注销/过期/重启失效、HTTPS Secure/HttpOnly/Strict、伪造代理头不能降级、全局会话与来源集合有界。
- [ ] 运行 `go test ./internal/server -run TestWebAuth -count=1`，确认失败。
- [ ] 实现标准密码派生校验、随机 32 字节会话令牌、恒定时间摘要比较、8 小时默认过期、登出回收；按直连 IP 限流并限制全局并发校验。会话总量上限 1024；满额先清理过期记录，仍满则返回 503，避免静默驱逐活跃用户。
- [ ] 重跑测试及 `go test -race ./internal/server -run TestWebAuth -count=1`，要求通过。
- [ ] 提交会话实现与测试，提交信息 `feat: add bounded Web login sessions`。

## 任务 3：Cookie 授权桥接与 CSRF

**文件：** 新增 `internal/server/web_authorization.go`、`internal/server/web_authorization_test.go`；修改 `internal/server/http.go`，必要时扩展 `internal/rpcutil` 的身份注入接口并补测试。

**接口：** `(*Server).webIdentity(r *http.Request) (context.Context, error)`；`(*webAuth).validateWrite(r *http.Request) error`；登录响应 `{username, expires_at_ms, csrf_token}`。Cookie 可用路由采用显式允许列表。

- [ ] 写 `TestWebAuthorizationScopes`、`TestWebAuthorizationBearerPrecedence`、`TestWebAuthorizationCSRF`、`TestWebAuthorizationRouteBoundary`：跨 owner 拒绝、无效 Bearer 不回退、缺失/错误 CSRF 和跨域写拒绝、Cookie 不能创建密钥或调用模型。
- [ ] 运行 `go test ./internal/server -run 'TestWebAuthorization' -count=1`，确认失败。
- [ ] 接入现有身份 context 和 API scope 检查；注册登录/会话/注销路由，Cookie 写请求检查 CSRF 与 Origin。外部来源固定配置优先，否则要求实际请求 Host 匹配；不依据未验证的代理头判断来源。
- [ ] 运行 `go test ./internal/server ./internal/rpcutil -count=1 -timeout=300s`，要求既有测试与新测试全部通过。
- [ ] 提交授权与测试，提交信息 `feat: authorize Control Web requests safely`。

## 任务 4：中文登录与任务页面接入

**文件：** 新增 `clients/control/src/webAuth.ts`、`clients/control/src/Login.tsx`；修改 `clients/control/App.tsx`、`clients/control/src/api.ts`、`clients/control/src/types.ts`。

**接口：** `WebSession` 对应任务 3 响应；`login(username: string, password: string): Promise<WebSession>`；`getSession(): Promise<WebSession>`；`logout(csrfToken: string): Promise<void>`。`ControlAPI` 保留原 Bearer 构造用法，并新增显式 Web session 认证选项；共用请求层处理 Cookie 与 CSRF。

- [ ] 在浏览器用例中先写默认登录、错误密码、页面刷新、登出和 401 清理状态断言，新增浏览器运行器前先确认用例失败。
- [ ] 实现 Web 同域登录与初始化；移动端继续保留原有凭据连接方式，Web 不展示长期 API Key 输入。
- [ ] 将现有任务/Worker/产物页面文案中文化，提交保持幂等键；取消和重试确认作用对象，按 capability 和 lease 开放。
- [ ] 流式请求统一走可携带 Cookie 的请求层；401 终止旧流、清空任务快照与凭据状态并返回登录；断网不排队写操作。
- [ ] 运行 `npm ci`、`npm run check`（目录 `clients/control`），要求类型检查与 Web 导出通过；浏览器行为由任务 6 完整验证。
- [ ] 提交页面与 API 改动，提交信息 `feat: add Chinese Control Web login and task UI`。

## 任务 5：Server 静态页面与镜像集成

**文件：** 新增 `internal/server/web_static.go`、`internal/server/web_static_test.go`；修改 `Dockerfile`、`Makefile`、`internal/config/web.go`、`internal/server/http.go`、`.dockerignore`（如存在）。

**接口：** 配置增加可选 `server.web.assets_dir`；容器默认产物路径 `/usr/share/computecloud/control`。`newWebStaticHandler(dir string) (http.Handler, error)` 验证入口存在并服务资源；Web 启用但资源不存在时启动失败。开发通过显式 assets_dir 指向导出目录。

- [ ] 写 `TestWebStaticRoutes`、`TestWebStaticBoundary`、`TestWebStaticMissingAssets`：入口及深链可用、未知 API 不回退、遍历/列表禁止、缺产物启动失败、安全头存在。
- [ ] 运行 `go test ./internal/server -run TestWebStatic -count=1`，确认失败。
- [ ] 接入 `/ui/` 与根路径引导、资源类型/缓存和安全头；CSP 依据导出资源设置并由浏览器用例验证。
- [ ] Docker 增加锁定 Node 构建阶段，导出并复制 Web 到 Server；保持普通 Go build/test 不依赖 Node，Worker 不引入前端层。
- [ ] 重跑静态测试、`make build`、Web 导出及隔离容器启动检查；CI 构建 linux/amd64 与 linux/arm64 Server。
- [ ] 提交静态服务与构建改动，提交信息 `feat: bundle Control Web with Server image`。

## 任务 6：浏览器验收、CI 与中文文档

**文件：** 新增 `clients/control/scripts/web-browser-e2e.mjs`、`scripts/ci_control_web_auth_e2e.py`、`docs/deployment/control-web.md`；修改 `clients/control/package.json`/锁文件、`.github/workflows/ci.yml`、`README.md`、`examples/server.yaml`（以现有实际 Server 示例路径为准）；同步客户端路线图。

**接口：** 浏览器测试用 Playwright，读取测试启动器生成的临时测试配置；测试启动器运行隔离 Server/fixture Worker，不读取线上凭据；输出脱敏断言报告与失败截图。

- [ ] 新增独立浏览器测试依赖与 `test:web` 命令，先运行任务 4 用例验证能发现错误登录/状态清理缺陷。
- [ ] 补完整流程：登录、配置改密后重启、任务提交至终态、重复提交只生成一个任务、取消/有限重试、Worker/产物展示、断线游标恢复、会话失效时 SSE 中止、窄屏布局。
- [ ] 运行 `python3 scripts/ci_control_web_auth_e2e.py`，所有断言通过；保留报告但不保存密码或会话内容。
- [ ] CI 加入 Web 认证与浏览器 Gate，运行 `go test ./... -count=1 -timeout=300s`、`npm run check` 和多架构构建；失败应阻止本轮合并。
- [ ] 补中文部署指南、默认密码警告、secret-file 示例、身份绑定、HTTPS 代理及改密重启/会话失效说明；路线图标明 Web 交付证据与 iOS/Android 后续项。
- [ ] 完成整分支审阅与 `git diff --check`，逐项记录实际通过/未验证状态，再提交代码与文档。线上发布与环境变更等待单独明确指示。

## 执行与完成条件

建议 Native：当前会话按 1 至 6 顺序实施，每任务保留测试证据与提交。实施前创建专用工作分支，保留当前本地验证文档和证书，不删除、不纳入提交。若执行期间发现与本规格矛盾，先修订明确设计再继续。

完成要求为六项交付及 CI 验收通过；不以页面构建成功代替真实浏览器操作，不以 fixture 结果声称真实 Provider 或移动设备验证完成。
