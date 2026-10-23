//go:build faultinject && recovery

package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/faultinject"
)

func TestSourceReadErrorIsInterruptedThenReconciled(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(6)}, nil)
	h.addRule(faultinject.Rule{ID: "usb-eio", Point: faultinject.SourceRead, Trigger: faultinject.Trigger{AfterBytes: int64(mib(2)), Once: true}, Action: faultinject.Action{Kind: "error", Errno: "EIO"}})
	h.startBackup()
	tk := h.waitStatusIn(1, 15*time.Second, "INTERRUPTED", "COMPLETED")
	if tk["status"] == "INTERRUPTED" {
		if tk["bytes_transferred"].(float64) < float64(mib(1)) {
			t.Fatalf("checkpoint should have advanced before the error: %v", tk)
		}
		tk = h.waitStatus(1, 15*time.Second, "COMPLETED")
	}
	if tk["retry_count"].(float64) != 1 || tk["checksum_sha256"] != h.sums[bigFile] {
		t.Fatalf("after reconcile: %v", tk)
	}
	h.assertDestination(bigFile)
	if !h.hasEvent("backup.completed", func(e map[string]any) bool { return e["data"].(map[string]any)["resumed"] == true }) {
		t.Fatal("completion must report resumed=true")
	}
	if !h.hasEvent("fault.hit", nil) || !h.hasEvent("task.interrupted", nil) {
		t.Fatal("fault.hit and task.interrupted events expected")
	}
}

func TestNASOutageDuringWriteRecovers(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(6)}, nil)
	// 序列：写到 3 MiB 时 NAS 掉线（写失败），之后探测持续失败 5 次（写失败后的复核 1 次 + 每秒一次的重连检查 4 次），
	// 期间任务停留在 INTERRUPTED；探测恢复后自动重新入队并从断点续传。
	h.addRule(faultinject.Rule{ID: "nas-write", Point: faultinject.DestWrite, Trigger: faultinject.Trigger{AfterBytes: int64(mib(3)), Once: true}, Action: faultinject.Action{Kind: "error", Errno: "EIO"}})
	h.addRule(faultinject.Rule{ID: "nas-down", Point: faultinject.DestProbe, Trigger: faultinject.Trigger{After: "nas-write", Times: 5}, Action: faultinject.Action{Kind: "error", Errno: "ENOTCONN"}})
	h.startBackup()
	tk := h.waitStatus(1, 15*time.Second, "INTERRUPTED")
	if tk["retry_count"].(float64) != 0 {
		t.Fatalf("first interruption must not count as retry: %v", tk)
	}
	if tk["bytes_transferred"].(float64) < float64(mib(3)) {
		t.Fatalf("checkpoint before outage expected at >= 3 MiB: %v", tk)
	}
	tk = h.waitStatus(1, 40*time.Second, "COMPLETED")
	if tk["retry_count"].(float64) != 1 {
		t.Fatalf("expected exactly one automatic retry: %v", tk)
	}
	if !h.hasEvent("backup.completed", func(e map[string]any) bool { return e["data"].(map[string]any)["resumed"] == true }) {
		t.Fatal("must resume from checkpoint after the NAS comes back")
	}
	h.assertDestination(bigFile)
	if !h.hasEvent("task.interrupted", func(e map[string]any) bool { return strings.Contains(e["message"].(string), "nas-write") }) {
		t.Fatal("write error must be classified INTERRUPTED when the target probe fails afterwards")
	}
}

