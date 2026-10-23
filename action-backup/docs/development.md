# 开发指南（mac）

- 工具链：Go 1.25.5、golangci-lint 2.x；Node 25 与 pnpm 10 仅改前端时需要（`web/dist` 已入库，纯 Go 构建不依赖 Node）。
- 代理：`GOPROXY=https://proxy.golang.org,direct`（goproxy.cn 2026-10-23 不可用）。
- 日常循环：`make fixtures` → `make dev` → 浏览器 `http://127.0.0.1:8080` 或 curl → `make test test-integration lint`。
- 改前端：终端二 `make web-dev`，访问 Vite 地址（/api 代理到 8080）；完成后 `make web-build` 并把 `web/dist` 一起提交，否则嵌入的页面不会更新。
- 测认证：`go build -tags faultinject -o dist/action-backup-dev ./cmd/action-backup`，复制 `configs/config.dev.yaml` 把 `server.auth.enabled` 改 true，`dist/action-backup-dev passwd --config <副本>` 设口令后启动。
- API 调用约定：登录拿到 Cookie 与 `csrf_token`（`/api/v1/auth/me` 也返回），POST / PATCH / DELETE 必须带 `X-CSRF-Token`；SSE `GET /api/v1/events/stream` 不经 30 秒超时包装，其它接口都经。
- 测试构建与正式构建：`-tags faultinject` 只在 `make dev`、`make test-integration`、`make build-arm64-test` 中出现；`make build-arm64` 与 `make package` 永远不带。
- 新增任何目标写入路径时，必须经 `storage.Target` 的方法，并在 `internal/storage/target_test.go` 增加 dry-run 拒绝用例。
- 操作系统相关代码只放 `internal/platform`，其它包不得调用外部命令或使用 `syscall` 常量。
- 新增 API 路由时必须加入 `internal/app/auth_test.go` 的 `protectedRoutes`，保证未登录 401 全覆盖；需要免认证的路径只能加到 `auth.DefaultPolicy()`。
- `PATCH /config` 只接受 `internal/config/patch.go` 白名单；新增项要同时实现 `Apply`、运行时应用（`internal/app/configstore.go`）和测试。
