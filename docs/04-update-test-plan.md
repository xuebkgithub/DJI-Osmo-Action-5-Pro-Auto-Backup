# 软件更新实机测试方案

目的：在设备 `172.20.31.6`（已部署 v0.9.0）上确认「网页一键更新」整条链路可用，并确认坏包会被拒绝、失败会回滚。
设计依据见 `Action_Backup_软件架构设计_V1.6.md` 第 19 章；本方案对应核对清单问题 11、13。

## 测试前已知

- v0.9.0 的 Release 带 `SHA256SUMS.sig`（64 字节），2026-11-19 在 mac 上用仓库公钥 `openssl pkeyutl -verify` 通过：真实私钥的签名链路是通的。
- 设备侧一项都没有验证过。2026-11-19 从开发机 SSH 到设备仍然超时，T0 的探查没有做成。
- v0.9.0 的更新定时器写的是 `OnBootSec=2min` + `OnUnitActiveSec=1min`。后者相对「服务上次激活的时间」计时，而没有请求时服务被条件跳过、从未激活，**定时器可能只在开机后触发一次就不再触发**。这一点没有实测过，T0.2 专门核对。v0.9.1-rc.1 已改成 `OnCalendar=*:*:00`。
  如果 T0.2 证实 v0.9.0 的定时器不重复触发：N 组与 T1 里「等一分钟」的地方改为手动 `systemctl start action-backup-update.service`（写入类操作，先确认）；设备升到 rc.1 之后定时器即恢复正常，rc.1 → rc.2 那一次才是对「全自动」的完整验证。
- v0.9.0 有一个已知缺陷：root 侧在停服务之前就拒绝安装时，页面会一直停在「已提交安装请求」，要重启服务才恢复。修复在 v0.9.1-rc.1 里（`watchResult`）。本方案的 N 组用例是手工注入请求，不经过页面，不受影响。

## 用什么版本测

更新需要一个比 0.9.0 高的版本。用**预发布版**，不动正式通道：

| 版本 | 用途 | 说明 |
| --- | --- | --- |
| `v0.9.1-rc.1` | T1 正常更新；N1、N2 的素材 | 发布流水线会把带 `-` 的 tag 标成 prerelease；`stable` 通道的设备看不到它 |
| `v0.9.1-rc.2` | T3 回滚演练（可选） | 同一份代码再打一个 tag 即可 |

测试设备临时把通道切到 `prerelease`（网页「设置 → 软件更新」），测完切回 `stable`。之后发 `v0.9.1` 正式版时，`0.9.1 > 0.9.1-rc.N`，设备会正常提示更新。

发布测试版（由用户执行，Claude 不主动打 tag、不推送）：

```bash
git tag -a v0.9.1-rc.1 -m "action-backup 0.9.1-rc.1" && git push origin main v0.9.1-rc.1
```

## 执行约定

- 设备命令一律 `~/MyMac/scripts/cmd_ip.sh 172.20.31.6 '<命令>'`。
- T0 是只读的。N 组、T1、T3 会在设备上写文件或触发安装，执行前逐项向用户确认。
- 每组做完把命令原始输出存进 `docs/evidence/<UTC时间>-v09-update-device/`，结论写进 `03-decisions.md`。`/proc/mounts` 之类含 SMB 用户名的输出要脱敏。
- 下文 `$D` = `/var/lib/action-backup/update`，`$R` = `https://github.com/xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup/releases/download`。