func TestCorruptedPartDetectedBySHA256AndRetried(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(4)}, nil)
	// 第一次断点落盘后翻转文件开头 16 字节：位于可信前缀内，basic 校验发现不了，sha256 必须发现
	h.addRule(faultinject.Rule{ID: "flip", Point: faultinject.PartCorrupt, Trigger: faultinject.Trigger{NthCall: 1}, Action: faultinject.Action{Kind: "corrupt", Offset: 0, Length: 16}})
	h.startBackup()
	tk := h.waitStatus(1, 15*time.Second, "FAILED")
	if !strings.Contains(tk["last_error"].(string), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch, got %v", tk["last_error"])
	}
	if _, err := os.Stat(h.destPath(bigFile) + ".part"); err == nil {
		t.Fatal("part must be discarded after a verification failure")
	}
	// FAILED 不自动重试；人工 retry 后从头重传并成功
	time.Sleep(1500 * time.Millisecond)
	if st := h.task(1)["status"]; st != "FAILED" {
		t.Fatalf("FAILED must not be auto-retried, got %v", st)
	}
	if code, _ := h.call("POST", "/api/v1/tasks/1/retry", nil); code != 200 {
		t.Fatalf("retry %d", code)
	}
	h.waitStatus(1, 15*time.Second, "COMPLETED")
	h.assertDestination(bigFile)
	if h.hasEvent("backup.completed", func(e map[string]any) bool { return e["data"].(map[string]any)["resumed"] == true }) {
		t.Fatal("after a verify failure the retry must restart from zero, not resume")
	}
}

func TestVerifyMismatchRule(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(2)}, nil)
	h.addRule(faultinject.Rule{ID: "bad-hash", Point: faultinject.VerifyHash, Trigger: faultinject.Trigger{Once: true}, Action: faultinject.Action{Kind: "mismatch"}})
	h.startBackup()
	h.waitStatus(1, 15*time.Second, "FAILED")
	if code, _ := h.call("POST", "/api/v1/tasks/1/retry", nil); code != 200 {
		t.Fatalf("retry %d", code)
	}
	h.waitStatus(1, 15*time.Second, "COMPLETED")
	h.assertDestination(bigFile)
}

func TestDeviceDetachMidTransfer(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(6)}, nil)
	h.addRule(faultinject.Rule{ID: "unplug", Point: faultinject.DeviceDetach, Trigger: faultinject.Trigger{AfterBytes: int64(mib(2)), Once: true}, Action: faultinject.Action{Kind: "disconnect"}})
	h.startBackup()
	tk := h.waitStatus(1, 15*time.Second, "INTERRUPTED")
	if code, out := h.call("GET", "/api/v1/devices", nil); code != 200 || len(out["data"].([]any)) != 0 {
		t.Fatalf("device should be detached: %v", out)
	}
	// 设备不在位：重连检查不得重新入队
	time.Sleep(2500 * time.Millisecond)
	if st := h.task(1)["status"]; st != "INTERRUPTED" {
		t.Fatalf("task must stay INTERRUPTED while device is absent, got %v", st)
	}
	// 重新插入 → start 复用任务并续传
	if code, _ := h.call("POST", "/api/v1/test/sim/devices/sim-cam-1/attach", nil); code != 200 {
		t.Fatal("attach")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, out := h.call("GET", "/api/v1/devices", nil); len(out["data"].([]any)) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	res := h.startBackup()
	if res["tasks_requeued"] != float64(1) || res["tasks_created"] != float64(0) {
		t.Fatalf("start after re-attach: %v", res)
	}
	h.waitStatus(1, 15*time.Second, "COMPLETED")
	h.assertDestination(bigFile)
	_ = tk
}

func TestThrottledPauseResume(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(8)}, nil)
	h.addRule(faultinject.Rule{ID: "slow-nas", Point: faultinject.DestWrite, Action: faultinject.Action{Kind: "throttle", BytesPerSec: int64(mib(4))}})
	h.startBackup()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tk := h.task(1)
		if tk["status"] == "TRANSFERRING" && tk["bytes_transferred"].(float64) >= float64(mib(1)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer did not reach 1 MiB: %v", tk)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := h.call("POST", "/api/v1/backups/pause", nil); code != 200 {
		t.Fatal("pause")
	}
	tk := h.waitStatus(1, 10*time.Second, "QUEUED")
	paused := tk["bytes_transferred"].(float64)
	if paused <= 0 || paused >= float64(mib(8)) {
		t.Fatalf("paused checkpoint %v", paused)
	}
	time.Sleep(500 * time.Millisecond)
	if st := h.task(1)["status"]; st != "QUEUED" {
		t.Fatalf("paused scheduler must not resume on its own: %v", st)
	}
	if code, _ := h.call("POST", "/api/v1/backups/resume", nil); code != 200 {
		t.Fatal("resume")
	}
	tk = h.waitStatus(1, 20*time.Second, "COMPLETED")
	if tk["retry_count"].(float64) != 0 {
		t.Fatalf("pause must not count as a retry: %v", tk)
	}
	h.assertDestination(bigFile)
	if !h.hasEvent("task.paused", nil) {
		t.Fatal("task.paused event expected")
	}
	if !h.hasEvent("backup.completed", func(e map[string]any) bool { return e["data"].(map[string]any)["resumed"] == true }) {
		t.Fatal("resume after pause must continue from checkpoint")
	}
}

