# mount-helper（V0.6，已实现）

`action-backup mount-helper` 是同一二进制的子命令，由 `action-backup-mounter.service` 以 root 运行，
监听 `/run/action-backup-mounter/helper.sock`（root:actionbackup 0660）。主服务以 `actionbackup`
身份连接，一次连接一个 JSON 请求：

| op | 参数 | 行为 |
| --- | --- | --- |
| `mount-source` | `device: /dev/sdX` | 校验 sdX 属于配置 VID/PID 的相机、文件系统为 exfat/vfat，以 `ro,nosuid,nodev,noexec,uid=actionbackup,umask=0077` 挂到 `<device.mount_root>/sdX`；残留的失效挂载先延迟卸载 |
| `umount-source` | `device` | 卸载并删除挂载点目录 |
| `mount-target` | 无 | 按 `storage.*` 调 `mount.cifs`（`credentials=`、`uid/gid`、`nosuid,nodev,noexec`、可选 `vers=`；SMB1 拒绝） |
| `umount-target` | 无 | 卸载 `storage.mount_point` |
| `ping` | 无 | 存活 |

不用 sudo 的原因：主服务单元 `NoNewPrivileges=true` 下 setuid 无效；独立 root 守护保持了权限分离，
且挂载发生在宿主命名空间，`ProtectSystem=strict` 的主服务沙箱以 slave 传播看到这些挂载。
规则实现在 `internal/platform/linux/helper_rules.go`，单元测试覆盖拒绝用例。