通用的结果查看命令（只读，下文称「查看结果」）：

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'cat /opt/action-backup/update-result.json; echo; /opt/action-backup/action-backup --version; systemctl is-active action-backup action-backup-mounter; ls /opt/action-backup/prev 2>&1 | head; ls -la /var/lib/action-backup/update; journalctl -u action-backup-update --since -10min --no-pager -o short-iso | tail -30'
```

## T0 前置检查（只读）

| # | 检查 | 命令 | 通过标准 |
| --- | --- | --- | --- |
| T0.1 | 版本 | `/opt/action-backup/action-backup --version` | `0.9.0`，`faultinject=false` |
| T0.2 | 定时任务是否每分钟触发 | `systemctl is-enabled action-backup-update.timer; systemctl cat action-backup-update.timer; systemctl list-timers action-backup-update.timer --no-pager; systemctl show action-backup-update.service -p ConditionTimestamp`，隔 2 分钟再执行一次后两条 | `enabled`；`NEXT` 列有值且在 1 分钟内；两次的 `ConditionTimestamp` 不同（确实又触发了）。`NEXT` 为 `n/a` 或时间戳不变 → v0.9.0 的定时器不重复触发，按「测试前已知」里的办法处理 |
| T0.3 | 无请求时不启动进程 | `systemctl show action-backup-update.service -p ConditionResult; journalctl -u action-backup-update -n 5 --no-pager` | `ConditionResult=no`；journal 里只有 systemd 的「skipped because of an unmet condition」（每次触发一行，属正常），没有 `self-update` 自己的输出。顺便记下这类行一天有多少，评估是否需要压制 |
| T0.4 | 设备到 GitHub | `curl -sSL -o /dev/null -m 20 -w "%{http_code} %{time_total}s\n" "https://api.github.com/repos/xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup/releases?per_page=1"`，再对 `$R/v0.9.0/action-backup-0.9.0-linux-arm64.tar.gz` 做同样的请求 | 都是 200。这是核对清单问题 11；不通则先解决网络（单元里设 `HTTPS_PROXY`），后面的 T1 做不了 |
| T0.5 | 主服务自己的检查 | 网页「设置 → 软件更新」；`journalctl -u action-backup --since -13h --no-pager \| grep -i update` | 「上次检查」有时间、无「检查失败」；显示「已是最新版本 v0.9.0」 |
| T0.6 | 页面认为可以安装 | 浏览器登录后访问 `/api/v1/system/update` | `install_supported: true`、`dev_build: false`。为 false 时对照架构 §19.6.6 的四个条件排查 |
| T0.7 | 沙箱内读得到结果文件 | T1 之后看页面「上次安装」一行 | 能显示即证明 `ProtectSystem=strict` 下可读 `/opt/action-backup/update-result.json` |

T0.4 或 T0.6 不过就停下，不做后面的用例。

## N 组：坏输入必须被拒绝，且系统不被改动

手工在更新目录里放文件并写请求标记，模拟「主服务被攻破后伪造请求」。每个用例做完都用「查看结果」确认，并等下一分钟确认没有重复处理。
每个用例的共同通过标准：

- `update-result.json` 里 `ok: false`，`step` 与 `error` 符合预期，`rolled_back` 不出现；
- 版本仍是 0.9.0，两个服务一直 active（**没有被停过**：`systemctl show action-backup -p ActiveEnterTimestamp` 前后不变）；
- `/opt/action-backup/prev` 不存在或没有 `installing` 文件；
- `$D/request` 已被删除，下一分钟不再处理。

准备目录（N1 之前执行一次）：

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'install -d -o actionbackup -g actionbackup -m 0750 /var/lib/action-backup/update'
```

### N3 重放旧包 / 防降级（不依赖测试版，可以最先做）

把 v0.9.0 自己的三个文件放进去：签名是真的，但版本不比当前高。

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'cd /var/lib/action-backup/update && R=https://github.com/xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup/releases/download/v0.9.0 && curl -fsSLO $R/SHA256SUMS && curl -fsSLO $R/SHA256SUMS.sig && curl -fsSLO $R/action-backup-0.9.0-linux-arm64.tar.gz && touch request && date'
```

预期：`step: verify`，`error` 含 `is not newer than installed`。

### N1 包被改过一个字节（需要 v0.9.1-rc.1 已发布）

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'cd /var/lib/action-backup/update && rm -f * && R=https://github.com/xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup/releases/download/v0.9.1-rc.1 && curl -fsSLO $R/SHA256SUMS && curl -fsSLO $R/SHA256SUMS.sig && curl -fsSLO $R/action-backup-0.9.1-rc.1-linux-arm64.tar.gz && printf "\377" | dd of=action-backup-0.9.1-rc.1-linux-arm64.tar.gz bs=1 seek=4096 conv=notrunc status=none && touch request && date'
```

预期：`step: verify`，`error` 含 `sha256 mismatch`。

### N2 签名被改

在 N1 的基础上换回好包、改坏签名：

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'cd /var/lib/action-backup/update && R=https://github.com/xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup/releases/download/v0.9.1-rc.1 && curl -fsSLO $R/action-backup-0.9.1-rc.1-linux-arm64.tar.gz && printf "\377" | dd of=SHA256SUMS.sig bs=1 seek=10 conv=notrunc status=none && touch request && date'
```

预期：`step: verify`，`error` 含 `signature verification failed`。

### N4 符号链接

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'cd /var/lib/action-backup/update && rm -f SHA256SUMS.sig && ln -s /etc/action-backup/smb.credentials SHA256SUMS.sig && touch request && date'
```

预期：`step: stage`，`error` 含 `open` 与 `too many levels of symbolic links`（`O_NOFOLLOW` 的报错）。结果文件与 journal 里**不得**出现凭据文件的内容。

N 组做完清理：`~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'rm -f /var/lib/action-backup/update/*'`。