func TestRetryExhaustedNeedsManualRetry(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(4)}, func(c *config.Config) { c.Backup.RetryCount = 2 })
	h.addRule(faultinject.Rule{ID: "always-eio", Point: faultinject.SourceRead, Trigger: faultinject.Trigger{AfterBytes: int64(mib(1))}, Action: faultinject.Action{Kind: "error", Errno: "EIO"}})
	h.startBackup()
	deadline := time.Now().Add(20 * time.Second)
	for {
		tk := h.task(1)
		if tk["status"] == "INTERRUPTED" && tk["retry_count"].(float64) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retries did not reach limit: %v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2500 * time.Millisecond)
	tk := h.task(1)
	if tk["status"] != "INTERRUPTED" || tk["retry_count"].(float64) != 2 {
		t.Fatalf("must stop retrying at the limit: %v", tk)
	}
	res := h.startBackup()
	if res["tasks_retry_exhausted"] != float64(1) || res["tasks_requeued"] != float64(0) {
		t.Fatalf("start must report exhausted task: %v", res)
	}
	// 清除故障后人工 retry 不受次数限制
	if code, _ := h.call("DELETE", "/api/v1/test/faults/always-eio", nil); code != 200 {
		t.Fatal("remove rule")
	}
	if code, _ := h.call("POST", "/api/v1/tasks/1/retry", nil); code != 200 {
		t.Fatal("manual retry")
	}
	h.waitStatus(1, 15*time.Second, "COMPLETED")
	h.assertDestination(bigFile)
}

func TestConflictPolicies(t *testing.T) {
	for _, policy := range []string{"manual", "overwrite"} {
		t.Run(policy, func(t *testing.T) {
			h := newHarness(t, map[string]int{bigFile: mib(1)}, func(c *config.Config) { c.Backup.OnConflict = policy })
			// 目标已有同名文件但数据库无记录
			dst := h.destPath(bigFile)
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.WriteFile(dst, []byte("stale"), 0o644); err != nil {
				t.Fatal(err)
			}
			res := h.startBackup()
			if res["conflicts"] != float64(1) {
				t.Fatalf("expected 1 conflict: %v", res)
			}
			if policy == "manual" {
				if res["tasks_created"] != float64(0) {
					t.Fatalf("manual must not create tasks: %v", res)
				}
				b, _ := os.ReadFile(dst)
				if string(b) != "stale" {
					t.Fatal("manual must not touch the destination")
				}
				return
			}
			if res["tasks_created"] != float64(1) || res["conflicts_overwritten"] != float64(1) {
				t.Fatalf("overwrite must create a task: %v", res)
			}
			h.waitStatus(1, 15*time.Second, "COMPLETED")
			h.assertDestination(bigFile)
		})
	}
}

func TestStallIsCancellable(t *testing.T) {
	h := newHarness(t, map[string]int{bigFile: mib(2)}, nil)
	h.addRule(faultinject.Rule{ID: "hang", Point: faultinject.DestWrite, Trigger: faultinject.Trigger{AfterBytes: int64(mib(1)), Once: true}, Action: faultinject.Action{Kind: "stall", Ms: 60000}})
	h.startBackup()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tk := h.task(1)
		if tk["status"] == "TRANSFERRING" && tk["bytes_transferred"].(float64) >= float64(mib(1)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach stall: %v", tk)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := h.call("POST", "/api/v1/tasks/1/cancel", nil); code != 200 {
		t.Fatal("cancel")
	}
	h.waitStatus(1, 10*time.Second, "CANCELLED")
	if _, err := os.Stat(h.destPath(bigFile) + ".part"); err == nil {
		t.Fatal("cancel must remove the part file")
	}
}
