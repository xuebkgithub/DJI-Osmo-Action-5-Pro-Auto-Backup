package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
)

type fakeMounter struct {
	mounted   bool
	failMount bool
	mounts    int
	unmounts  int
}

func (f *fakeMounter) MountTarget(context.Context) error {
	f.mounts++
	if f.failMount {
		return errors.New("mount cifs: host unreachable")
	}
	f.mounted = true
	return nil
}

func (f *fakeMounter) UnmountTarget(context.Context) error {
	f.unmounts++
	f.mounted = false
	return nil
}

func TestStorageMountControl(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	// simulation 后端没有目标挂载器：mode 报不支持，接口 501
	code, out := get(t, h, "GET", "/api/v1/system/mode")
	if code != 200 || out["data"].(map[string]any)["storage_mount_control"] != false {
		t.Fatalf("mode without mounter: %d %v", code, out)
	}
	for _, p := range []string{"/api/v1/storage/mount", "/api/v1/storage/remount", "/api/v1/storage/unmount"} {
		if code, out := get(t, h, "POST", p); code != http.StatusNotImplemented {
			t.Fatalf("%s without mounter: %d %v", p, code, out)
		}
	}

	fm := &fakeMounter{}
	a.backend.Storage = fm
	code, out = get(t, h, "GET", "/api/v1/system/mode")
	if code != 200 || out["data"].(map[string]any)["storage_mount_control"] != true {
		t.Fatalf("mode with mounter: %d %v", code, out)
	}

	code, out = get(t, h, "POST", "/api/v1/storage/mount")
	if code != 200 || !fm.mounted || fm.mounts != 1 {
		t.Fatalf("mount: %d %v mounter=%+v", code, out, fm)
	}
	data := out["data"].(map[string]any)
	if data["action"] != "MOUNT" || data["health"].(map[string]any)["ok"] != true {
		t.Fatalf("mount response: %v", data)
	}

	code, out = get(t, h, "POST", "/api/v1/storage/remount")
	if code != 200 || fm.unmounts != 1 || fm.mounts != 2 || !fm.mounted {
		t.Fatalf("remount: %d %v mounter=%+v", code, out, fm)
	}

	code, out = get(t, h, "POST", "/api/v1/storage/unmount")
	if code != 200 || fm.mounted || fm.unmounts != 2 {
		t.Fatalf("unmount: %d %v mounter=%+v", code, out, fm)
	}

	fm.failMount = true
	code, out = get(t, h, "POST", "/api/v1/storage/mount")
	if code != http.StatusServiceUnavailable || out["error"].(map[string]any)["code"] != "STORAGE_MOUNT_FAILED" {
		t.Fatalf("failed mount: %d %v", code, out)
	}
	if _, ok := out["data"].(map[string]any)["health"]; !ok {
		t.Fatalf("failed mount must still carry health: %v", out)
	}

	// 事件：mounted / unmounted / mount_failed 各至少一条
	code, out = get(t, h, "GET", "/api/v1/events?limit=20")
	if code != 200 {
		t.Fatalf("events: %d %v", code, out)
	}
	seen := map[string]bool{}
	for _, e := range out["data"].([]any) {
		seen[e.(map[string]any)["type"].(string)] = true
	}
	for _, want := range []string{"storage.mounted", "storage.unmounted", "storage.mount_failed"} {
		if !seen[want] {
			t.Errorf("missing event %s in %v", want, seen)
		}
	}
}

func TestBackupRetryFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.devices.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	code, out := get(t, h, "POST", "/api/v1/backups/retry-failed")
	if code != 200 || out["data"].(map[string]any)["retried"].(float64) != 0 {
		t.Fatalf("empty retry: %d %v", code, out)
	}

	// 造一个 FAILED 和一个 INTERRUPTED 任务（retry_count 不限：手工重试要覆盖已用尽自动重试的任务）
	devs := a.devices.Attached()
	if len(devs) == 0 || len(devs[0].Volumes) == 0 {
		t.Fatalf("no attached sim device: %+v", devs)
	}
	volID, err := (repository.Devices{DB: a.db.DB}).VolumeID(ctx, devs[0].Key, devs[0].Volumes[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	tasks := repository.Tasks{DB: a.db.DB}
	var ids []int64
	for i, final := range []domain.TaskStatus{domain.TaskFailed, domain.TaskInterrupted} {
		fid, err := (repository.SourceFiles{DB: a.db.DB}).Upsert(ctx, domain.SourceFile{VolumeID: volID, RelativePath: "DCIM/x" + string(rune('a'+i)) + ".MP4", Size: 10, ModTime: time.Now(), Fingerprint: "fp" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		id, err := tasks.Create(ctx, fid, "internal/DCIM/x.MP4", domain.VerifyBasic)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range []domain.TaskStatus{domain.TaskQueued, domain.TaskTransferring, final} {
			if _, err := tasks.Transition(ctx, id, st, repository.TransitionOptions{Error: "boom"}); err != nil {
				t.Fatalf("transition %d -> %s: %v", id, st, err)
			}
		}
		ids = append(ids, id)
	}

	code, out = get(t, h, "POST", "/api/v1/backups/retry-failed")
	data := out["data"].(map[string]any)
	if code != 200 || data["retried"].(float64) != 2 || data["skipped"].(float64) != 0 {
		t.Fatalf("retry: %d %v", code, out)
	}
	for _, id := range ids {
		rec, err := tasks.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != domain.TaskQueued {
			t.Errorf("task %d: %s, want QUEUED", id, rec.Status)
		}
	}
}
