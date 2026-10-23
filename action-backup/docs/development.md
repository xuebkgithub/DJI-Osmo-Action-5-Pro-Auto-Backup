# 开发指南（mac）

- 工具链：Go 1.25.5、golangci-lint 2.x；Node 25 与 pnpm 10 仅 Web UI（V0.5）需要。
- 代理：`GOPROXY=https://proxy.golang.org,direct`（goproxy.cn 2026-10-23 不可用）。
- 日常循环：`make fixtures` → `make dev` → 浏览器或 curl → `make test test-integration lint`。
- 测试构建与正式构建：`-tags faultinject` 只在 `make dev`、`make test-integration`、`make build-arm64-test` 中出现；`make build-arm64` 与 `make package` 永远不带。
- 新增任何目标写入路径时，必须经 `storage.Target` 的方法，并在 `internal/storage/target_test.go` 增加 dry-run 拒绝用例。
- 操作系统相关代码只放 `internal/platform`，其它包不得调用外部命令或使用 `syscall` 常量。
