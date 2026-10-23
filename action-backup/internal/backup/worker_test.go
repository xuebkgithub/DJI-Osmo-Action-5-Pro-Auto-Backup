package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/database"
	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/platform/sim"
	"action-backup/internal/runmode"
	"action-backup/internal/storage"
)

type env struct {
	db     *database.DB
	target *storage.Target
	worker *Worker
	src    string
	sum    string
	task   repository.TaskRecord
}

func newEnv(t *testing.T, size int, mode domain.VerifyMode) *env {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// 源文件
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "DCIM", "DJI_003", "a.MP4")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	data := make([]byte, size)
	_, _ = io.ReadFull(rand.Reader, data)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	st, _ := os.Stat(src)
	// 设备 / 卷 / 文件 / 任务
	devRepo := repository.Devices{DB: db.DB}
	dev := domain.Device{Key: "0506:8002:X", VendorID: "0506", ProductID: "8002", Name: "cam",
		Volumes: []domain.Volume{{Key: "uuid:v1", Type: domain.VolumeSDCard, Filesystem: "exfat", LUN: 1, MountPath: srcDir}}}
	if _, err := devRepo.Upsert(ctx, dev); err != nil {
		t.Fatal(err)
	}
	volID, _ := devRepo.VolumeID(ctx, dev.Key, "uuid:v1")
	f := domain.SourceFile{VolumeID: volID, VolumeKey: "uuid:v1", DeviceKey: dev.Key, RelativePath: "DCIM/DJI_003/a.MP4", Size: int64(size), ModTime: domain.NormalizeModTime(st.ModTime())}
	f.Fingerprint = domain.ComputeFingerprint(dev.Key, "uuid:v1", f.RelativePath, f.Size, f.ModTime)
	fileID, err := repository.SourceFiles{DB: db.DB}.Upsert(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	tasks := repository.Tasks{DB: db.DB}
	taskID, err := tasks.Create(ctx, fileID, "sdcard/"+f.RelativePath, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Transition(ctx, taskID, domain.TaskQueued, repository.TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	target, err := storage.New(config.StorageConfig{Type: config.StorageLocal, LocalRoot: t.TempDir(), DestinationRoot: "R-TEST", DestinationLayout: "{volume_type}/{relative_path}"},
		runmode.NewGuard(runmode.Simulation, true), sim.LocalProbe{})
	if err != nil {
		t.Fatal(err)
	}
	w := New(Options{BufferSize: 64 << 10, CheckpointBytes: 256 << 10, CheckpointInterval: time.Hour},
		Deps{Target: target, Tasks: tasks, Checkpoints: repository.Checkpoints{DB: db.DB}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	task, _ := tasks.Get(ctx, taskID)
	return &env{db: db, target: target, worker: w, src: src, sum: hex.EncodeToString(sum[:]), task: *task}
}

func (e *env) startTransferring(t *testing.T) repository.TaskRecord {
	t.Helper()
	tasks := repository.Tasks{DB: e.db.DB}
	if _, err := tasks.Transition(context.Background(), e.task.ID, domain.TaskTransferring, repository.TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	task, _ := tasks.Get(context.Background(), e.task.ID)
	return *task
}

func (e *env) finalPath(t *testing.T) string {
	t.Helper()
	p, err := e.target.Resolve(e.task.DestinationPath)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fileSHA(t *testing.T, p string) string {
	t.Helper()
	s, err := hashFile(context.Background(), p, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTransferSHA256(t *testing.T) {
	e := newEnv(t, 3<<20+12345, domain.VerifySHA256)
	task := e.startTransferring(t)
	var last int64
	out := e.worker.Run(context.Background(), Job{Task: task, SourcePath: e.src, OnProgress: func(b, _ int64) { last = b }})
	if out.Status != domain.TaskCompleted || out.Err != nil {
		t.Fatalf("outcome %+v", out)
	}
	if out.Checksum != e.sum || last != task.FileSize {
		t.Fatalf("checksum %s want %s, progress %d", out.Checksum, e.sum, last)
	}
	final := e.finalPath(t)
	if fileSHA(t, final) != e.sum {
		t.Fatal("destination content mismatch")
	}
	if _, err := os.Stat(final + storage.PartSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("part file must be renamed away")
	}
	if cp, _ := (repository.Checkpoints{DB: e.db.DB}).Get(context.Background(), task.ID); cp != nil {
		t.Fatal("checkpoint must be deleted after completion")
	}
}

func TestTransferBasic(t *testing.T) {
	e := newEnv(t, 100<<10, domain.VerifyBasic)
	out := e.worker.Run(context.Background(), Job{Task: e.startTransferring(t), SourcePath: e.src})
	if out.Status != domain.TaskCompleted || out.Checksum != "" {
		t.Fatalf("outcome %+v", out)
	}
	if fileSHA(t, e.finalPath(t)) != e.sum {
		t.Fatal("content mismatch")
	}
}

func TestPauseResumeWithCheckpoint(t *testing.T) {
	e := newEnv(t, 2<<20, domain.VerifySHA256)
	task := e.startTransferring(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	out := e.worker.Run(ctx, Job{Task: task, SourcePath: e.src, OnProgress: func(b, _ int64) {
		if b >= 512<<10 {
			cancel(ErrPaused)
		}
	}})
	if out.Status != domain.TaskQueued {
		t.Fatalf("paused outcome %+v", out)
	}
	cp, _ := (repository.Checkpoints{DB: e.db.DB}).Get(context.Background(), task.ID)
	if cp == nil || cp.Offset == 0 || cp.HashState == "" {
		t.Fatalf("checkpoint missing: %+v", cp)
	}
	// 模拟断点之后的脏数据：追加垃圾，恢复时必须截断
	part := e.finalPath(t) + storage.PartSuffix
	f, _ := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.Write([]byte("GARBAGE-GARBAGE"))
	f.Close()

	// 状态机：调度器会把暂停的任务置回 QUEUED，再次执行时 QUEUED -> TRANSFERRING
	tasks := repository.Tasks{DB: e.db.DB}
	for _, st := range []domain.TaskStatus{domain.TaskQueued, domain.TaskTransferring} {
		if _, err := tasks.Transition(context.Background(), task.ID, st, repository.TransitionOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	task2, _ := tasks.Get(context.Background(), task.ID)
	out2 := e.worker.Run(context.Background(), Job{Task: *task2, SourcePath: e.src})
	if out2.Status != domain.TaskCompleted || !out2.Resumed || out2.ResumeAt != cp.Offset {
		t.Fatalf("resume outcome %+v (checkpoint %d)", out2, cp.Offset)
	}
	if out2.Checksum != e.sum || fileSHA(t, e.finalPath(t)) != e.sum {
		t.Fatal("resumed content mismatch")
	}
}

func TestUntrustedCheckpointRestarts(t *testing.T) {
	e := newEnv(t, 1<<20, domain.VerifySHA256)
	task := e.startTransferring(t)
	// 断点声称 900 KiB，但 part 文件只有 10 字节 → 不可信，从头开始
	part, _ := e.target.CreatePart(context.Background(), task.DestinationPath)
	_, _ = part.Write([]byte("0123456789"))
	part.Close()
	_ = (repository.Checkpoints{DB: e.db.DB}).Upsert(context.Background(), repository.Checkpoint{TaskID: task.ID, Offset: 900 << 10, PartPath: "x", HashState: "bogus"})
	out := e.worker.Run(context.Background(), Job{Task: task, SourcePath: e.src})
	if out.Status != domain.TaskCompleted || out.Resumed {
		t.Fatalf("outcome %+v", out)
	}
	if fileSHA(t, e.finalPath(t)) != e.sum {
		t.Fatal("content mismatch")
	}
}

func TestCancelRemovesPart(t *testing.T) {
	e := newEnv(t, 2<<20, domain.VerifyBasic)
	task := e.startTransferring(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	out := e.worker.Run(ctx, Job{Task: task, SourcePath: e.src, OnProgress: func(b, _ int64) {
		if b > 0 {
			cancel(ErrCancelled)
		}
	}})
	if out.Status != domain.TaskCancelled {
		t.Fatalf("outcome %+v", out)
	}
	if _, err := os.Stat(e.finalPath(t) + storage.PartSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("part must be removed on cancel")
	}
}

func TestSourceChangedFails(t *testing.T) {
	e := newEnv(t, 64<<10, domain.VerifyBasic)
	task := e.startTransferring(t)
	// 规划后源文件被改写（大小变化）→ 身份复核失败
	if err := os.WriteFile(e.src, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := e.worker.Run(context.Background(), Job{Task: task, SourcePath: e.src})
	if out.Status != domain.TaskFailed || !IsVerifyError(out.Err) {
		t.Fatalf("outcome %+v", out)
	}
}

func TestMissingSourceIsInterrupted(t *testing.T) {
	e := newEnv(t, 64<<10, domain.VerifyBasic)
	task := e.startTransferring(t)
	out := e.worker.Run(context.Background(), Job{Task: task, SourcePath: e.src + ".gone"})
	if out.Status != domain.TaskInterrupted {
		t.Fatalf("outcome %+v", out)
	}
}

func TestDryRunGuardBlocksTransfer(t *testing.T) {
	e := newEnv(t, 64<<10, domain.VerifyBasic)
	task := e.startTransferring(t)
	ro, _ := storage.New(config.StorageConfig{Type: config.StorageLocal, LocalRoot: t.TempDir(), DestinationRoot: "R-TEST", DestinationLayout: "{volume_type}/{relative_path}"},
		runmode.NewGuard(runmode.DryRun, true), sim.LocalProbe{})
	w := New(Options{}, Deps{Target: ro, Tasks: repository.Tasks{DB: e.db.DB}, Checkpoints: repository.Checkpoints{DB: e.db.DB}})
	out := w.Run(context.Background(), Job{Task: task, SourcePath: e.src})
	if out.Status != domain.TaskFailed || !errors.Is(out.Err, domain.ErrModeReadOnly) {
		t.Fatalf("outcome %+v", out)
	}
}
