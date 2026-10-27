package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/domain"
	"action-backup/internal/platform/sim"
	"action-backup/internal/runmode"
)

func newTarget(t *testing.T, mode runmode.Mode) (*Target, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.StorageConfig{Type: config.StorageLocal, LocalRoot: root, DestinationRoot: "DJI-TEST", DestinationLayout: "{volume_type}/{relative_path}"}
	tg, err := New(cfg, runmode.NewGuard(mode, true), sim.LocalProbe{})
	if err != nil {
		t.Fatal(err)
	}
	return tg, root
}

func TestWriteCycle(t *testing.T) {
	ctx := context.Background()
	tg, root := newTarget(t, runmode.Simulation)
	if h := tg.Health(ctx); !h.OK {
		t.Fatalf("health: %+v", h)
	}
	rel := "sdcard/DCIM/DJI_003/a.MP4"
	f, err := tg.CreatePart(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := filepath.Join(root, "DJI-TEST", "sdcard", "DCIM", "DJI_003", "a.MP4")
	if _, err := os.Stat(want + PartSuffix); err != nil {
		t.Fatalf("part file missing: %v", err)
	}
	wantTime := time.Unix(1700000000, 0)
	if err := tg.SetModTime(ctx, rel, wantTime); err == nil {
		t.Fatal("SetModTime before Commit must fail: final file does not exist yet")
	}
	if err := tg.Commit(ctx, rel); err != nil {
		t.Fatal(err)
	}
	if st, err := tg.Stat(rel); err != nil || st.Size() != 5 {
		t.Fatalf("final file: %v", err)
	}
	if err := tg.SetModTime(ctx, rel, wantTime); err != nil {
		t.Fatal(err)
	}
	if st, _ := tg.Stat(rel); !st.ModTime().Equal(wantTime) {
		t.Fatalf("mtime %v, want %v", st.ModTime(), wantTime)
	}
	if err := tg.RemovePart(ctx, rel); err != nil {
		t.Fatalf("remove missing part must be ok: %v", err)
	}
}

func TestDryRunDeniesWrites(t *testing.T) {
	ctx := context.Background()
	tg, root := newTarget(t, runmode.DryRun)
	if _, err := tg.CreatePart(ctx, "x/y.MP4"); !errors.Is(err, domain.ErrModeReadOnly) {
		t.Fatalf("want ErrModeReadOnly, got %v", err)
	}
	if err := tg.Commit(ctx, "x/y.MP4"); !errors.Is(err, domain.ErrModeReadOnly) {
		t.Fatalf("want ErrModeReadOnly, got %v", err)
	}
	if err := tg.RemovePart(ctx, "x/y.MP4"); !errors.Is(err, domain.ErrModeReadOnly) {
		t.Fatalf("want ErrModeReadOnly, got %v", err)
	}
	if err := tg.SetModTime(ctx, "x/y.MP4", time.Unix(1700000000, 0)); !errors.Is(err, domain.ErrModeReadOnly) {
		t.Fatalf("want ErrModeReadOnly, got %v", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("dry-run must leave the destination untouched, found %v", entries)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	tg, _ := newTarget(t, runmode.Simulation)
	for _, rel := range []string{"../x", "a/../../x", "/abs", "", "a//b", "./a"} {
		if _, err := tg.Resolve(rel); !errors.Is(err, domain.ErrPathEscapesRoot) {
			t.Errorf("%q: want ErrPathEscapesRoot, got %v", rel, err)
		}
	}
}

func TestUnavailableStorageBlocksWrites(t *testing.T) {
	ctx := context.Background()
	cfg := config.StorageConfig{Type: config.StorageLocal, LocalRoot: filepath.Join(t.TempDir(), "gone"), DestinationRoot: "R-TEST", DestinationLayout: "{filename}"}
	tg, err := New(cfg, runmode.NewGuard(runmode.Simulation, true), sim.LocalProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if h := tg.Health(ctx); h.OK {
		t.Fatal("missing mount point must be unhealthy")
	}
	if _, err := tg.CreatePart(ctx, "a.MP4"); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("want ErrStorageUnavailable, got %v", err)
	}
}
