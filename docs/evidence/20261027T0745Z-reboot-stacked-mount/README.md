# 2026-10-27 07:45Z 开机后 NAS 误判为 "cifs mounted from /dev/mmcblk0p7"

复现步骤（用户）：拔相机 → 重启设备 → 开机 → 插相机 → 网页健康 `degraded`，两个任务 `interrupted`。

- `01-device-after-reboot.txt`：宿主与主服务沙箱两个视角的 mountinfo、单元文件、两个单元的本次开机日志（SMB 用户名已脱敏）。

结论见 `docs/03-decisions.md`。关键观察：宿主只有一条 `/mnt/action-backup-nas`（cifs），主服务沙箱里有三条——
`ReadWritePaths=/mnt/action-backup-nas` 在守护挂 NAS 之前（服务 15:39:56 启动，守护 15:39:57 挂载）把本地 ext4 目录绑定进沙箱，
CIFS 随后以 slave 传播叠在其上。`statfs` 报 cifs，`mountEntryIn` 取第一条匹配得到 `/dev/mmcblk0p7`，拼出矛盾的错误。
- `02-option-b-readonly-failure.txt`：去掉 `ReadWritePaths=/mnt/action-backup-nas` 后重启，沙箱内 CIFS 行变 `ro`，探测仍 ready，任务 `mkdir` 报 read-only file system → 方案 B 否决，单元恢复该行。

最终部署（15:57 与 16:05 两次 `make deploy RESTART=1`）：`storage target ready`，沙箱内 `/mnt/action-backup-nas` 为 `rw` cifs；
网页总览页新增 NAS 挂载 / 重新挂载 / 卸载、重新检测设备、重试失败任务按钮（mac simulation 下用 chrome-devtools 核对渲染与「重新检测设备」点击）。
按用户原步骤（拔相机 → 重启 → 插相机）的复测由用户执行。

