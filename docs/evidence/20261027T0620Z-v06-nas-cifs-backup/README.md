# V0.6 真 NAS（CIFS）首次备份（2026-10-27 06:24–06:30 UTC）

用户在设备上填好 `/etc/action-backup/smb.credentials` 与 `storage.share: //172.20.100.254/home` 后，`systemctl restart action-backup`。
文件 `01-nas-backup.txt` 是命令原始输出（`/proc/mounts` 中的 SMB 用户名已脱敏）。

- 挂载由 `action-backup-mounter` 的 `mount-target` 完成：`mount.cifs` 协商到 **SMB 3.1.1**，选项 `nosuid,nodev,noexec,uid=999,gid=995,file_mode=0660,dir_mode=0770,iocharset=utf8`，
  `rsize/wsize=4 MiB`。主服务 `storage target ready kind=cifs`，NAS 共享 21 TB、剩 16 TB；`POST /storage/test` latency 4 ms。
- 相机此时只剩 2 个 MP4（用户已清理）：计划 2 upload（409,728 B `target_missing`——之前写到本地目标的那份；482,234,357 B `incomplete`——之前被取消），0 warning。
- `POST /backups/start`：小文件 0.54 s 完成；大文件 `14:25:03.643 → 14:25:23.622`，**19.98 s，24.1 MB/s**（含 4 MiB 缓冲写入、fsync 与 basic 校验）。
  链路：eth0 1000 Mb/s 全双工，USB 3.0（5000 Mb/s），4 核，负载 0.55。按此速率 81.1 GB 需约 56 分钟，未达 V1.4 §17 的 30 分钟目标，待性能测试调 `buffer_size_mb` / Worker 数并定位瓶颈（USB 读、CIFS 写还是 fsync 频率）。
- 两个文件源 / NAS SHA-256 一致：`e8cba9f5…98d57`、`90782c77…61fcf`。
- 期间用户重新插过相机：microSD 由 `/dev/sdb` 变为 `/dev/sdc`，检测器与守护自动跟上（挂载点 `source/sdc`，旧 `sdb` 目录已清），备份不受影响。
- NAS 上文件 mtime 为写入时间（同问题 8）。
