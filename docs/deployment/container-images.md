# 多架构容器镜像部署与发布

- 状态：Implemented
- 架构决策：[ADR-020](../adr/0020-multiarch-container-packaging.md)
- CI Issue：[#55](https://github.com/tommyxie2026-tech/computecloud/issues/55)
- 支持平台：linux/amd64、linux/arm64

## 1. 镜像类型

computecloud 保持一个 Go binary，但发布两个 runtime filesystem：

~~~text
computecloud-server: scratch + computecloud + CA, non-root
computecloud-worker: Debian slim + computecloud + git + ssh + CA, non-root
~~~

这不是服务拆分。

## 2. 本机构建

需要 Docker Buildx：

~~~sh
make ci-container-image
~~~

输出：

~~~text
dist/container/server.oci.tar
dist/container/worker.oci.tar
dist/container/report.json
~~~

`report.json` 记录每个 image/architecture 的 manifest digest、layer 数和 registry compressed size。默认预算为 server 35 MiB、worker 100 MiB/architecture。

可以临时收紧验证：

~~~sh
SERVER_IMAGE_MAX_MIB=30 WORKER_IMAGE_MAX_MIB=90 make ci-container-image
~~~

CI 预算失败时应先检查新增 layer/package，不应直接提高预算。

## 3. Server 运行

默认命令：`computecloud server --config /etc/computecloud/computecloud.yaml`，UID/GID 65532。

~~~sh
docker run --rm --name computecloud-server \
  -p 7443:7443 \
  -v "$PWD/server.yaml:/etc/computecloud/computecloud.yaml:ro" \
  -v "$PWD/server-data:/var/lib/computecloud" \
  -v "$PWD/secrets:/run/secrets:ro" \
  ghcr.io/tommyxie2026-tech/computecloud-server:edge
~~~

容器配置建议：

~~~yaml
server:
  listen: 0.0.0.0:7443
  data_dir: /var/lib/computecloud
~~~

宿主 data directory 必须允许 UID 65532 写入。正式部署优先使用 `image@sha256:<digest>`。

## 4. Worker 运行

默认命令：`computecloud worker --config /etc/computecloud/computecloud.yaml`。基础 image 已包含 Git、OpenSSH client、CA，但**不包含 Codex/Claude**。

CLI Runtime 建议使用派生镜像：

~~~dockerfile
FROM ghcr.io/tommyxie2026-tech/computecloud-worker:<version>
USER root
# 安装组织批准并固定版本的 Runtime；不要 bake token/API key
USER 65532:65532
~~~

Worker 示例：

~~~sh
docker run --rm --name computecloud-worker-a \
  -v "$PWD/worker.yaml:/etc/computecloud/computecloud.yaml:ro" \
  -v "$PWD/worker-data:/var/lib/computecloud" \
  -v "$PWD/repos:/repos:ro" \
  -v "$PWD/secrets:/run/secrets:ro" \
  ghcr.io/tommyxie2026-tech/computecloud-worker:edge
~~~

配置中的 repository、Runtime executable 和 secret path 必须使用容器内路径。

## 5. Workspace / Repository

- repository mount 可为只读 source；
- Worker workspace/data volume 必须可写；
- 两个 Worker 不共享同一个 writable data_dir；
- Worker 重启继续挂载自己的原 data volume；
- 不默认把 host Docker socket 挂给 Worker；
- Worker 运行在容器中，与 EnvironmentProvider 使用容器/VM 是两个不同层次。

## 6. CI

独立 `container-image` Gate：

~~~text
Buildx server amd64+arm64
Buildx worker amd64+arm64
        ↓
manifest completeness
        ↓
compressed size budget
        ↓
amd64 version/git smoke
        ↓
PASS
~~~

PR/普通分支只验证不 push。main/tag 在 package Gate 后发布 GHCR。

## 7. GHCR tags

main：`edge`、`sha-<commit>`。

正式 release：`<version>`、`v<version>`、`latest`。每个 tag 都指向包含 amd64/arm64 的 OCI index。

## 8. SBOM / provenance

用于体积统计的 CI OCI archive 关闭 provenance，避免 attestation 混入 size budget。真正 push 到 GHCR 时启用 SBOM 与 max provenance。

## 9. GitHub Release

原有 binary assets 保持：amd64 tar.gz、arm64 tar.gz、SHA256SUMS。正式 Release 新增 `CONTAINER_IMAGES.txt`，记录 Server/Worker tags、source commit 和 multi-arch digest。

## 10. 体积策略

优先使用 static Go、trimpath/-s/-w、Server scratch、Worker no-install-recommends、Runtime/toolchain 不进入基础 Worker、.dockerignore、CI compressed-size Gate。明确不使用 UPX。

如果 Worker 需要 Node/Python/Browser/Go，创建面向特定任务的派生镜像，不永久加入通用 Worker base。

## 11. 故障排查

- Server permission denied：检查 data volume 的 UID/GID 65532 权限；
- Worker 找不到 Codex/Claude：预期行为，基础 image 不内置 Agent CLI；
- SSH clone 失败：挂载正确的 SSH material/known_hosts，不长期关闭 host key verification；
- arm64 build 较慢：Debian package install 可能通过 QEMU，规模扩大后可切原生多架构 builder。

## 12. 回退

镜像回退不能绕过 DB schema compatibility。仅 packaging 回退时可切回 previous image digest 并复用兼容 data volume；若应用版本包含 schema migration，仍按对应版本 runbook 的 backup/restore 规则处理。