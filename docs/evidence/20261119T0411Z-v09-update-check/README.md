# V0.9：新版本检查 + 页面提示（软件更新第一阶段，mac 实测）

环境：mac，`-tags faultinject` 测试构建并用 `-ldflags -X .../version.Version=0.7.0` 把版本标成 0.7.0，simulation 模式，
临时配置在 `config.dev.yaml` 基础上改监听 `127.0.0.1:18080`、数据库与目标目录指向临时目录。
更新源是真实的 GitHub 仓库（当时最新发布为 v0.8.0），没有用假服务。

| 文件 | 内容 |
| --- | --- |
| `01-api-update-status.json` | `GET /api/v1/system/update`：启动 30 秒后的定期检查完成后的状态，`next_check_at` 排在 12 小时后 |
| `02-api-events.json` | `GET /api/v1/events`：`update.available` 只有一条（手动检查与随后的定期检查没有重复发） |
| `03-api-patch-channel.json` | `PATCH /config` 把 `update.channel` 改为 `prerelease` 成功；改 `update.api_base_url` 被白名单拒绝（400 `BAD_PATCH`） |
| `ui-topbar-badge.png` | 顶栏版本号旁的「有新版本 v0.8.0」徽标（裁剪过） |
| `ui-settings-update-card.png` | 「设置 → 软件更新」卡片（裁剪过，去掉了含本机路径的其它卡片） |

结论：

- 标成 0.7.0 的实例查到 v0.8.0 与本平台安装包 `action-backup-0.8.0-darwin-arm64.tar.gz`，`update_available: true`。
- 启动后 30 秒自动检查一次，下一次排在 12 小时后；同一版本只发一条 `update.available` 事件。
- 顶栏徽标、设置页卡片（结论行、版本、发布时间、安装包链接、发布说明、三个配置项）、通知设置里的「有新版本」开关显示正常；
  最近事件里显示中文「发现新版本 0.8.0，当前 0.7.0」。浏览器核对走 chrome-devtools MCP。
- `make test`、`make test-integration`、`make lint` 全绿。

未做：

- Linux 实机核对：设备 `172.20.31.6` 2026-11-19 SSH 超时。设备到 `api.github.com` 是否可达没有测，见 `02-verification-checklist.md` 问题 11。
- 推送渠道没有实发（开发配置里通知关闭）；`update.available` → 消息的格式由 `internal/notify` 单元测试覆盖。
- 检查失败时的页面显示没有在浏览器里看，由接口与单元测试覆盖（502 + `last_error`）。
- 下载、验签、安装、重启、回滚属于第二阶段，未实现；设计见架构文档 V1.6 第 19 章。
