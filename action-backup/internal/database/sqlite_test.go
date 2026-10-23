package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "t.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := db.Migrate(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first migrate: n=%d err=%v", n, err)
	}
	n, err = db.Migrate(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second migrate: n=%d err=%v", n, err)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil || v != 1 {
		t.Fatalf("schema version %d %v", v, err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode=%q err=%v", mode, err)
	}
	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	// 状态 CHECK 约束生效
	if _, err := db.ExecContext(ctx, `INSERT INTO source_files(volume_id, relative_path, file_size, modified_time_ns, fingerprint) VALUES(1,'x',1,1,'f')`); err == nil {
		t.Fatal("foreign key to volumes must be enforced")
	}
}
