# V0.9：实机更新 0.9.0 → 0.9.1-rc.1（测试方案 T1，用户手工执行）

设备：生产设备，`mode: production`，`storage.type: cifs`。开发机连不到该设备，本目录只有用户贴回的主服务 journal（`01-journal-action-backup.txt`）。
用户结论：手工测试可以正常更新。

## 从日志能确认的

| 时间（+08:00） | 事件 |
| --- | --- |
| 13:59:31 | 主服务检查到 0.9.1-rc.1（设备到 GitHub API 可达，核对清单问题 11） |
| 13:59:46 | 下载并验签完成，写入安装请求（设备能下载 Release 资产；下载 + 验签约 15 秒内完成） |
| 14:00:56 | 更新单元接手，systemd 停主服务（请求后 70 秒） |
| 14:01:06 | 主服务退出，**用了 10 秒，退出码 1** |
| 14:01:12 | 新进程启动，版本 `0.9.1-rc.1`，`faultinject=false`，迁移 0 条 |
| 14:01:12 | 通知渠道、认证、NAS 挂载（cifs）、HTTP 监听全部正常，配置未丢 |
| 14:02:59 | 用户重新登录（会话因重启失效，符合预期） |

主服务不可用时长：14:00:56 → 14:01:12，共 16 秒，其中 10 秒是下面第 1 个问题造成的。

## 日志暴露的两个问题（已修，待发布）

1. **停服务等满 10 秒并以失败退出**（`error: context deadline exceeded`，`status=1/FAILURE`）。
   原因：浏览器开着页面时有一条 SSE 长连接，`http.Server.Shutdown` 等它结束，10 秒超时后 `Run` 返回错误。更新时页面必然开着（遮罩在轮询），所以每次更新都会触发。
   修复：停机时先通知 SSE 处理器结束；`Shutdown` 仍超时则强制关闭连接并正常退出。测试 `TestShutdownWithOpenEventStream`。
2. **新进程没有报告「更新完成」**：14:01:12 之后没有 `updated from 0.9.0 to 0.9.1-rc.1` 这一行。
   原因：root 侧是先重启主服务、健康检查通过之后才写结果文件，新进程只在启动那一刻读一次，读不到。结果要等下一次重启才会被报告。
   修复：启动后 10 分钟内每 5 秒查看结果文件，出现新结果即报告。测试 `TestResultWrittenAfterStartupIsReported`。
   mac 上的演示是先写结果再启动的，所以当时没有发现。

## 结果文件与更新单元 journal（`02-device-result-and-update-journal.txt`，用户随后补贴）

| 核对项 | 结果 |
| --- | --- |
| 结果文件 | `ok: true`，`0.9.0 → 0.9.1-rc.1`，root 侧全程 19 秒（06:00:56Z → 06:01:15Z） |
| `install.sh` | 包内 9 个文件 `sha256sum -c` 全部成功 |
| 健康检查 | 主服务 14:01:12 启动，14:01:15 判定健康并写结果，约 3 秒 |
| 更新单元 | `self-update finished ok=true`，`Deactivated successfully`，CPU 1.454 s |
| 备份 | `/opt/action-backup/prev/` 有 `f00`–`f06` 与 `manifest.json`（二进制、4 个单元、udev 规则、数据库 344 KB；`-wal` / `-shm` 当时不存在），没有 `installing` 标记 |
| 清理 | 更新目录为空（请求标记、安装包、清单、签名都已删除）；`update-work` 不存在 |
| 定时器 | 更新前（v0.9.0，`OnUnitActiveSec=1min`）13:55:16、13:56:26、13:57:33、13:58:36、13:59:46 各触发一次；更新后（`OnCalendar`）每分钟整点后几秒触发一次。**两种写法都持续触发** |
| 无请求时 | 每次触发 systemd 记一行 `was skipped because no trigger condition checks were met`，不启动进程 |

由此得出：

- 测试方案 T1 的设备侧标准全部满足。
- **之前对 v0.9.0 定时器「可能只触发一次」的怀疑不成立**：实测它每 60–70 秒触发一次。改成 `OnCalendar` 不是必须的，但已随 rc.1 上线且工作正常（间隔更准），保留。
- 请求写入的时刻（13:59:46.495）恰好与一次触发（13:59:46）撞上，那次被判为无请求而跳过，所以等到 14:00:56 才被接手：最坏情况就是这样多等一个周期。
- journal 噪声实测为每分钟一行，一天约 1440 行。命令里 `grep "unmet condition"` 没匹配上，是因为这个 systemd 版本的措辞是 `no trigger condition checks were met`。
- 更新目录里没有 `reported` 文件，与上文问题 2 一致：这次的结果尚未被主服务报告。
- `/opt/action-backup/` 下的 `action-backup-0.9.0-linux-arm64/` 与同名 `.tar.gz` 是手工部署 0.9.0 时留下的，与更新无关，可以删。

## 仍未做的

- N 组（坏输入拒绝）、T2（等待空闲）、T3（回滚演练）。
- 挂载守护是否一起重启没有直接证据（主服务启动时 CIFS 目标立即可用，间接说明守护正常）。
