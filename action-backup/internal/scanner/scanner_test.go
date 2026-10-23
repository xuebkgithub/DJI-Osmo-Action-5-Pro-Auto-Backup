package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"action-backup/internal/config"
	"action-backup/internal/domain"
)

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(root, "DCIM/DJI_003/DJI_20261023080221_0019_D.MP4"), 10)
	write(t, filepath.Join(root, "DCIM/DJI_003/DJI_20261023080221_0019_D.LRF"), 5)
	write(t, filepath.Join(root, "DCIM/DJI_002_XZH/lower.mp4"), 7)
	write(t, filepath.Join(root, "DCIM/DJI_002_XZH/._DJI_x.MP4"), 1)
	write(t, filepath.Join(root, "DCIM/.DS_Store"), 1)
	write(t, filepath.Join(root, "DCIM/.Spotlight-V100/store.MP4"), 1)
	write(t, filepath.Join(root, "MISC/outside.MP4"), 1)
	write(t, filepath.Join(outside, "link.MP4"), 3)
	if err := os.Symlink(filepath.Join(outside, "link.MP4"), filepath.Join(root, "DCIM/DJI_003/link.MP4")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "DCIM/linkdir")); err != nil {
		t.Fatal(err)
	}

	s := New(Options{SourceDirectory: "DCIM", Extensions: []string{"MP4"}, IgnorePatterns: config.DefaultIgnorePatterns})
	dev := domain.Device{Key: "0506:8002:X"}
	vol := domain.Volume{ID: 7, Key: "uuid:abc"}
	res, err := s.Scan(context.Background(), dev, vol, root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SourceExists {
		t.Fatal("DCIM exists")
	}
	got := map[string]domain.SourceFile{}
	for _, f := range res.Files {
		got[f.RelativePath] = f
	}
	want := []string{"DCIM/DJI_003/DJI_20261023080221_0019_D.MP4", "DCIM/DJI_002_XZH/lower.mp4"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing %s", w)
		}
	}
	f := got[want[0]]
	if f.Size != 10 || f.VolumeID != 7 || f.DeviceKey != dev.Key || len(f.Fingerprint) != 64 || f.ModTime.Nanosecond() != 0 {
		t.Errorf("file %+v", f)
	}
	if res.TotalBytes != 17 {
		t.Errorf("total bytes %d", res.TotalBytes)
	}
	if res.Skipped < 3 {
		t.Errorf("skipped %d", res.Skipped)
	}
}

func TestScanMissingSource(t *testing.T) {
	s := New(Options{Extensions: []string{".mp4"}})
	res, err := s.Scan(context.Background(), domain.Device{}, domain.Volume{}, t.TempDir())
	if err != nil || res.SourceExists || len(res.Files) != 0 {
		t.Fatalf("empty volume: %+v %v", res, err)
	}
}

func TestScanIsReadOnly(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "DCIM/DJI_003/a.MP4"), 1)
	if err := os.Chmod(filepath.Join(root, "DCIM/DJI_003"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "DCIM/DJI_003"), 0o755) })
	s := New(Options{Extensions: []string{".mp4"}})
	res, err := s.Scan(context.Background(), domain.Device{}, domain.Volume{}, root)
	if err != nil || len(res.Files) != 1 {
		t.Fatalf("read-only directory must scan fine: %+v %v", res, err)
	}
}
