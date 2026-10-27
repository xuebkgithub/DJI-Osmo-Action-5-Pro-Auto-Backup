# V0.7 性能测试（2026-10-27 06:49–07:00 UTC，OEC-Turbo → NAS `//172.20.100.254/home`）

对象：microSD 上 482,234,357 B 的 `DJI_20261027075753_0027_D.MP4`，每轮删掉 NAS 副本后 `POST /backups/start`，
吞吐按 journald 的 `transferring` / `completed` 时间戳（纳秒）计算，含 4 MiB 分块写、检查点 fsync、basic 校验、rename 与 mtime 设置。
`01-*.txt`、`02-*.txt` 由 `action-backup/scripts/test-performance.sh` 产生；裸吞吐与首轮数据来自会话中的命令输出（见下）。

| 条件 | 吞吐 | 来源 |
| --- | --- | --- |
| 裸 USB 读（`dd` 4 MiB / 64 MiB → /dev/null，drop_caches） | 102 / 116 MB/s | 会话命令 |
| 裸 CIFS 写（`/dev/zero` → NAS，4 MiB，`conv=fsync`） | 71.7 MB/s | 会话命令 |
| `dd` USB → CIFS 直拷（4 MiB，fsync） | 44.9 MB/s | 会话命令（串行读写的上限 ≈ 1/(1/110+1/72) ≈ 43） |
| 应用 v0.6.0-3：串行读写，检查点 5 s / 64 MB | 24.1 MB/s（19.98 s） | `20261027T0620Z-v06-nas-cifs-backup/` |
| 应用 v0.6.0-3：串行读写，检查点 512 MB（5 s 间隔仅触发 1 次） | 65.9 MB/s（7.32 s） | 会话命令 14:49 |
| 应用 v0.6.0-3-dirty：读写流水线，4 MB 缓冲，检查点 512 MB / 30 s | 66.9 / **69.6** MB/s（7.21 / 6.93 s，两轮） | `01-pipeline-4mb.txt` |
| 同上，16 MB 缓冲 | 66.2 MB/s（7.29 s） | `02-pipeline-16mb.txt` |

结论：

1. 瓶颈曾是**每个检查点的 fsync**：5 s / 64 MB 的默认值让 CIFS 每 2–3 秒 flush 一次，吞吐 24 MB/s；把检查点放宽到 512 MB 后直接到 66 MB/s。
   默认改为 `checkpoint_interval_sec: 30`、`checkpoint_bytes_mb: 512`，崩溃最多丢 512 MB 进度（约 8 秒），续传机制不变。
2. 读写流水线（预读 goroutine + 两块缓冲）把串行上限（~43 MB/s）解除，现在贴近裸 CIFS 写（72 MB/s）的 93–97%。
3. 缓冲 4 MB 与 16 MB 无差别，CIFS `wsize` 本就是 4 MiB；默认保持 4 MB。
4. 折算 81.1 GB 全量约 **19–20 分钟**，满足 30 分钟目标（待相机放回全量素材实测端到端）。
5. 资源：主服务 RSS 6.2 MB（流水线两块 4 MB 缓冲），`ps pcpu` 为生命周期均值不能反映瞬时 CPU，负载 0.6 左右（4 核）。
6. 归档后 `SetModTime` 生效：NAS 上 mtime `2026-10-27 07:59:41`，源 `07:59:41.49`（CIFS 侧秒级）。
