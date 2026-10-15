# Worker 部署加固与目标主机验收设计

状态：已批准实施；目标主机隔离验收仍待完成
范围：现有双 Worker 受控部署的加固包；不直接变更运行中的主机或容器。

## 背景

公网部署安全复审发现，Server 的公网 HTTP/gRPC 入口有 TLS、鉴权和限流，但 Worker 的实际执行信任边界偏宽：Worker 容器以高权限运行并挂载长期凭据，另一 Worker 挂载范围过宽且可写的 Runtime home，Worker/Server 容器没有 CPU、内存、PID 预算，Worker egress 是否限制到必要目标尚未证明。仓库已经提供 strict OCI Runtime 的 CPU/内存/PID 控制、命名 egress network 以及 egress 验收工具；现有受控部署仍走 `trusted_only` 与宿主 CLI/凭据路径。因此首阶段目标是将已有能力转成可复用、可校验、不会夸大保障程度的部署交付。

## 目标

- 为 Worker A/B 提供专用 Runtime 配置目录，不再要求挂载用户个人的完整 Codex home。
- 在部署配置中显式设置非 root 身份、最小挂载、只读根文件系统、cap-drop、no-new-privileges、CPU/内存/PID 与日志预算；数值由管理员按机器容量配置并有合理校验范围。
- 将任务执行 Runtime 的出站网络限制到 Server、已配置模型入口和必要代码托管端点；网络规则由宿主机防火墙/运营网络实现，Worker 容器配置本身不能宣称其已生效。
- 提供自动化 validator 与安全负向验收步骤，分别说明“配置静态检查通过”和“目标主机网络/进程隔离已实测”。
- 维持产品定位为 Agent-aware Distributed Job Execution Platform / Agent Job Executor。

## 非目标

- 本阶段不在生产主机执行停机、换挂载、密钥轮换、防火墙、DNS 或安全组修改。
- 不声称 `read-only` Agent sandbox 或降低 UID 能把同一任务进程可读取的 API 凭据隔离起来。
- 不把仍使用宿主持久认证的 Worker 声明为 `isolated_only` 或适用于不可信多租户；该配置只允许在明确的可信单租户假设下运行。
- 不引入通用编排器、外部消息队列、多 Server HA、数据库替换或云平台专有依赖。
- 不默认公开模型凭据或将它们写入普通任务日志、事件、Artifact、验收证据。

## 设计

### 专用 Runtime home 与 UID

每台 Worker 使用独立服务账户和专用 Runtime home。部署文件将 Worker 可写状态、缓存与供应商 CLI 配置分开挂载；静态配置只读挂载，Worker 状态和专用 Runtime home 单独挂载并按 CLI 的写入需要设置权限。禁止挂入个人完整 home、SSH agent socket、Docker socket 或无关宿主目录。目录权限、所有权、只读/读写预期须由 validator 明确检查。

降为非 root 是必要的纵深措施，但若 CLI 执行进程仍能读取供应商认证文件，不能据此称为任务级凭据隔离。允许 trusted-only 示例把专用而非个人的 Runtime home 以读写方式挂载给 CLI，但须注明同一执行信任域可以读取其中凭据。使用 direct host credential 的 Worker 仍按 `trusted_only` 管理；只有 Attempt 级认证代理和隔离 Runtime 同时启用后，才可进入 strict 公网任务准入。

### 资源预算

部署配置需要分别为 Server、Worker 控制面容器和每个 Attempt OCI 容器设置资源上限。服务级至少覆盖 CPU、内存、PID、日志轮转；Attempt 级复用 Runtime controller 的 CPU、memory、pids limit。预算须保留给操作系统、Nginx、数据库和其他守护进程，避免所有容器预算总和超过主机容量。未设置/零值不得被 validator 当成有限预算。

### Egress

Attempt 使用独立、operator-created 的命名网络；strict OCI 拒绝 Docker 内置网络。目标主机应只允许到已配置的 Server/Gateway 与必要 SCM/依赖目标。DNS、代理和 TLS 根证书由运维配置；宿主 proxy/TLS 环境覆盖不应被隐式继承到 strict Attempt。验收脚本对显式允许目标和明确拒绝目标作无破坏探测，只报告标签、状态和脱敏错误，不做网段扫描。

Worker 控制面和 Attempt Runtime 若共用网络，应在设计/部署说明中明确这种可达关系。只有静态 YAML 检查不能证明宿主防火墙真正拦截了非 allowlist 连接。

### 验收证据

静态 validator 检查用户、挂载源/目标、挂载模式、只读根文件系统、capabilities、no-new-privileges、资源限额、运行模式、网络名称与敏感字段缺失。目标机验收额外绑定 Worker ID、平台、镜像 digest、配置摘要和运行时间，执行凭据文件读写负向测试、非允许目的地址 egress 拒绝测试、允许端点连通测试及资源触顶保护测试。

静态配置报告与目标机运行证据必须分别标识。证据不得包含 API key、证书私钥、提示词、完整个人路径、认证头或原始模型响应；配置摘要应排除 secret 值。

## 兼容性与迁移

- 文档/样例与 validator 先落地，不自动改生产容器。
- 当前宿主 CLI 路径可继续供可信单租户任务使用，但报告需显示 `trusted_only` 与未隔离凭据风险。
- 当某模型 Runtime 不支持 Attempt Gateway 时，部署指南必须将其标为 strict 模式不可用，而不是回退到宿主 key。
- 真实迁移须有候选镜像 digest、Worker 排空/停止、备份配置、回退步骤和重新注册/证书状态核验；操作另行批准并实施。

## 验收条件

1. 专用配置示例不包含个人 home 全目录和无关宿主挂载，且配置检查可拒绝读写错误或越权挂载。
2. 非 root、只读根文件系统、drop-all capabilities、no-new-privileges 与有限 CPU/内存/PID 在每种 Worker 部署样例中均可机读验证。
3. 未配置服务/Attempt 限额、内置 Docker 网络、缺失目标机身份或存在 secret-shaped 输出时，validator 失败关闭。
4. 文档明确说明静态验证不能替代目标机防火墙、OCI 逃逸或凭据不可读实测。
5. 目标机 acceptance 对每台 Worker 单独输出脱敏证据；CI fixtures 不能伪装成 target-host 通过。
6. 当前 Claude direct credential 部署仍被标注为可信单租户，直到 Claude Attempt Gateway 子项目完成并验收。

## 风险与权衡

- 过低的 cgroup 限额会让 Agent/Server 在正常负载下被 OOM 或触顶杀死；必须在预生产用实际任务测量后定值。
- 过窄 egress 会阻断模型或 SCM 流量；逐步以显式目标开通并保留拒绝证据。
- 不同宿主的 UID/GID 与 Codex CLI 文件要求可能不同；文档不得将 Linux UID 规则直接套用于 macOS Docker Desktop。
- 只读配置目录可能使 CLI 无法更新缓存；写路径应迁移到隔离状态目录，而不是扩大个人 home 挂载。

## 待后续决策

- Worker A/B 各自允许的模型、SCM、DNS 和软件分发目的地址。
- 各宿主的 CPU/内存/PID 数值、日志保留量和磁盘预算。
- 真实 Worker 验收窗口、回退时间和验收证据存放位置。
