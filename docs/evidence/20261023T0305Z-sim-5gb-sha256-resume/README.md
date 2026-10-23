# 5 GB simulation 备份 + 断点续传实测（2026-10-23）

- 源：`scripts/gen-fixtures.sh --size 5G --seed 7` 生成的 5 368 709 120 字节 MP4（不可压缩随机流）。
- 目标：本地目录 `dev/nas-5g`，`verify_mode: sha256`，`checkpoint_bytes_mb: 16`。
- 过程：首次运行在约 2.77 GB 处收到 SIGTERM（服务停止）；重启后启动恢复把任务标为 INTERRUPTED，
  `auto_backup` 触发的 `Service.Start` 把该任务重新入队，Worker 从数据库断点续传，SHA-256 状态由断点恢复。
- 结果：任务 COMPLETED，`checksum_sha256 = 8addbbbc…1a00`，与 `source-manifest.sha256` 一致，目标目录无残留 `.part`。
- 本地 APFS 吞吐约 450 到 900 MB/s，与 NAS 无关，不作为性能依据。
- 事件序列见 `service.log`：recovery.interrupted → device.attached → plan.generated → backup.started(1 re-queued) → task.started → backup.completed(resumed=true)。
