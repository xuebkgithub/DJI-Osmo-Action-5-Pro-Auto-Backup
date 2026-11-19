# V0.9：实机 N1（坏包拒绝）与 0.9.1-rc.1 → 0.9.1-rc.2 更新

生产设备，用户在 root shell 里手工执行并贴回输出。开发机连不到该设备。

| 文件 | 内容 |
| --- | --- |
| `01-n1-corrupt-package.txt` | 测试方案 N1：清单与签名是 rc.2 原件，安装包被改写 4 个字节 |
| `02-baseline-before-update.txt` | 更新前基线（14:45:48） |
| `03-after-update-rc1-to-rc2.txt` | 网页一键更新 rc.1 → rc.2 之后的检查 |
| `04-n3-n2-n4.txt` | 测试方案 N3 / N2 / N4（更新到 rc.2 之后做，素材为 rc.2 的发布文件） |

## N1 包被改写：通过

- root 侧 14:44:06 处理，`step: verify`，`error: sha256 mismatch`，没有 `rolled_back`。
- 版本仍是 rc.1；两个服务的 `ActiveEnterTimestamp` 仍是 14:01:12（没被停过）。
- 请求标记已删除，`installing` 计数 0，`update-work` 不存在。
- 更新单元以 `status=1/FAILURE` 结束（符合设计：失败时返回非零，journal 里有醒目记录），下一分钟不再处理。

这是「主服务被攻破后往更新目录放一个被改过的包」的真实演练：签名对得上的清单 + 对不上的包，root 侧在停服务之前就拒绝了。

## rc.1 → rc.2 更新：通过

| 时间（+08:00） | 事件 |
| --- | --- |
| 14:46:19 | 检查到 rc.2 |
| 14:47:01 | 下载并验签完成，写入请求 |
| 14:47:06 | 定时任务接手（等了 5 秒），停主服务 |
| 14:47:16 | 主服务退出，仍然是 10 秒 + `context deadline exceeded`（停的是 rc.1 的旧进程，预期内） |
| 14:47:22 | rc.2 启动（commit `1b5430d`），CIFS 目标就绪；两个服务的 `ActiveEnterTimestamp` 都是 14:47:22，**挂载守护确实一起重启了** |
| 14:47:22 | 先报告了一条 `update.failed`（N1 留下的旧结果，预期内） |
| 14:47:25 | root 侧健康检查通过，写结果，`ok: true`，全程 19 秒 |
| 14:47:27 | **`updated from 0.9.1-rc.1 to 0.9.1-rc.2`**：结果写入 2 秒后被新进程报告 |

确认的修复与此前的未验证项：

- **「更新完成」未报告的修复有效**（rc.2 的 `awaitResult`）：结果文件在进程启动 3 秒后才写入，仍被报告；更新目录里出现了 `reported` 文件。
- **主服务沙箱（`ProtectSystem=strict`）下读得到 `/opt/action-backup/update-result.json`**：上面两条事件都来自该文件。
- 备份：`prev/` 的 `f04` 从 198 字节变成 430 字节，是 rc.1 的定时器单元（`OnCalendar` 写法带注释），说明备份的确是当前版本的文件。
- 配置未丢：`mode: production`、`auto_backup: true`、`channel: prerelease`；NAS 挂载 1 条。
- 主服务不可用 16 秒（14:47:06 → 14:47:22），其中 10 秒来自旧进程的停机超时。

## N3 / N2 / N4：全部通过

| 用例 | 注入内容 | 处理时间 | `step` | `error` |
| --- | --- | --- | --- | --- |
| N3 重放 / 防降级 | rc.2 的三个原件（签名有效，版本不比当前高） | 14:53:06 | `verify` | `package version 0.9.1-rc.2 is not newer than installed 0.9.1-rc.2` |
| N2 坏签名 | 签名文件偏移 10 起 4 个字节清零 | 14:54:06 | `verify` | `SHA256SUMS signature verification failed` |
| N4 符号链接 | `SHA256SUMS.sig -> /etc/hostname` | 14:56:06 | `stage` | `open …/SHA256SUMS.sig: too many levels of symbolic links` |

三个用例的共同结果：

- 版本始终是 rc.2；两个服务的 `ActiveEnterTimestamp` 始终是 14:47:22（没被停过）。
- 没有 `rolled_back`；`installing` 计数 0；`update-work` 每次都被清掉；请求标记每次都被删除，只处理一次。
- N2、N4 的结果里 `to` 为空：还没走到「从清单取版本」那一步就被拒了，符合检查顺序（取文件 → 验签 → 取版本 → 比版本 → 核对包）。
- N4 的报错来自内核对 `O_NOFOLLOW` 的拒绝（ELOOP），`internal/platform/linux/update_ops_linux.go` 的这条路径首次在真实 Linux 上执行。链接目标的内容没有出现在结果或 journal 里。
- 清理后更新目录只剩 `reported`。

加上 N1，测试方案的 N 组全部通过。

## 仍未验证

- **停机超时的修复**（`stopping` 通道）：要等 rc.2 被下一个版本替换时才看得到，届时停服务应在 1 秒左右完成、journal 里不再有 `context deadline exceeded`。
- T2（等待空闲）、T3（回滚演练）未做。

## 顺带看到的

- 设备配置文件里 `mode: "production"` 这一行的行尾注释仍是「实机首轮验证：只扫描、规划，不写 NAS」，那是 dry-run 模板留下的旧注释，与当前值不符，容易误导，可以手工删掉。
- 失败的那次运行让 `action-backup-update.service` 进入 failed 状态，直到下一次成功运行；`systemctl --failed` 会列出它。
- N 组做完后结果文件里留着 N4 的失败结果（`finished_at` 06:56:06Z），而 `reported` 记的仍是 14:47 那次成功。主服务下次启动时会把它当成未报告的结果发一条「更新失败（取包）」事件，属于测试残留。
