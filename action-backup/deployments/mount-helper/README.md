# mount-helper（V0.6）

受限 root helper：只接受固定的 DJI 相机块设备路径和 `/run/action-backup/source/<volume>` 挂载点，
以 `ro,nosuid,nodev,noexec` 挂载 exFAT，以及按 `/etc/action-backup/config.yaml` 的 `storage.share`
与凭据文件挂载 CIFS 到 `storage.mount_point`。通过 sudoers 规则授权 `actionbackup` 用户调用，
参数一律白名单校验，不接受任意路径。实现放在 V0.6。
