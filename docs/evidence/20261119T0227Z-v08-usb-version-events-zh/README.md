# V0.8：USB 协议版本显示 + 事件中文显示（mac simulation 实测）

环境：mac，`-tags faultinject` 测试构建，simulation 模式，临时配置在 `config.dev.yaml` 基础上把模拟相机设为
`link_speed_mbps: 480`（演练降速告警），监听 `127.0.0.1:18080`。触发 `POST /backups/plan`、`POST /backups/start` 产生事件。

| 文件 | 内容 |
| --- | --- |
| `01-api-devices.json` | `GET /api/v1/devices`：新增 `usb_version` / `link_speed_mbps` / `usb_link_limited` |
| `02-api-events.json` | `GET /api/v1/events`：`device.attached` 为 warn，`data` 带 USB 字段；后端消息仍是英文原文 |
| `03-ui-headless-chrome.json` | 无头 Chrome（CDP）逐页读取的页面文字：总览最近事件、设备页 USB 协议行、事件页中文 / 类型筛选 / 原文、语言选项刷新后保留 |
| `ui-devices.png` | 设备页「USB 协议」行与降速徽标（裁剪过，去掉了含本机路径的卷表） |
| `ui-events-zh.png` / `ui-events-raw.png` | 事件页中文 / 原文 |
| `ui-overview.png` | 总览页最近事件（中文；裁剪过，只留最近事件卡片） |

结论：

- 设备页显示 `USB 3.2 · 当前 480 Mb/s` 与「降速连接，请检查线材 / 接口」徽标；`device.attached` 升为 warn。
- 事件页默认中文（级别、类型、消息），可切回原文；类型下拉按中文名筛选；选择记在浏览器 localStorage，总览页跟随。
- 历史事件同样显示中文（前端按 `type + data` 生成，库里数据未动）。
- `make test`、`make test-integration`、`make lint` 全绿。

未做：

- Linux 实机核对（sysfs `speed` / `version`）：设备 `172.20.31.6` 2026-11-19 不在线，见 `02-verification-checklist.md` 问题 10。
- 浏览器核对没走 chrome-devtools MCP（其浏览器配置目录被另一个实例占用），改用独立配置目录的无头 Chrome。
