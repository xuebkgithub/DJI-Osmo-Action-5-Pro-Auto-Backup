# 恢复测试套件实测（2026-10-23）

`make test-recovery`（`go test -race -tags faultinject,recovery ./tests/recovery/...`）输出见 `go-test-recovery.txt`。

| 用例 | 注入点 / 动作 | 验证 |
| --- | --- | --- |
| TestSourceReadErrorIsInterruptedThenReconciled | `source.read` error EIO after_bytes 2 MiB once | INTERRUPTED → 重连检查自动重新入队 → 从断点续传 → sha256 一致 |
| TestNASOutageDuringWriteRecovers | `dest.write` error 后 `dest.probe` 持续失败 5 次（`after` 编排） | 写失败归为 INTERRUPTED，NAS 恢复后自动续传，retry_count = 1 |
| TestCorruptedPartDetectedBySHA256AndRetried | `part.corrupt` 翻转可信前缀内 16 字节 | sha256 校验失败 → FAILED，断点与 .part 丢弃，不自动重试；人工 retry 从头重传成功 |
| TestVerifyMismatchRule | `verify.hash` mismatch once | FAILED → retry → COMPLETED |
| TestDeviceDetachMidTransfer | `device.detach` disconnect（模拟拔出） | INTERRUPTED，设备不在位时不重试；重新插入后 start 复用任务续传 |
| TestThrottledPauseResume | `dest.write` throttle 4 MiB/s | 传输中暂停 → QUEUED 且断点保留 → 恢复后续传，不计为 retry |
| TestRetryExhaustedNeedsManualRetry | `source.read` error 持续 | 自动重试到 retry_count 上限后停止；start 报告 retry-exhausted；清除故障后人工 retry 成功 |
| TestConflictPolicies | 目标已有同名文件、数据库无记录 | manual 不建任务不动目标；overwrite 建任务并覆盖 |
| TestStallIsCancellable | `dest.write` stall 60 s | 卡住的任务可取消，.part 删除 |
| TestRemainingPointsClassification | `source.open`、`dest.open`、`dest.sync`、`dest.rename`、`db.commit`、`dest.write`(ENOSPC)、`dest.probe` 各一次性错误 | 源 / 目标不可用归 INTERRUPTED 并自动重试；目标在线时的写、刷盘、重命名、落库失败归 FAILED，不自动重试，人工 retry 成功 |
| TestProcessCrashAtCheckpointThenRestartResumes | 子进程 `process.crash` 第二个断点后 `os.Exit(137)` | 崩溃现场 .part 与断点存在；重启后 recovery.interrupted → 自动重新入队 → 从断点续传 → sha256 一致 |
