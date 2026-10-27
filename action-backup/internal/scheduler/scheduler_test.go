package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"action-backup/internal/backup"
	"action-backup/internal/config"
	"action-backup/internal/database"
	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/platform/sim"
	"action-backup/internal/runmode"
	"action-backup/internal/storage"
)

type fakeDevices struct{ devs []domain.Device }

func (f fakeDevices) Attached() []domain.Device { return f.devs }

type env struct {
	db    *database.DB
	sched *Scheduler
	tasks repository.Tasks
	ids   []int64
	ctx   context.Context
}

func newEnv(t *testing.T, files int, size int) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	dev := domain.Device{Key: "0506:8002:X", VendorID: "0506", ProductID: "8002", Name: "cam",
		Volumes: []domain.Volume{{Key: "uuid:v1", Type: domain.VolumeSDCard, Filesystem: "exfat", LUN: 1, MountPath: srcDir}}}
	devRepo := repository.Devices{DB: db.DB}
	if _, err := devRepo.Upsert(ctx, dev); err != nil {
		t.Fatal(err)
	}
	volID, _ := devRepo.VolumeID(ctx, dev.Key, "uuid:v1")
	tasks := repository.Tasks{DB: db.DB}
	var ids []int64
	for i := 0; i < files; i++ {
		rel := filepath.Join("DCIM", "DJI_003", "f"+string(rune('a'+i))+".MP4")
		p := filepath.Join(srcDir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		data := make([]byte, size)
		_, _ = io.ReadFull(rand.Reader, data)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		f := domain.SourceFile{VolumeID: volID, VolumeKey: "uuid:v1", DeviceKey: dev.Key, RelativePath: filepath.ToSlash(rel), Size: int64(size), ModTime: domain.NormalizeModTime(st.ModTime())}
		f.Fingerprint = domain.ComputeFingerprint(dev.Key, "uuid:v1", f.RelativePath, f.Size, f.ModTime)
		fid, err := (repository.SourceFiles{DB: db.DB}).Upsert(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		id, err := tasks.Create(ctx, fid, "sdcard/"+f.RelativePath, domain.VerifySHA256)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	target, _ := storage.New(config.StorageConfig{Type: config.StorageLocal, LocalRoot: t.TempDir(), DestinationRoot: "R-TEST", DestinationLayout: "{volume_type}/{relative_path}"},
		runmode.NewGuard(runmode.Simulation, true), sim.LocalProbe{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := backup.New(backup.Options{BufferSize: 64 << 10, CheckpointBytes: 128 << 10, CheckpointInterval: time.Hour},
		backup.Deps{Target: target, Tasks: tasks, Checkpoints: repository.Checkpoints{DB: db.DB}, Log: log})
	bus := events.New(repository.Events{DB: db.DB}, log)
	s := New(tasks, w, fakeDevices{[]domain.Device{dev}}, sim.MountPassthrough{}, bus, log)
	s.Start(ctx)
	return &env{db: db, sched: s, tasks: tasks, ids: ids, ctx: ctx}
}

func (e *env) status(t *testing.T, id int64) domain.TaskStatus {
	t.Helper()
	tk, err := e.tasks.Get(e.ctx, id)
	if err != nil || tk == nil {
		t.Fatalf("task %d: %v", id, err)
	}
	return tk.Status
}

func TestRunsQueueInOrder(t *testing.T) {
	e := newEnv(t, 3, 300<<10)
	n, err := e.sched.Enqueue(e.ctx, e.ids)
	if err != nil || n != 3 {
		t.Fatalf("enqueue %d %v", n, err)
	}
	wctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	if err := e.sched.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range e.ids {
		if st := e.status(t, id); st != domain.TaskCompleted {
			t.Fatalf("task %d: %s", id, st)
		}
	}
	if st := e.sched.State(); st.Current != nil || !st.Running {
		t.Fatalf("state %+v", st)
	}
}

func TestRetryGuardAndCancelQueued(t *testing.T) {
	e := newEnv(t, 2, 64<<10)
	// PENDING 任务不能 retry
	if err := e.sched.Retry(e.ctx, e.ids[0]); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("retry pending: %v", err)
	}
	if err := e.sched.Retry(e.ctx, 999); err == nil {
		t.Fatal("unknown task")
	}
	// 暂停后入队：任务停留在 QUEUED，可取消
	e.sched.Pause()
	if _, err := e.sched.Enqueue(e.ctx, e.ids); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if st := e.status(t, e.ids[0]); st != domain.TaskQueued {
		t.Fatalf("paused scheduler must not start tasks: %s", st)
	}
	if err := e.sched.Cancel(e.ctx, e.ids[1]); err != nil {
		t.Fatal(err)
	}
	if st := e.status(t, e.ids[1]); st != domain.TaskCancelled {
		t.Fatalf("cancelled: %s", st)
	}
	e.sched.Resume()
	wctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	if err := e.sched.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	if st := e.status(t, e.ids[0]); st != domain.TaskCompleted {
		t.Fatalf("after resume: %s", st)
	}
	// COMPLETED 不能 retry
	if err := e.sched.Retry(e.ctx, e.ids[0]); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("retry completed: %v", err)
	}
}

func TestBackupFinishedSummary(t *testing.T) {
	e := newEnv(t, 3, 64<<10)
	sub, cancel := e.sched.bus.Subscribe(64)
	defer cancel()
	if _, err := e.sched.Enqueue(e.ctx, e.ids); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type != "backup.finished" {
				continue
			}
			if ev.Data["completed"] != 3 || ev.Data["failed"] != 0 || ev.Data["bytes"] != int64(3*64<<10) || ev.Level != "info" {
				t.Fatalf("summary %+v", ev)
			}
			for _, id := range e.ids {
				if e.status(t, id) != domain.TaskCompleted {
					t.Fatalf("summary published before task %d completed", id)
				}
			}
			// 队列空闲时不再重复发
			time.Sleep(2500 * time.Millisecond)
			for {
				select {
				case ev := <-sub:
					if ev.Type == "backup.finished" {
						t.Fatal("duplicate backup.finished on idle queue")
					}
					continue
				default:
				}
				return
			}
		case <-deadline:
			t.Fatal("no backup.finished")
		}
	}
}
