# 20261027T0720Z V0.7 通知功能验证

- `01-device-outbound-probe.txt`：在 OEC-Turbo（172.20.31.6）上探测两个推送服务的可达性。api.chuckfang.com 直连 HTTPS 200；api.telegram.org 解析到被污染的 IPv6 地址、8 秒超时。设备无 proxy 环境变量，`action-backup.service` 无网络沙箱限制。
- `02-fake-meow-received.jsonl`：mac simulation 模式下把 `notifications.meow.base_url` 指向本机假 MeoW 服务（`fake_meow.py`，按文档返回 `{"status":200}`），通过浏览器「设置 → 通知」启用并填昵称后收到的全部请求：测试、开始备份、备份完成、相机已插入。第一行 `/x` 是启动前的连通性自检。
- `03-server-log-notify-lines.txt`：服务端日志中与通知相关的行。
- `04-config-written-back.yaml`：PATCH /config 写回配置文件后的 `notifications` 段（注释保留）。
- `05-settings-notify.png`：设置页「通知」卡片截图（发送测试后显示 `meow：已发送`）。

浏览器操作通过 chrome-devtools MCP 完成；`make test`、`make test-integration`、`make lint` 全绿。
