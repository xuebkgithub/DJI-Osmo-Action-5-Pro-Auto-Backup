package linux

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"action-backup/internal/platform/raw"
)

func newTestDetector(t *testing.T, tr fakeTree, mountinfo string) *Detector {
	t.Helper()
	d := NewDetector("0506", "8002", "/run/action-backup-mounter/source", 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.fs = tr.Sysfs
	d.mountinfo = mountinfo
	return d
}

func TestDetectorDiscover(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	tr.addLUN(t, 1, "sdb", 243474432, "8:16", cameraProps("0123-4567", ""))
	d := newTestDetector(t, tr, filepath.Join(t.TempDir(), "missing"))
	devs, err := d.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %+v", devs)
	}
	dev := devs[0]
	if dev.Key != "0506:8002:0A1B2C3D" || dev.SerialNumber != "0A1B2C3D" || dev.WeakIdentity || dev.Name != "DJI OsmoAction5pro" {
		t.Errorf("device %+v", dev)
	}
	if len(dev.Volumes) != 2 {
		t.Fatalf("volumes %+v", dev.Volumes)
	}
	v0, v1 := dev.Volumes[0], dev.Volumes[1]
	if v0.Key != "uuid:f59d-f69a" || v0.LUN != 0 || v0.Label != "OsmoAction" || v0.Filesystem != "exfat" || v0.DevicePath != "/dev/sda" || v0.MountPath != "" || v0.SizeBytes != 100573896*512 {
		t.Errorf("v0 %+v", v0)
	}
	if v1.Key != "uuid:0123-4567" || v1.LUN != 1 || v1.Label != "" || v1.DevicePath != "/dev/sdb" {
		t.Errorf("v1 %+v", v1)
	}
}

func TestDetectorReusesExistingMount(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	mi := filepath.Join(t.TempDir(), "mountinfo")
	content := "100 30 8:0 / /run/action-backup-mounter/source/sda ro,nosuid,nodev,noexec,relatime shared:1 - exfat /dev/sda ro,uid=999\n"
	if err := os.WriteFile(mi, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDetector(t, tr, mi)
	devs, _ := d.Discover(context.Background())
	if got := devs[0].Volumes[0].MountPath; got != "/run/action-backup-mounter/source/sda" {
		t.Fatalf("mount path %q", got)
	}
	// 同名挂载点但来源不是本设备（残留）时不复用
	content = "100 30 8:0 / /run/action-backup-mounter/source/sda ro shared:1 - exfat /dev/sdc ro\n"
	_ = os.WriteFile(mi, []byte(content), 0o644)
	devs, _ = d.Discover(context.Background())
	if got := devs[0].Volumes[0].MountPath; got != "" {
		t.Fatalf("stale mount must not be reused, got %q", got)
	}
}

func TestDetectorWatchEvents(t *testing.T) {
	tr := newFakeTree(t)
	d := newTestDetector(t, tr, filepath.Join(t.TempDir(), "missing"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := d.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	ch, err := d.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 相机出现但 LUN 未就绪：不应有事件
	tr.addLUN(t, 0, "sda", 100573896, "8:0", nil)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	select {
	case ev := <-ch:
		if ev.Kind != raw.Attached || ev.Device.Key != "0506:8002:0A1B2C3D" {
			t.Fatalf("unexpected %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no attached event")
	}
	// 拔出：USB 节点消失
	_ = os.RemoveAll(tr.usbDev)
	_ = os.Remove(filepath.Join(tr.Root, "bus", "usb", "devices", "6-1"))
	select {
	case ev := <-ch:
		if ev.Kind != raw.Detached {
			t.Fatalf("unexpected %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no detached event")
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[usbDevice]string{
		{Product: "OsmoAction5pro_SN:0A1B2C3D", Manufacturer: "DJI"}: "DJI OsmoAction5pro",
		{Product: "DJI Thing", Manufacturer: "DJI"}:                  "DJI Thing",
		{Name: "6-1"}: "6-1",
	}
	for in, want := range cases {
		if got := displayName(in); got != want {
			t.Errorf("%+v: got %q want %q", in, got, want)
		}
	}
}
