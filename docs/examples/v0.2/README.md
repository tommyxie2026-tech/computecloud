# v0.2 接入示例

这些示例对应已实现的 v0.2，使用 `job submit` 或 HTTP/MCP；不能交给旧 `task submit`。日期：2026-09-22。

| 文件 | 用途 |
| --- | --- |
| [job.schema.json](job.schema.json) | Draft 2020-12 请求结构；额外业务约束见契约 |
| [job-single.json](job-single.json) | single Job 示例 |
| [job-map-reduce.json](job-map-reduce.json) | Codex + Claude 分片审查、Codex 汇总 |
| [codex-mcp.toml](codex-mcp.toml) | 本地 Codex 通过 MCP 提交远端任务 |
| [codex-gateway.toml](codex-gateway.toml) | 本地 Codex 使用 G1 模型代理；不会自动委派任务 |
| [server-v0.2.yaml](server-v0.2.yaml) | Server 增量配置段，非完整可运行配置 |

两个 Job 示例中的基线取项目 v0.1 代码提交。执行前需在各 Worker 配置 repository_ref=computecloud 并准备该 commit；configured-*-model、凭据/策略/验收名称均为需替换的占位引用，不能推定为上游真实模型或已经存在的模板。

按[运行指南](../../implementation/v0.2-runbook.md)完成配置后，可用 HTTP 提交：

```sh
curl --fail-with-body \
  -H "Authorization: Bearer ${COMPUTECLOUD_TASK_TOKEN}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: review-v01' \
  --data-binary @job-map-reduce.json \
  https://computecloud.example.internal:7444/v1/jobs
```

Token 由操作环境提供，不写入仓库。Token 不同于上游 API Key。MCP 与模型网关可同时配置，但应使用不同权限的 Token；后台 Worker 不加载任务提交 MCP 配置。无需模型代理时，只配置 MCP 即可远端委派。

JSON Schema 只能验证结构：分片 key 唯一、权限、真实仓库基线、UTF-8 字节大小、路径安全、模板摘要、补丁交集等必须在服务端校验。示例解析通过不代表远端服务或真实模型已验证。
