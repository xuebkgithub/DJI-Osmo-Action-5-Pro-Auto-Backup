//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"action-backup/internal/platform/raw"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFindUSBDevices(t *testing.T) {
	nodes, err := findUSBDevices(fixture(t, "ioreg-iousb.plist"), 0x0506, 0x8002)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 DJI device, got %d", len(nodes))
	}
	n := nodes[0]
	if n.EntryName != "OsmoAction5pro_SN:0A1B2C3D" || n.ProductName != n.EntryName || n.Serial != "123456789ABCDEF" || n.VendorName != "DJI" || n.DeviceSpeed != 3 {
		t.Errorf("unexpected node %+v", n)
	}
	if nodes, _ := findUSBDevices(fixture(t, "ioreg-iousb.plist"), 0x0506, 0x9999); len(nodes) != 0 {
		t.Error("other PID must not match")
	}
}

func TestFindLUNs(t *testing.T) {
	luns, err := findLUNs(fixture(t, "ioreg-dji-subtree.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(luns) != 2 || luns[0].LUN != 0 || luns[0].BSDName != "disk4" || luns[1].LUN != 1 || luns[1].BSDName != "disk5" {
		t.Fatalf("unexpected %+v", luns)
	}
	if luns[0].Size != 51493834752 || luns[1].Size != 124658909184 {
		t.Errorf("sizes %+v", luns)
	}
}

func TestParseDiskInfo(t *testing.T) {
	info, err := parseDiskInfo(fixture(t, "diskutil-info-disk5.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Device != "disk5" || info.VolumeName != "Untitled" || info.MountPoint != "/Volumes/Untitled" || info.FilesystemType != "exfat" || info.Size != 124658909184 || info.VolumeUUID == "" || !info.WholeDisk {
		t.Errorf("unexpected %+v", info)
	}
}

// fakeRunner 用固件回放命令输出；挂载点改为临时目录以通过存在性检查。
func fakeRunner(t *testing.T, mountRoot string, present bool) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(cmd, "ioreg -p IOUSB"):
			if !present {
				return []byte(`<plist version="1.0"><dict><key>IORegistryEntryChildren</key><array/></dict></plist>`), nil
			}
			return fixture(t, "ioreg-iousb.plist"), nil
		case strings.HasPrefix(cmd, "ioreg -p IOService"):
			if args[len(args)-1] != "OsmoAction5pro_SN:0A1B2C3D" {
				return nil, fmt.Errorf("unexpected -n %q", args[len(args)-1])
			}
			return fixture(t, "ioreg-dji-subtree.plist"), nil
		case strings.HasPrefix(cmd, "diskutil info -plist "):
			b := fixture(t, "diskutil-info-"+args[2]+".plist")
			return []byte(strings.ReplaceAll(string(b), "/Volumes/", mountRoot+"/")), nil
		}
		return nil, fmt.Errorf("unexpected command %q", cmd)
	}
}

func newTestDetector(t *testing.T, run Runner) *Detector {
	t.Helper()
	d, err := NewDetector("0506", "8002", 10*time.Millisecond, run, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDiscoverRealCameraFixture(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"OsmoAction", "Untitled"} {
		if err := os.MkdirAll(filepath.Join(root, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := newTestDetector(t, fakeRunner(t, root, true))
	devs, err := d.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	dev := devs[0]
	if dev.Key != "0506:8002:0A1B2C3D" || dev.SerialNumber != "0A1B2C3D" || dev.WeakIdentity || dev.Name != "DJI OsmoAction5pro" {
		t.Errorf("identity %+v", dev)
	}
	if len(dev.Volumes) != 2 {
		t.Fatalf("volumes %+v", dev.Volumes)
	}
	v0, v1 := dev.Volumes[0], dev.Volumes[1]
	if v0.LUN != 0 || v0.Label != "OsmoAction" || v0.SizeBytes != 51493834752 || v0.MountPath != filepath.Join(root, "OsmoAction") || v0.Filesystem != "exfat" || !strings.HasPrefix(v0.Key, "uuid:") {
		t.Errorf("v0 %+v", v0)
	}
	if v1.LUN != 1 || v1.Label != "Untitled" || v1.SizeBytes != 124658909184 || v1.DevicePath != "/dev/disk5" {
		t.Errorf("v1 %+v", v1)
	}
	if p, err := (Mounter{}).MountReadOnly(context.Background(), v1); err != nil || p != v1.MountPath {
		t.Errorf("mounter %q %v", p, err)
	}
}

func TestNotReadyWhenMountPointMissing(t *testing.T) {
	root := t.TempDir() // 不创建挂载目录 → 未就绪
	d := newTestDetector(t, fakeRunner(t, root, true))
	devs, err := d.Discover(context.Background())
	if err != nil || len(devs) != 0 {
		t.Fatalf("unmounted camera must not be ready: %v %v", devs, err)
	}
}

func TestWatchEvents(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"OsmoAction", "Untitled"} {
		_ = os.MkdirAll(filepath.Join(root, v), 0o755)
	}
	var present atomic.Bool
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return fakeRunner(t, root, present.Load())(ctx, name, args...)
	}
	d := newTestDetector(t, run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := d.Watch(ctx)
	present.Store(true)
	select {
	case ev := <-ch:
		if ev.Kind != raw.Attached || ev.Device.Key != "0506:8002:0A1B2C3D" {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no attach event")
	}
	present.Store(false)
	select {
	case ev := <-ch:
		if ev.Kind != raw.Detached {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no detach event")
	}
}
