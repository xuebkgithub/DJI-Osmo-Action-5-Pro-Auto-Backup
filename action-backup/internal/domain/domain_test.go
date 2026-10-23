package domain

import (
	"testing"
	"time"
)

func TestResolveSerial(t *testing.T) {
	cases := []struct {
		product, usb string
		want         string
		weak         bool
	}{
		{"OsmoAction5pro_SN:0A1B2C3D", "123456789ABCDEF", "0A1B2C3D", false},
		{"OsmoAction5pro_SN:0a1b2c3d", "", "0A1B2C3D", false},
		{"Unknown", "123456789ABCDEF", "", true},
		{"Unknown", "", "", true},
		{"Unknown", "REAL123", "REAL123", true},
	}
	for _, c := range cases {
		got, weak := ResolveSerial(c.product, c.usb)
		if got != c.want || weak != c.weak {
			t.Errorf("ResolveSerial(%q,%q) = %q,%v want %q,%v", c.product, c.usb, got, weak, c.want, c.weak)
		}
	}
}

func TestDeviceKey(t *testing.T) {
	if got := DeviceKey("0506", "8002", "0a1b2c3d"); got != "0506:8002:0A1B2C3D" {
		t.Fatalf("got %q", got)
	}
	if got := DeviceKey("0506", "8002", ""); got != "0506:8002:unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestClassifyVolume(t *testing.T) {
	if ClassifyVolume(0, 124<<30, "Untitled") != VolumeInternal {
		t.Error("LUN 0 must win over size")
	}
	if ClassifyVolume(1, 51<<30, "OsmoAction") != VolumeSDCard {
		t.Error("LUN 1 must win over label")
	}
	if ClassifyVolume(-1, 51<<30, "") != VolumeInternal {
		t.Error("51 GB without LUN is internal")
	}
	if ClassifyVolume(-1, 124<<30, "") != VolumeSDCard {
		t.Error("124 GB without LUN is sdcard")
	}
	if ClassifyVolume(-1, 0, "OsmoAction") != VolumeInternal {
		t.Error("label fallback")
	}
	if ClassifyVolume(-1, 0, "") != VolumeUnknown {
		t.Error("unknown fallback")
	}
}

func TestNormalizeModTimeAndFingerprint(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	a := time.Date(2026, 10, 23, 8, 50, 35, 123_000_000, loc)
	b := time.Date(2026, 10, 23, 8, 50, 35, 999_000_000, loc)
	if NormalizeModTime(a) != NormalizeModTime(b) {
		t.Fatal("sub-second differences must be truncated")
	}
	if NormalizeModTime(a).Location() != time.UTC {
		t.Fatal("must be UTC")
	}
	f1 := ComputeFingerprint("d", "v", "DCIM/DJI_003/x.MP4", 10, a)
	f2 := ComputeFingerprint("d", "v", "DCIM/DJI_003/x.MP4", 10, b)
	f3 := ComputeFingerprint("d", "v", "DCIM/DJI_003/x.MP4", 11, a)
	if f1 != f2 || f1 == f3 || len(f1) != 64 {
		t.Fatalf("fingerprint mismatch: %s %s %s", f1, f2, f3)
	}
}

func TestTaskTransitions(t *testing.T) {
	ok := [][2]TaskStatus{
		{TaskPending, TaskQueued}, {TaskPending, TaskSkipped}, {TaskQueued, TaskTransferring},
		{TaskTransferring, TaskVerifying}, {TaskTransferring, TaskInterrupted}, {TaskVerifying, TaskCompleted},
		{TaskVerifying, TaskFailed}, {TaskInterrupted, TaskQueued}, {TaskFailed, TaskQueued}, {TaskQueued, TaskCancelled}, {TaskTransferring, TaskQueued},
	}
	for _, p := range ok {
		if !p[0].CanTransitionTo(p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	bad := [][2]TaskStatus{
		{TaskPending, TaskTransferring}, {TaskCompleted, TaskQueued}, {TaskSkipped, TaskQueued},
		{TaskCancelled, TaskQueued}, {TaskQueued, TaskCompleted}, {TaskTransferring, TaskCompleted},
	}
	for _, p := range bad {
		if p[0].CanTransitionTo(p[1]) {
			t.Errorf("%s -> %s must be rejected", p[0], p[1])
		}
	}
	for _, s := range []TaskStatus{TaskCompleted, TaskSkipped, TaskCancelled} {
		if !s.IsTerminal() {
			t.Errorf("%s is terminal", s)
		}
	}
	if TaskFailed.IsTerminal() {
		t.Error("FAILED is retryable, not terminal")
	}
}
