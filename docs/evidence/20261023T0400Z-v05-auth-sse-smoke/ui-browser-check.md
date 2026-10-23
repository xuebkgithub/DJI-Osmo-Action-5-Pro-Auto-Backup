# Web UI 浏览器核对（2026-10-23，Chrome via chrome-devtools-mcp，隔离上下文）

服务：测试构建 + `server.auth.enabled: true` 的 simulation，`http://127.0.0.1:18080`。

| 步骤 | 结果 |
| --- | --- |
| 打开 `/` | 渲染登录页；控制台仅有预期的 `/auth/me` 401 与 favicon 404，无 JS 错误 |
| 错误口令登录 | 显示"用户名或密码错误"，密码框清空 |
| 正确口令登录 | 顶部 SIMULATION 横幅、"实时连接"（SSE 已建立）、总览四卡片与最近事件全部填充（`ui-overview.png`） |
| 总览"开始备份" | 4 个 8 MB 文件入队并完成，事件经 SSE 实时追加，任务计数 COMPLETED=4 |
| 测试面板 | 模拟设备显示"已插入"；点预设 `slow-nas` 自动填表，"添加规则"后表格显示规则与调用 / 命中计数 |
| 计划页"扫描并开始备份" | 新增 24 MB 文件 1 upload / 4 skip；提示"已入队：新建 1"，自动切到任务页 |
| 任务页 | 任务 #5 TRANSFERRING，进度条与速度每秒随 `backup.progress` 更新（33.3% → 83.3%） |
| 总览"暂停" | 任务回到 QUEUED，事件 `task.paused … at 12582912 bytes`，"恢复"按钮启用 |
| 总览"恢复" | 从 50%（12 MB 断点）继续传输 |
| 任务页"取消" | 状态变为 CANCELLED，操作按钮消失 |
| 设置页保存 | `verify_mode=sha256`、`retry_count=3`、`logging.level=debug` 写回配置文件并回显；事件 `config.changed` |
| 设置页"测试连接" | 结果"可用"，文件系统 local，剩余空间、根目录存在、允许写入 |
| 设备与卷 | 序列号、USB ID、设备键、双卷（LUN 0 internal / LUN 1 sdcard）表格 |
| 事件页 | 按时间倒序，级别徽章，详情可展开（`ui-events.png`） |
| 退出 | 回到登录页，之后 API 401 |

核对后修复：设备名标题不再被 CSS 转为大写；登录表单 label 关联 input，设置表单控件加 name。
