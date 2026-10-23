package syncengine

import (
	"context"
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

type fixture struct {
	db       *database.DB
	target   *storage.Target
	volumeID int64
	file     domain.SourceFile
}

func setup(t *testing.T) fixture {
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
	devRepo := repository.Devices{DB: db.DB}
	dev := domain.Device{Key: "0506:8002:X", VendorID: "0506", ProductID: "8002", Name: "cam",
		Volumes: []domain.Volume{{Key: "uuid:v1", Type: domain.VolumeSDCard, Filesystem: "exfat", LUN: 1}}}
	if _, err := devRepo.Upsert(ctx, dev); err != nil {
		t.Fatal(err)
	}
	volumeID, _ := devRepo.VolumeID(ctx, dev.Key, "uuid:v1")
	root := t.TempDir()
	target, err := storage.New(config.StorageConfig{Type: config.StorageLocal, LocalRoot: root, DestinationRoot: "R-TEST", DestinationLayout: "{volume_type}/{relative_path}"},
		runmode.NewGuard(runmode.Simulation, true), sim.LocalProbe{})
	if err != nil {
		t.Fatal(err)
	}
	f := domain.SourceFile{VolumeID: volumeID, VolumeKey: "uuid:v1", DeviceKey: dev.Key, RelativePath: "DCIM/DJI_003/a.MP4", Size: 5, ModTime: time.Unix(1700000000, 0).UTC()}
	f.Fingerprint = domain.ComputeFingerprint(dev.Key, "uuid:v1", f.RelativePath, f.Size, f.ModTime)
	return fixture{db: db, target: target, volumeID: volumeID, file: f}
}

func (fx fixture) insertFile(t *testing.T, f domain.SourceFile) int64 {
	t.Helper()
	res, err := fx.db.ExecContext(context.Background(), `INSERT INTO source_files(volume_id, relative_path, file_size, modified_time_ns, fingerprint) VALUES(?,?,?,?,?)`,
		f.VolumeID, f.RelativePath, f.Size, f.ModTime.UnixNano(), f.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (fx fixture) insertTask(t *testing.T, fileID int64, dest, status string) {
	t.Helper()
	if _, err := fx.db.ExecContext(context.Background(), `INSERT INTO backup_tasks(file_id, destination_path, status) VALUES(?,?,?)`, fileID, dest, status); err != nil {
		t.Fatal(err)
	}
}

func (fx fixture) writeDest(t *testing.T, rel string, n int) {
	t.Helper()
	abs, err := fx.target.Resolve(rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decide(t *testing.T, fx fixture, checked bool) (domain.PlanAction, string) {
	t.Helper()
	c := Comparator{Files: repository.SourceFiles{DB: fx.db.DB}, Tasks: repository.Tasks{DB: fx.db.DB}, Target: fx.target, TargetChecked: checked}
	a, r, err := c.Decide(context.Background(), fx.volumeID, fx.file, "sdcard/"+fx.file.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	return a, r
}

func TestNotBackedUp(t *testing.T) {
	fx := setup(t)
	if a, r := decide(t, fx, true); a != domain.PlanUpload || r != ReasonNotBackedUp {
		t.Fatalf("%s %s", a, r)
	}
}

func TestDestinationExistsWithoutRecord(t *testing.T) {
	fx := setup(t)
	fx.writeDest(t, "sdcard/"+fx.file.RelativePath, 5)
	if a, r := decide(t, fx, true); a != domain.PlanConflict || r != ReasonDestExistsNoRecord {
		t.Fatalf("%s %s", a, r)
	}
}

func TestAlreadyBackedUp(t *testing.T) {
	fx := setup(t)
	id := fx.insertFile(t, fx.file)
	fx.insertTask(t, id, "old-layout/a.MP4", "COMPLETED")
	fx.writeDest(t, "old-layout/a.MP4", 5)
	if a, r := decide(t, fx, true); a != domain.PlanSkip || r != ReasonAlreadyBackedUp {
		t.Fatalf("%s %s", a, r)
	}
	// 目标不可用时不能跳过
	if a, r := decide(t, fx, false); a != domain.PlanUpload || r != ReasonTargetUnverified {
		t.Fatalf("%s %s", a, r)
	}
}

func TestTargetMissingAndSizeMismatch(t *testing.T) {
	fx := setup(t)
	id := fx.insertFile(t, fx.file)
	fx.insertTask(t, id, "sdcard/"+fx.file.RelativePath, "COMPLETED")
	if a, r := decide(t, fx, true); a != domain.PlanUpload || r != ReasonTargetMissing {
		t.Fatalf("%s %s", a, r)
	}
	fx.writeDest(t, "sdcard/"+fx.file.RelativePath, 99)
	if a, r := decide(t, fx, true); a != domain.PlanConflict || r != ReasonTargetSizeMismatch {
		t.Fatalf("%s %s", a, r)
	}
}

func TestIncompleteAndChanged(t *testing.T) {
	fx := setup(t)
	id := fx.insertFile(t, fx.file)
	fx.insertTask(t, id, "sdcard/"+fx.file.RelativePath, "INTERRUPTED")
	if a, r := decide(t, fx, true); a != domain.PlanUpload || r != ReasonIncomplete {
		t.Fatalf("%s %s", a, r)
	}
	changed := fx.file
	changed.Size = 6
	changed.Fingerprint = domain.ComputeFingerprint(changed.DeviceKey, changed.VolumeKey, changed.RelativePath, changed.Size, changed.ModTime)
	fx.file = changed
	if a, r := decide(t, fx, true); a != domain.PlanUpload || r != ReasonChanged {
		t.Fatalf("%s %s", a, r)
	}
	fx.writeDest(t, "sdcard/"+fx.file.RelativePath, 5)
	if a, r := decide(t, fx, true); a != domain.PlanConflict || r != ReasonChangedDestExists {
		t.Fatalf("%s %s", a, r)
	}
}

func TestUnknownVolumeIsNotBackedUp(t *testing.T) {
	fx := setup(t)
	c := Comparator{Files: repository.SourceFiles{DB: fx.db.DB}, Tasks: repository.Tasks{DB: fx.db.DB}, Target: fx.target, TargetChecked: true}
	a, r, err := c.Decide(context.Background(), 0, fx.file, "x/a.MP4")
	if err != nil || a != domain.PlanUpload || r != ReasonNotBackedUp {
		t.Fatalf("%s %s %v", a, r, err)
	}
}
