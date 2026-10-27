package linux

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/platform/raw"
)

// fakeOps 记录挂载调用，不碰真实系统。
type fakeOps struct {
	mu       sync.Mutex
	mounts   map[string]string // target → source
	healthy  map[string]bool
	sources  []sourcePlan
	targets  []targetPlan
	unmounts []string
	fail     error
}

func newFakeOps() *fakeOps { return &fakeOps{mounts: map[string]string{}, healthy: map[string]bool{}} }

func (f *fakeOps) MountSource(_ context.Context, p sourcePlan, uid, gid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sources = append(f.sources, p)
	f.mounts[p.Target] = p.Source
	f.healthy[p.Target] = true
	return nil
}

func (f *fakeOps) MountTarget(_ context.Context, p targetPlan, uid, gid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.targets = append(f.targets, p)
	f.mounts[p.MountPoint] = p.Share
	f.healthy[p.MountPoint] = true
	return nil
}

func (f *fakeOps) Unmount(target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unmounts = append(f.unmounts, target)
	delete(f.mounts, target)
	delete(f.healthy, target)
	return nil
}

func (f *fakeOps) IsMounted(target string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.mounts[target]
	return s, ok
}

func (f *fakeOps) Healthy(target string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy[target]
}

func writeConfig(t *testing.T, dir, mountRoot, share, credFile string) string {
	t.Helper()
	cfg := `mode: "dry-run"
server:
  listen: "127.0.0.1:0"
device:
  vendor_id: "0506"
  product_id: "8002"
  auto_detect: true
  auto_backup: true
  mount_root: "` + mountRoot + `"
  readonly: true
backup:
  source_directory: "DCIM"
  file_extensions: [".mp4"]
  worker_count: 1
  buffer_size_mb: 4
  retry_count: 3
  checkpoint_interval_sec: 5
  checkpoint_bytes_mb: 64
  verify_mode: "basic"
  delete_source: false
storage:
  type: "cifs"
  share: "` + share + `"
  mount_point: "` + filepath.Join(dir, "nas") + `"
  smb_version: "auto"
  credentials_file: "` + credFile + `"
  destination_root: "DJI"
  destination_layout: "{volume_type}/{relative_path}"
database:
  path: "` + filepath.Join(dir, "backup.db") + `"
logging:
  level: "info"
  format: "json"
`
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestServer(t *testing.T) (*Server, *fakeOps, fakeTree, string) {
	t.Helper()
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	dir := t.TempDir()
	cred := filepath.Join(dir, "smb.credentials")
	if err := os.WriteFile(cred, []byte("username=u\npassword=p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountRoot := filepath.Join(dir, "source")
	cfgPath := writeConfig(t, dir, mountRoot, "//192.168.1.100/VideoBackup", cred)
	ops := newFakeOps()
	srv := &Server{ConfigPath: cfgPath, OwnerUID: 999, OwnerGID: 995, Sysfs: tr.Sysfs, Ops: ops, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return srv, ops, tr, mountRoot
}

func TestHelperMountSource(t *testing.T) {
	srv, ops, _, mountRoot := newTestServer(t)
	ctx := context.Background()
	resp := srv.Handle(ctx, HelperRequest{Op: OpMountSource, Device: "/dev/sda"})
	if !resp.OK || resp.Path != filepath.Join(mountRoot, "sda") {
		t.Fatalf("resp %+v", resp)
	}
	if len(ops.sources) != 1 || ops.sources[0].FSType != "exfat" || ops.sources[0].Source != "/dev/sda" {
		t.Fatalf("sources %+v", ops.sources)
	}
	if st, err := os.Stat(resp.Path); err != nil || !st.IsDir() {
		t.Fatalf("mount point dir must exist: %v", err)
	}
	// 重复请求：已挂载且健康，不再挂
	resp = srv.Handle(ctx, HelperRequest{Op: OpMountSource, Device: "/dev/sda"})
	if !resp.OK || len(ops.sources) != 1 {
		t.Fatalf("second mount %+v sources=%d", resp, len(ops.sources))
	}
	// 失效残留：先卸再挂
	ops.healthy[resp.Path] = false
	resp = srv.Handle(ctx, HelperRequest{Op: OpMountSource, Device: "/dev/sda"})
	if !resp.OK || len(ops.sources) != 2 || len(ops.unmounts) != 1 {
		t.Fatalf("stale remount %+v sources=%d unmounts=%v", resp, len(ops.sources), ops.unmounts)
	}
	// 卸载
	resp = srv.Handle(ctx, HelperRequest{Op: OpUnmountSource, Device: "/dev/sda"})
	if !resp.OK || len(ops.unmounts) != 2 {
		t.Fatalf("unmount %+v %v", resp, ops.unmounts)
	}
	if _, err := os.Stat(filepath.Join(mountRoot, "sda")); !os.IsNotExist(err) {
		t.Error("mount point dir should be removed after unmount")
	}
}

func TestHelperRejectsBadSources(t *testing.T) {
	srv, ops, _, _ := newTestServer(t)
	ctx := context.Background()
	bad := []string{"/dev/sdb", "/dev/sda1", "/dev/mmcblk0p7", "/dev/../dev/sda", "sda", "/dev/sda;rm", "", "/dev/zram0"}
	for _, dev := range bad {
		if resp := srv.Handle(ctx, HelperRequest{Op: OpMountSource, Device: dev}); resp.OK {
			t.Errorf("%q must be rejected", dev)
		}
	}
	if len(ops.sources) != 0 {
		t.Errorf("no mount expected, got %+v", ops.sources)
	}
	if resp := srv.Handle(ctx, HelperRequest{Op: "format"}); resp.OK {
		t.Error("unknown op must be rejected")
	}
	if resp := srv.Handle(ctx, HelperRequest{Op: OpPing}); !resp.OK {
		t.Error("ping")
	}
}

func TestHelperRejectsForeignFilesystem(t *testing.T) {
	srv, ops, tr, _ := newTestServer(t)
	props := cameraProps("1111-2222", "")
	props["ID_FS_TYPE"] = "ext4"
	tr.addLUN(t, 1, "sdb", 1000, "8:16", props)
	if resp := srv.Handle(context.Background(), HelperRequest{Op: OpMountSource, Device: "/dev/sdb"}); resp.OK || !strings.Contains(resp.Error, "ext4") {
		t.Fatalf("ext4 must be rejected: %+v", resp)
	}
	if len(ops.sources) != 0 {
		t.Error("no mount expected")
	}
}

func TestHelperMountTarget(t *testing.T) {
	srv, ops, _, _ := newTestServer(t)
	ctx := context.Background()
	resp := srv.Handle(ctx, HelperRequest{Op: OpMountTarget})
	if !resp.OK || len(ops.targets) != 1 {
		t.Fatalf("resp %+v targets %+v", resp, ops.targets)
	}
	p := ops.targets[0]
	if p.Share != "//192.168.1.100/VideoBackup" || p.Vers != "" || !strings.HasSuffix(p.CredentialsFile, "smb.credentials") {
		t.Errorf("plan %+v", p)
	}
	if resp := srv.Handle(ctx, HelperRequest{Op: OpMountTarget}); !resp.OK || len(ops.targets) != 1 {
		t.Fatalf("second mount must be a no-op: %+v", resp)
	}
	if resp := srv.Handle(ctx, HelperRequest{Op: OpUnmountTarget}); !resp.OK || len(ops.unmounts) != 1 {
		t.Fatalf("unmount %+v", resp)
	}
}

func TestHelperMountTargetFailurePropagates(t *testing.T) {
	srv, ops, _, _ := newTestServer(t)
	ops.fail = errors.New("mount error(13): Permission denied")
	resp := srv.Handle(context.Background(), HelperRequest{Op: OpMountTarget})
	if resp.OK || !strings.Contains(resp.Error, "Permission denied") {
		t.Fatalf("resp %+v", resp)
	}
}

func TestPlanTargetRules(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "cred")
	_ = os.WriteFile(cred, []byte("username=u\npassword=p\n"), 0o600)
	base := func() *config.Config {
		cfg, err := config.Load(writeConfig(t, dir, "/run/x/source", "//nas.local/Share", cred))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := base()
	if p, err := planTarget(cfg); err != nil || p.Vers != "" {
		t.Fatalf("auto: %v %+v", err, p)
	}
	cfg.Storage.SMBVersion = "3.1.1"
	if p, err := planTarget(cfg); err != nil || p.Vers != "3.1.1" {
		t.Fatalf("3.1.1: %v %+v", err, p)
	}
	for _, v := range []string{"1.0", "1", "0.9", "abc", "3.1.1; rm"} {
		cfg.Storage.SMBVersion = v
		if _, err := planTarget(cfg); err == nil {
			t.Errorf("vers %q must be rejected", v)
		}
	}
	cfg = base()
	for _, sh := range []string{"//nas/", "nas/share", "//nas/share,uid=0", "//nas/share/sub", "//nas /share", "", "//nas/share\n"} {
		cfg.Storage.Share = sh
		if _, err := planTarget(cfg); err == nil {
			t.Errorf("share %q must be rejected", sh)
		}
	}
	cfg = base()
	cfg.Storage.MountPoint = "/mnt/../etc"
	if _, err := planTarget(cfg); err == nil {
		t.Error("unclean mount point must be rejected")
	}
	cfg = base()
	cfg.Storage.Type = config.StoragePremounted
	if _, err := planTarget(cfg); err == nil {
		t.Error("premounted must not be mounted by helper")
	}
	cfg = base()
	_ = os.Chmod(cred, 0o644)
	if _, err := planTarget(cfg); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Errorf("world-readable credentials must be rejected: %v", err)
	}
	_ = os.Chmod(cred, 0o600)
	cfg = base()
	cfg.Storage.CredentialsFile = filepath.Join(dir, "missing")
	if _, err := planTarget(cfg); err == nil {
		t.Error("missing credentials must be rejected")
	}
}

func TestClientServerOverSocket(t *testing.T) {
	srv, ops, _, mountRoot := newTestServer(t)
	// macOS 的 unix socket 路径上限 104 字节，不能用 t.TempDir()
	sockDir, err := os.MkdirTemp("/tmp", "ab-helper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	srv.Socket = filepath.Join(sockDir, "s")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	c := Client{Socket: srv.Socket, Timeout: 5 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := c.Call(ctx, HelperRequest{Op: OpPing}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	m := Mounter{Client: c}
	path, err := m.MountReadOnly(ctx, rawVolume("/dev/sda"))
	if err != nil || path != filepath.Join(mountRoot, "sda") {
		t.Fatalf("mount via socket: %v %q", err, path)
	}
	if _, err := m.MountReadOnly(ctx, rawVolume("/dev/sdq")); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign device must fail with helper error: %v", err)
	}
	if err := m.Unmount(ctx, rawVolume("/dev/sda")); err != nil {
		t.Fatal(err)
	}
	sm := StorageMounter{Client: c}
	if err := sm.MountTarget(ctx); err != nil || len(ops.targets) != 1 {
		t.Fatalf("mount target: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	if _, err := c.Call(context.Background(), HelperRequest{Op: OpPing}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("after stop: %v", err)
	}
}

func rawVolume(dev string) raw.Volume { return raw.Volume{Key: "k", DevicePath: dev} }
