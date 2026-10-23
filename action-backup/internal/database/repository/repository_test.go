package repository

import (
	"context"
	"path/filepath"
	"testing"

	"action-backup/internal/database"
	"action-backup/internal/domain"
)

func openDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDevicesUpsertAndList(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	r := Devices{DB: db.DB}
	d := domain.Device{Key: "0506:8002:0A1B2C3D", VendorID: "0506", ProductID: "8002", SerialNumber: "0A1B2C3D", Name: "Osmo",
		Volumes: []domain.Volume{
			{Key: "v0", Type: domain.VolumeInternal, Filesystem: "exfat", Label: "OsmoAction", LUN: 0, SizeBytes: 51},
			{Key: "v1", Type: domain.VolumeSDCard, Filesystem: "exfat", Label: "Untitled", LUN: 1, SizeBytes: 124},
		}}
	id1, err := r.Upsert(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	d.Name = "Osmo renamed"
	d.Volumes[1].Label = "NewCard"
	id2, err := r.Upsert(ctx, d)
	if err != nil || id1 != id2 {
		t.Fatalf("upsert must keep id: %d %d %v", id1, id2, err)
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	got := list[0]
	if got.Name != "Osmo renamed" || len(got.Volumes) != 2 || got.Volumes[1].Label != "NewCard" || got.Volumes[0].Type != domain.VolumeInternal {
		t.Errorf("unexpected %+v", got)
	}
}

func TestEventsAndTasks(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	ev := Events{DB: db.DB}
	if _, err := ev.Insert(ctx, EventRecord{Level: "info", Type: "t", Message: "m", Data: map[string]any{"k": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Insert(ctx, EventRecord{Level: "warn", Type: "t2", Message: "m2"}); err != nil {
		t.Fatal(err)
	}
	list, err := ev.ListRecent(ctx, 10)
	if err != nil || len(list) != 2 || list[0].Type != "t2" || list[1].Data["k"] != float64(1) {
		t.Fatalf("events: %v %+v", err, list)
	}
	counts, err := Tasks{DB: db.DB}.CountByStatus(ctx)
	if err != nil || len(counts) != len(domain.AllTaskStatuses) || counts[domain.TaskPending] != 0 {
		t.Fatalf("counts: %v %v", err, counts)
	}
}
