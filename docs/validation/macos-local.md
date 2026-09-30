# macOS 原生本机验证

无需容器或 Linux 虚拟机。需要 Go 1.26+、Git、Python 3.12+；race 检测还需要 Xcode Command Line Tools（`xcode-select --install`）。首次构建需要网络下载工具链及模块。所有命令在仓库根目录运行。

## 自动验证（不调用真实模型）

```sh
go version
python3 --version
go mod download
go mod verify
make build
./bin/computecloud version
make vet test race
make smoke
make ci-flow
make ci-retry-flow
make ci-artifact-flow
make capacity-check
```

测试会启动本机 Server、Worker 和模拟 CLI 子进程，使用临时 SQLite 与 Git 工作区。运行环境须允许回环网络监听、内核进程查询及进程组信号；受限沙箱中应授权运行测试，不能把查询失败视为进程已经退出。

`dist/ci-task-flow/`、`dist/ci-retry-flow/`、`dist/ci-artifact-flow/` 保存报告和日志；小容量报告为 `dist/capacity-check-*.json`。命令退出码应为 0，报告应通过。故障注入产生 FAILED Job 是预期行为，以用例结果判断成功。

## 真实 CLI 验证

在运行 Worker 的同一 macOS 用户下安装并登录 Codex / Claude。依照 `examples/worker.yaml` 填写 CLI 完整版本输出、模型、Git 仓库绝对路径；删除未安装的 runtime。配置 Server/Worker Token、客户端 Token，并将 Task 的 commit 替换为测试仓库的完整 commit。配置文件内相对路径以配置所在目录为基准。

按 [基础运行指南](../implementation/v0.1-runbook.md) 启动 Server、Worker，先提交 `examples/task.json` 只读任务。再按 [Job 运行指南](../implementation/v0.2-runbook.md) 配置模板摘要、HTTP 入口与 Job scopes，验证 single、Map/Reduce、取消和产物下载。真实 CLI 验证可能消耗模型额度，自动 fixture 验证不证明真实 CLI 兼容性。

## 实现与边界

- Linux 保持 boot ID + `/proc` start ticks 的持久进程身份格式。
- macOS 使用 `kern.bootsessionuuid` + 内核进程启动时间（微秒），通过 `kern.proc.pgrp` 检查进程组，忽略僵尸进程。查询失败不确认清理；身份不匹配不发送信号。
- 两平台共用 SIGTERM → 宽限期 → SIGKILL 清理流程。进程组监管不是隔离沙箱；自行脱离进程组的程序不在受信任务约定内。
- 本地数据目录属于运行主机；不要把存有活动执行的 Worker 数据目录跨主机复制后直接恢复运行。
- 容量采样在 macOS 使用系统 `ps`，CPU 时间精度和采样开销与 Linux 不同；不产生 cgroup 指标。
- 本机验证不替代独立主机网络、TLS、真实模型或生产容量验收。

## 本次本机验证记录

2026-09-27，darwin/arm64、Go 1.26.0、Python 3.13.3、computecloud 0.3.2，无容器：

| 检查 | 结果 |
| --- | --- |
| `go mod verify`、构建、`go vet ./...` | PASS |
| `go test ./... -count=1` | PASS |
| `go test -race ./... -count=1` | PASS |
| 进程身份不匹配、父进程退出后清理、忽略 SIGTERM 的进程组 | PASS |
| `make smoke`（含取消、Worker/Server SIGKILL、备份恢复） | PASS |
| `make ci-flow` | PASS：6 步，2 成功 / 1 取消 / 1 预期失败 Job |
| `make ci-retry-flow` | PASS：4 步，schema v6 |
| `make ci-artifact-flow` | PASS |
| Python 自测、`make capacity-check` | PASS：8 项自测；1/2 Worker、每节点 1 slot、每组 4 Job |
| Linux amd64 进程监管测试交叉编译 | PASS；未执行 Linux 运行测试 |

GitHub macOS CI 已加入配置，尚未在远端执行。真实 Codex / Claude、独立多机和生产容量未在本次验证中运行。
