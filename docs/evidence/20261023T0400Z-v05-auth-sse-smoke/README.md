# V0.5 认证 / CSRF / SSE 冒烟测试（2026-10-23，mac，simulation + faultinject 构建）

- `curl-session.txt`：未登录全路由 401、登录 Cookie 属性、CSRF 403、`PATCH /config` 写回文件、`POST /storage/test`、SSE 实时插拔事件、第 4 次登录 429、注销后 401、安全事件入库。第 1 节首次因 zsh 不拆分变量而误发请求（400），文末有重跑结果。
- `login-headers.txt`：登录响应头（会话 ID 已截断）。
- `sse.txt`、`sse-headers.txt`：6 秒内收到的 SSE 原始流与响应头。
- `service.log`：服务日志（路径已脱敏）。
- `ui-browser-check.md`、`ui-overview.png`、`ui-events.png`：Chrome（chrome-devtools-mcp）逐页核对记录与截图：登录、SSE 实时进度、暂停 / 恢复 / 取消、设置写回、存储测试、测试面板、退出。
- `go-test-lint.txt`：`go test`（单元 / faultinject / integration / recovery）、`golangci-lint`、`pnpm build` 全部输出。

配置：`configs/config.dev.yaml` 副本，`server.auth.enabled: true`，口令由 `action-backup passwd --stdin` 写入。
