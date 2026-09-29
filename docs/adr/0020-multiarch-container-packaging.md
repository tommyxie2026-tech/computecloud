# ADR-020：多架构最小容器镜像交付

- 状态：Accepted
- 日期：2026-09-29
- Issue：[#55](https://github.com/tommyxie2026-tech/computecloud/issues/55)
- 影响范围：Release Packaging、CI、GHCR、Server/Worker 部署

## 1. Context

computecloud 已经保持单 Go binary，同时支持 Server、Worker 与 CLI，并且现有 release 会生成 Linux amd64/arm64 tar.gz。随着 EnvironmentProvider、Prepared Workspace 和私有 Worker 部署推进，需要增加容器交付，但不能因此把单体部署演进成强制微服务。

目标：amd64/arm64 multi-platform；Server 尽可能小且 non-root；Worker 保留 Git/SSH 与常见 Agent CLI 的 Linux 用户态兼容性；Runtime CLI 不绑定到基础镜像；镜像体积成为 CI Gate；正式镜像带 SBOM/provenance；tar.gz 发布继续保留。

## 2. 方案比较

| 方案 | 优点 | 问题 | 结论 |
| --- | --- | --- | --- |
| 全部 scratch | 最小 | Worker 无 Git/SSH/glibc | 仅 Server 合适 |
| 全部 Alpine | 小 | musl 对部分第三方 CLI 有兼容风险 | 不作为统一 runtime |
| Distroless | 攻击面小 | Worker 仍缺 Git/SSH；Server 用 static Go 时收益有限 | 暂不引入 |
| Server scratch + Worker Debian slim | Server 极小；Worker 兼容性更好 | 两个镜像、Worker 稍大 | 采用 |

## 3. Decision

### 3.1 一个 Dockerfile，两个 target

~~~text
Dockerfile
├── target=server
└── target=worker
~~~

两个 target 使用同一个 computecloud binary，不拆源码或服务。

### 3.2 Multi-architecture build

正式支持 linux/amd64、linux/arm64。Builder 使用 BUILDPLATFORM，Go 使用 `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH` 交叉编译，并启用 `-trimpath -buildvcs=false -ldflags '-s -w'`。

### 3.3 Server image

Server 使用 scratch，仅保留 static computecloud、CA bundle、/tmp、/var/lib/computecloud、/etc/computecloud。默认 `USER 65532:65532`，无 shell、package manager、Git、SSH、Agent CLI 或 Go toolchain。

### 3.4 Worker image

Worker 使用 `debian:bookworm-slim`，只通过 `--no-install-recommends` 安装 ca-certificates、git、openssh-client，默认 UID/GID 65532。

基础 Worker 不预装 Codex、Claude、Node、Python、Go 或 Browser。需要特定 Runtime 时创建受控派生镜像，Runtime 版本、许可证与凭据继续独立管理。

### 3.5 Size budget

CI 检查 registry-transfer compressed bytes：

| Image | amd64 | arm64 |
| --- | ---: | ---: |
| server | <= 35 MiB | <= 35 MiB |
| worker | <= 100 MiB | <= 100 MiB |

超过预算必须显式 Review。明确不使用 UPX，避免影响扫描、调试、启动和可重复性。

### 3.6 CI 与发布

每个分支/PR 构建 server/worker 的 amd64+arm64 OCI，验证 manifest、体积预算，并执行 amd64 version/git smoke。main/tag 在完整 Gate 通过后发布到 GHCR：

~~~text
ghcr.io/<owner>/computecloud-server
ghcr.io/<owner>/computecloud-worker
~~~

main tags：`:edge`、`:sha-<12>`。正式发布 tags：`:<semver>`、`:v<semver>`、`:latest`。

正式 registry push 启用 BuildKit `--sbom=true` 和 `--provenance=mode=max`。GitHub Release 保留原 tar.gz，并增加 `CONTAINER_IMAGES.txt` 记录 immutable multi-arch digest。

## 4. Security boundaries

- 两类镜像默认 non-root；
- Server image 无交互 shell；
- secret 不 bake 进 image；
- config/token/certificate 使用只读 mount/secret 注入；
- Worker 不默认挂 Docker socket；
- 容器化不改变 Worker 主动出站连接模型；
- 正式部署优先 pin digest，而不是长期依赖 edge/latest。

## 5. Reproducibility

首轮基础镜像使用明确版本系列：`golang:1.27.1-alpine`、`alpine:3.22`、`debian:bookworm-slim`。正式发布记录最终 image digest、SBOM 和 provenance。后续可增加基础镜像 digest lock/Renovate。

## 6. Consequences

收益：Server 攻击面接近 static binary；Worker 不因极限减重牺牲 glibc/CLI 兼容；amd64/arm64 与 binary release 对齐；size regression 被 CI 拦截；Runtime CLI 与核心镜像解耦。

代价：Server/Worker 两个镜像；Worker 大于 Alpine/scratch；multi-arch Worker build 需要 QEMU 或原生 builder；release 时间增加。

## 7. Non-goals

Kubernetes Operator、Helm chart、Docker-in-Docker Worker、bundled Codex/Claude、Runtime image marketplace、container EnvironmentProvider 本身、微服务拆分都不属于本 ADR。

## 8. Release invariants

~~~text
server + worker both contain linux/amd64 and linux/arm64
server has no shell/package manager
both images run non-root
worker contains git + ssh but no Agent runtime by default
compressed image sizes remain within CI budget
published images have SBOM + provenance
GitHub Release records immutable image digests
~~~