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

容器化部署支持通过 **Server IP + TCP 端口**直接交互，但所有跨主机流量必须加密：

| 连接 | 地址示例 | 协议 | 用途 |
| --- | --- | --- | --- |
| Worker/Task CLI → Server | `10.20.0.10:7443` | gRPC over TLS | Worker 注册、任务分配、Attempt/Artifact 传输、Task CLI |
| Job CLI/MCP → Server | `https://10.20.0.10:7444` | HTTPS (TLS) | Job 投递、查询、事件、Artifact 下载、MCP |

7443 和 7444 使用同一 Server 证书；证书 SAN 必须包含客户端实际访问的 IP（例如 `IP:10.20.0.10`），或者改用证书中已有的 DNS 名称。TLS 负责传输机密性和完整性，Bearer Token 负责身份和权限，两者不能互相替代。当前协议最低为 TLS 1.2，生产环境不启用客户端证书认证。

~~~sh
SERVER_IP=10.20.0.10
docker run --rm --name computecloud-server \
  -p "${SERVER_IP}:7443:7443" \
  -p "${SERVER_IP}:7444:7444" \
  -v "$PWD/server.yaml:/etc/computecloud/computecloud.yaml:ro" \
  -v "$PWD/server-data:/var/lib/computecloud" \
  -v "$PWD/secrets:/run/secrets:ro" \
  ghcr.io/tommyxie2026-tech/computecloud-server:edge
~~~

容器配置建议：

~~~yaml
server:
  listen: 0.0.0.0:7443
  http:
    listen: 0.0.0.0:7444
    allowed_origins: []
  tls:
    cert_file: /run/secrets/server.crt
    key_file: /run/secrets/server.key
  data_dir: /var/lib/computecloud
~~~

宿主 data directory 必须允许 UID 65532 写入。正式部署优先使用 `image@sha256:<digest>`。

`0.0.0.0` 只表示容器监听所有容器接口，不代表应该向互联网开放。宿主防火墙至少应限制 7443 仅允许 Worker 网段，7444 仅允许运维客户端或受信代理；不要发布数据库、工作目录或 Docker socket。

### 3.1 证书与密钥准备

生产环境应从组织 PKI 获取 Server 证书。证书的 SAN 必须覆盖实际 IP/端口对应的主机身份；端口不写入 SAN。Server 容器只挂载私钥和证书，Worker/客户端只挂载 CA，不复制 Server 私钥：

~~~text
server-secrets/
├── server.crt       # Server 证书，容器内只读
├── server.key       # Server 私钥，0600，仅 Server 可读
└── ca.crt           # 签发 CA；分发给 Worker/客户端
~~~

仅用于实验的自签名证书也必须包含 IP SAN，示例（不要用于生产）：

~~~sh
# 仅实验环境：先创建临时 CA，再签发包含 IP SAN 的 Server 证书。
openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
  -keyout ca.key -out ca.crt -subj '/CN=computecloud-dev-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign'
openssl req -newkey rsa:2048 -nodes -keyout server.key -out server.csr \
  -subj '/CN=computecloud-server'
cat > server-ext.cnf <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:10.20.0.10
EOF
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 30 -sha256 -extfile server-ext.cnf
# 生产环境改用组织 PKI；ca.key 只留在签发环境，绝不挂载进 Server 容器。
openssl x509 -in server.crt -text -noout | grep -A1 'Subject Alternative Name'
chmod 0600 server.key
~~~

### 3.2 Worker 与客户端的 IP+端口配置

Worker 不接受入站端口，只主动连接 Server 的 7443：

~~~yaml
worker:
  id: worker-a
  server_address: 10.20.0.10:7443
  token_file: /run/secrets/worker-a.token
  tls:
    ca_file: /run/secrets/ca.crt
~~~

Job CLI/MCP 客户端同时配置 gRPC 地址和 HTTPS URL：

~~~yaml
client:
  address: 10.20.0.10:7443
  http_url: https://10.20.0.10:7444
  token_file: /run/secrets/task.token
  tls:
    ca_file: /run/secrets/ca.crt
~~~

非回环 IP 地址禁止设置 `insecure_loopback`。该选项只允许字面量 `127.0.0.1`/`[::1]`，不能用来关闭容器或跨主机连接的加密。

### 3.3 加密链路验收

Server 启动后先从客户端主机核验 TLS 证书，再验证应用层 Token：

~~~sh
SERVER_IP=10.20.0.10
openssl s_client -connect "${SERVER_IP}:7443" \
  -CAfile ca.crt -verify_return_error </dev/null

curl --fail --cacert ca.crt \
  -H "Authorization: Bearer $(cat task.token)" \
  "https://${SERVER_IP}:7444/v1/capabilities"

computecloud workers --config client.yaml
~~~

验收必须确认：

- `openssl s_client` 验证链成功，且证书 SAN 包含实际 IP；
- 使用 `http://` 访问 7444 被客户端拒绝，不能通过明文绕过；
- 缺少/错误 Token 返回 401 或 gRPC `Unauthenticated`；
- Worker 能通过 7443 注册，Server 日志不打印 Token、私钥或完整任务输入。

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

Worker 容器不发布端口；只需允许其出站访问 `${SERVER_IP}:7443`。如果 Worker 和 Server 位于不同主机，优先使用可路由的宿主 IP 或内部 DNS，不使用 Docker bridge 的容器私有 IP。

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
- `x509: certificate is not valid for <IP>`：证书缺少该 IP 的 SAN，重新签发证书或使用证书中的 DNS 名称；
- `x509: certificate signed by unknown authority`：将正确的签发 CA 只读挂载到 Worker/客户端，并配置 `tls.ca_file`；
- `tls: first record does not look like a TLS handshake`：检查客户端是否误用明文地址/端口，7443/7444 均不得用明文跨主机访问；
- HTTP 客户端报 `HTTP requires TLS`：将 `client.http_url` 改为 `https://IP:7444`，不要设置非回环 `insecure_loopback`；
- SSH clone 失败：挂载正确的 SSH material/known_hosts，不长期关闭 host key verification；
- arm64 build 较慢：Debian package install 可能通过 QEMU，规模扩大后可切原生多架构 builder。

## 12. 回退

镜像回退不能绕过 DB schema compatibility。仅 packaging 回退时可切回 previous image digest 并复用兼容 data volume；若应用版本包含 schema migration，仍按对应版本 runbook 的 backup/restore 规则处理。


## 9.1 移动端 Control

移动端不是 Server/Worker 容器，也不连接 7443。它以 companion client 访问 Server 的 HTTPS 7444，并使用 SecureStore 保存设备 profile。v0.4.6 没有签名 IPA/APK/AAB；部署、CI artifact 和本地 iOS/Android 构建见 [移动端 Control 部署指南](mobile-control.md)。跨主机移动访问必须校验 HTTPS 证书和最小权限 Token，禁止把 Token 放进 computecloud: 深链。