## T1 正常更新（核心用例）

前置：T0 全过，N 组做完并清理，v0.9.1-rc.1 已发布，相机没有在备份。

1. 网页「设置 → 软件更新」把「更新通道」改成 `prerelease` 并保存，点「立即检查」。
   预期：结论行「有新版本 v0.9.1-rc.1（预发布）」，顶栏出现徽标，出现「立即更新到 v0.9.1-rc.1」按钮。
2. 记下时间，点「立即更新」，确认弹窗。
   预期：依次看到「正在下载安装包」（进度条在动）→「正在校验」→ 全屏遮罩「正在更新到 v0.9.1-rc.1」。
3. 等待。预期最多约 1 分钟后服务重启，页面自动刷新（开了认证会回到登录页）。
4. 重新登录后核对页面：
   - 顶栏版本 `v0.9.1-rc.1`，徽标消失；
   - 「软件更新」卡片：「已是最新版本」，「上次安装：v0.9.0 → v0.9.1-rc.1 成功」；
   - 事件页有「更新已就绪」「更新完成」两条。
5. 「查看结果」核对设备侧。

通过标准：

| 项 | 标准 |
| --- | --- |
| 结果文件 | `ok: true`，`from: 0.9.0`，`to: 0.9.1-rc.1` |
| 二进制 | `--version` 输出 `0.9.1-rc.1`，`faultinject=false` |
| 服务 | `action-backup`、`action-backup-mounter` 都 active，`ActiveEnterTimestamp` 是刚才的时间（守护也重启了） |
| 清理 | `$D` 里没有 `request`、安装包、`SHA256SUMS*`；`/opt/action-backup/update-work` 不存在；`/opt/action-backup/prev/` 有 `manifest.json`、没有 `installing` |
| journal | `action-backup-update` 有一条 `self-update finished` 且 `ok=true`；无 error |
| 功能回归 | NAS 仍挂载（总览页健康为 ok）；配置未丢（`mode: production`、`auto_backup`、通知设置都在）；`/etc/action-backup/smb.credentials` 没被动过；拔插一次相机能正常开始备份 |
| 耗时 | 记录从点击到页面恢复的总时长，以及服务不可用的时长（journal 里 stop 到 health 通过） |

T1 不过时：先「查看结果」保留现场，把 `update-result.json` 与 `journalctl -u action-backup-update` 全文存证，再决定是否手动 `make deploy RESTART=1` 恢复。

## T2 有备份在跑时不打断（可选）

需要相机里有一个几 GB 的未备份文件，以及另一个更高的测试版（可与 T3 共用 rc.2）。

1. 开始备份，确认总览页有任务在传输。
2. 点「立即更新」。预期：下载完成后停在「安装包已就绪，等待备份任务结束后安装」，不出现遮罩；`$D/request` 不存在。
3. 备份结束后预期自动进入「已提交安装请求」并完成更新，备份的文件 SHA-256 正确、任务状态为 COMPLETED。

## T3 回滚演练（可选，会造成数分钟停机）

目的：确认新版本起不来时能回到旧版本，数据库也回到安装前。这是唯一会让生产服务停几分钟的用例，**做之前必须单独征得用户同意**，并选相机不在备份的时间。

需要 v0.9.1-rc.2 已发布，设备当前为 rc.1。办法是用一个 systemd drop-in 让主服务启动必然失败，drop-in 不在回滚清单里，所以能同时演练「回滚后仍不健康 → 保留现场 → 下次触发再恢复」。

1. 放置 drop-in，**不要** `daemon-reload`（安装过程自己会 reload，此刻运行中的服务不受影响）：

   ```bash
   ~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'mkdir -p /etc/systemd/system/action-backup.service.d && printf "[Service]\nExecStartPre=/bin/false\n" > /etc/systemd/system/action-backup.service.d/zz-update-test.conf'
   ```

2. 记下数据库现状供事后比对：`sqlite3` 不一定有，改记文件哈希：`sha256sum /var/lib/action-backup/backup.db`（主服务运行中哈希会变，以 `prev/` 里的备份为准，见第 5 步）。
3. 网页点「立即更新到 v0.9.1-rc.2」。预期遮罩出现，随后页面长时间打不开。
4. 约 3 至 4 分钟后「查看结果」。预期：
   - journal 里第一次的结果是 `ok=false`，`step` 为 `restart` 或 `health`，`rolled_back=true`，`rollback_error` 非空（因为 drop-in 还在，回滚后服务同样起不来）；
   - 此后每分钟定时任务会因 `installing` 标记再回滚一次，所以 `update-result.json` 里看到的可能已经是 `step: recover` 且 `rollback_error` 非空，这是预期行为。每次都会重启挂载守护；drop-in 留得越久折腾越多，第 5 步尽快做；
   - `/opt/action-backup/action-backup --version` 已是 **0.9.1-rc.1**（文件已恢复）；
   - `/opt/action-backup/prev/installing` **仍然存在**（现场保留）。
