//go:build faultinject && recovery

package recovery

import (
	"testing"
	"time"

	"action-backup/internal/faultinject"
)

// TestRemainingPointsClassification 覆盖其余注入点：一次性故障后的任务状态分类，以及清除故障后人工 retry 能完成。
func TestRemainingPointsClassification(t *testing.T) {
	cases := []struct {
		point  faultinject.Point
		errno  string
		expect string
		nth    int // 0 = once；dest.probe 的第 1 次命中会被计划阶段消耗，故取第 2 次
	}{
		{faultinject.SourceOpen, "EIO", "INTERRUPTED", 0},     // 源打不开：设备问题，可重试
		{faultinject.DestOpen, "EACCES", "FAILED", 0},         // 目标打不开但目标在线：失败
		{faultinject.DestSync, "EIO", "FAILED", 0},            // fsync 失败且目标在线：失败
		{faultinject.DestRename, "EIO", "FAILED", 0},          // 归档重命名失败：失败
		{faultinject.DBCommit, "EBUSY", "FAILED", 0},          // 断点落库失败：失败，人工重试
		{faultinject.DestWrite, "ENOSPC", "FAILED", 0},        // 磁盘满且目标在线：失败
		{faultinject.DestProbe, "ENOTCONN", "INTERRUPTED", 2}, // 建 part 前探测失败：目标不可用
	}
	for _, c := range cases {
		t.Run(string(c.point), func(t *testing.T) {
			h := newHarness(t, map[string]int{bigFile: mib(2)}, nil)
			h.addRule(faultinject.Rule{ID: "once", Point: c.point, Trigger: faultinject.Trigger{Once: c.nth == 0, NthCall: c.nth}, Action: faultinject.Action{Kind: "error", Errno: c.errno}})
			h.startBackup()
			tk := h.waitStatusIn(1, 15*time.Second, c.expect, "COMPLETED", "FAILED", "INTERRUPTED")
			if tk["status"] != c.expect {
				t.Fatalf("%s: want %s got %v (%v)", c.point, c.expect, tk["status"], tk["last_error"])
			}
			if c.expect == "INTERRUPTED" {
				// 一次性故障已消耗：重连检查会自动重试
				h.waitStatus(1, 15*time.Second, "COMPLETED")
			} else {
				time.Sleep(1200 * time.Millisecond)
				if st := h.task(1)["status"]; st != "FAILED" {
					t.Fatalf("FAILED must not auto-retry: %v", st)
				}
				if code, _ := h.call("POST", "/api/v1/tasks/1/retry", nil); code != 200 {
					t.Fatal("retry")
				}
				h.waitStatus(1, 15*time.Second, "COMPLETED")
			}
			h.assertDestination(bigFile)
		})
	}
}
