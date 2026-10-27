# V0.6 Linux 后端实机验证（2026-10-27 05:30–06:10 UTC，OEC-Turbo 172.20.31.6）

相机 DJI Osmo Action 5 Pro 经 USB 接在设备上。本次实现并验证：sysfs + udev 数据库检测、root 挂载守护只读挂载、
拔插闭环、production 模式下一次真实复制（目标为设备本地目录，NAS 不可达）。

构建 `v0.6.0-2-g86d1168-dirty`（HEAD 之后的 V0.6 改动未提交），production 构建，`faultinject=false`。

| 文件 | 内容 |
| --- | --- |
| `01-camera-sysfs-udev.txt` | lsusb / lsblk、USB sysfs 属性、SCSI LUN → 块设备、`/run/udev/data/b8:*` 属性、非 root 可读性、exfat 模块 |
| `02-units-mounts-config.txt` | 两个 systemd 单元状态与内容、socket 权限、挂载表、配置关键字段、版本 |
| `03-journal.txt` | 守护与主服务的日志（含首次 0 文件、85.8 GB 计划、取消、小文件完成、拔插） |
| `04-copy-verify.txt` | 源 / 目标 SHA-256、stat、目标目录、相机 MP4 清单、磁盘余量 |

## 实测事实

- USB `6-1`：`idVendor=0506`、`idProduct=8002`、`serial=123456789ABCDEF`（占位）、`product=OsmoAction5pro_SN:0A1B2C3D`、`manufacturer=DJI`。
  序列号从 `product` 的 `SN:` 解析得 `0A1B2C3D`，与 mac 实测一致。
- usb-storage 暴露两个 LUN：`1:0:0:0 → sda`（48 GB，exfat，LABEL `OsmoAction`，UUID `F59D-F69A`）、`1:0:0:1 → sdb`（116.1 GB，exfat，无卷标，UUID `0123-4567`）。
  两卷都是整盘 exFAT，无分区。LUN 0 = 内置、LUN 1 = microSD，与 mac 实测一致。
- `/run/udev/data/b8:0`、`b8:16` 世界可读，含 `ID_FS_TYPE / ID_FS_UUID / ID_FS_LABEL / ID_VENDOR_ID / ID_MODEL_ID` 以及我们 udev 规则写入的 `UDISKS_IGNORE=1`。
  `/dev/sda` 对非 root 不可读（`root:disk 0660`），所以检测器不碰设备节点，只读 sysfs 与 udev 数据库。
- `exfat.ko` 存在、默认未加载；root 守护 `mount(2)` 时内核自动加载。
- exFAT 时间戳：源文件 mtime 读出为 `2026-10-25 12:30:28.26 +0800`，与 mac 实测的 `+0800` 一致（Linux exfat 驱动按卷内 UTC 偏移换算）。

## 过程与问题

1. 首次部署后 `device DJI OsmoAction5pro attached with 2 volume(s)`，但计划 0 文件，warning
   `volume uuid:f59d-f69a has no device path`：`syncengine` / `scheduler` 构造 `RawVolume` 时没带 `DevicePath`。
   给 `domain.Volume` 加 `DevicePath` 并透传后修复。
2. 修复后守护日志 `source mounted read-only`，但宿主 `/proc/mounts` 看不到、目录为空：守护单元带了 `ProtectHome=true`、`PrivateTmp=true`，
   systemd 因此给它私有挂载命名空间，挂载被困在守护自己的命名空间里。去掉这两项后正常；单元内已加注释。
   顺带发现 `deploy.sh RESTART=1` 只重启主服务，守护单元 / 二进制更新后不会重启——已改为两者一起重启。
3. 切 production + 本地目标后，自动备份把 **10 个 MP4 共 85.8 GB** 全部入队并开始传 5 GB 文件，而 eMMC 仅余 5.1 GB。
   立即 `systemctl stop`，删掉 1.29 GB 的 `.part`，`device.auto_backup` 改 false，重启后 `POST /backups/pause`，
   取消任务 1、2、4–10，`resume`，reconcile 在 30 秒内把任务 3（小文件 `DJI_20261025123025_0022_D.MP4`，409,728 B）重新入队并完成。
4. 源 / 目标 SHA-256 均为 `e8cba9f582869fb6025dc2daff634c4c2d06af9f5f160ab2760d619e50f98d57`。目标 mtime 为写入时间（未保留源 mtime），记入待确认问题。
5. 拔插：`echo 0 > /sys/bus/usb/devices/6-1/authorized` 模拟拔出 → `device.detached` → 守护卸载 sda / sdb；`echo 1` 插回 → 8 秒内 `device.attached`；
   `POST /backups/plan` 重新只读挂载两卷（`ro,nosuid,nodev,noexec,uid=999,gid=995,fmask=0077,dmask=0077`），计划 1 skip（already_backed_up）、9 upload、0 warning。

## 当前设备状态

`mode: production`、`storage.type: local`（`local_root: /var/lib/action-backup/local-target`，`allow_local_destination: true`）、
`device.auto_backup: false`（本地盘放不下全量，NAS 可用并切回 cifs 后再开）、认证开启。
两个单元均 active；`/etc/action-backup/config.yaml.bak-cifs` 是切换前的 cifs 配置备份。