5. 移除 drop-in：

   ```bash
   ~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'rm -f /etc/systemd/system/action-backup.service.d/zz-update-test.conf && rmdir /etc/systemd/system/action-backup.service.d && systemctl daemon-reload'
   ```

   **不要**手动启动服务。主服务单元带 `Restart=on-failure`，drop-in 去掉后几秒内会自己起来；随后一分钟内更新单元因 `prev/installing` 存在而运行，再回滚一次并确认健康。
6. 「查看结果」。预期：新的结果 `step: recover`、`rolled_back: true`、`rollback_error` 为空；`installing` 已删除；两个服务 active；版本 0.9.1-rc.1。
7. 页面恢复后核对：「上次安装」显示失败并已回滚；事件页有「更新失败」；任务历史、设备记录与演练前一致（数据库已恢复）。
8. 再点一次「立即更新到 v0.9.1-rc.2」，确认去掉 drop-in 后能正常更新成功，设备最终停在 rc.2。

兜底：第 5 步之后 5 分钟服务仍未恢复，手动 `systemctl start action-backup`；仍不行就 `make deploy RESTART=1` 重装，并把 `prev/`、结果文件、journal 全部存证。

## 收尾

- 通道切回 `stable`。设备会停在 rc 版本，直到 `v0.9.1` 正式发布。
- 确认 `/var/lib/action-backup/update/` 里没有残留的测试文件，`/etc/systemd/system/action-backup.service.d/` 不存在。
- 证据归档，结论写进 `03-decisions.md`，核对清单问题 11、13 改为已确认或记录失败原因，架构文档 §19.6.9 的「没有验证」清单相应更新。

## 没有覆盖的

- 安装过程中真实断电：只有单元测试覆盖（`installing` 标记 + 下次触发回滚），实机不做。
- `install.sh` 自身失败：需要一个故意做坏的签名包，不值得为此发布；单元测试覆盖。
- 第三阶段的自动安装尚未实现，不在本方案内。

## 执行记录

| 日期 | 用例 | 结果 | 证据 / 说明 |
| --- | --- | --- | --- |
| 2026-11-19 | T0.4 设备到 GitHub | 通过 | 主服务检查到 rc.1 并下载成功，间接确认；耗时未单独测 |
| 2026-11-19 | T1 正常更新 0.9.0 → 0.9.1-rc.1 | 通过（用户手工执行） | `evidence/20261119T0600Z-v09-update-device-t1/`。主服务 journal、结果文件（`ok: true`，root 侧 19 秒）、更新单元 journal、`prev/` 备份、更新目录清理均已核对，符合通过标准；功能回归里的「拔插相机能正常备份」未单独记录 |
| 2026-11-19 | T0.2 定时器持续触发 | 通过 | 同上证据：v0.9.0（`OnUnitActiveSec`）每 60–70 秒一次，rc.1（`OnCalendar`）每分钟一次。「测试前已知」里对 v0.9.0 定时器的担心不成立 |
| 2026-11-19 | T0.3 无请求时不启动进程 | 通过 | 每次触发只有 systemd 的一行 `skipped because no trigger condition checks were met`（措辞与方案里写的不同），约 1440 行 / 天 |
| 2026-11-19 | T1 发现的问题 | 已修，待发布 | 停服务等满 10 秒且退出码 1（SSE 长连接）；新进程没有报告「更新完成」（结果文件在其启动之后才写） |
| - | N1–N4 坏输入拒绝 | 未做 | |
| - | T2 等待空闲、T3 回滚演练 | 未做 | 可选 |

下一个测试版（rc.2）除了验证上面两处修复，也是第一次「定时器为 `OnCalendar` 的版本自己更新自己」：设备现在是 rc.1，定时器已是新写法。
rc.1 → rc.2 时额外核对：

- 停服务到新进程启动的间隔应降到几秒，journal 里不再有 `context deadline exceeded` 与 `Failed with result`；
- 新进程启动后约 5 至 30 秒内出现 `updated from 0.9.1-rc.1 to 0.9.1-rc.2`（事件页「更新完成」），「上次安装」一行显示成功；
- 注意：rc.1 的主服务本身仍带着这两个问题，所以**停 rc.1 时仍会慢 10 秒**，要到 rc.2 → 下一版才能看到停机变快；「更新完成」事件由新进程（rc.2）报告，这一次就能看到。
