package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizePatch(t *testing.T) {
	p, err := NormalizePatch(map[string]any{"backup.verify_mode": "sha256", "backup.retry_count": float64(5), "device.auto_backup": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if p["backup.verify_mode"] != "sha256" || p["backup.retry_count"] != 5 || p["device.auto_backup"] != true {
		t.Fatalf("normalized: %v", p)
	}
	for _, bad := range []map[string]any{
		{"server.auth.password_hash": "x"},
		{"backup.verify_mode": "md5"},
		{"backup.retry_count": 1.5},
		{"backup.retry_count": 1000},
		{"backup.reconcile_interval_sec": 0},
		{},
	} {
		if _, err := NormalizePatch(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func TestApplyAndValidate(t *testing.T) {
	cfg, err := Parse([]byte("mode: simulation\nsimulation: {device_source: fixtures, fixtures: {devices: [{key: a, serial: s, volumes: [{type: sdcard, path: /tmp/x}]}]}, allow_local_destination: true}\nstorage: {type: local, local_root: /tmp/nas, destination_root: X}\ndatabase: {path: /tmp/t.db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := NormalizePatch(map[string]any{"backup.verify_mode": "sha256", "logging.level": "debug", "backup.on_conflict": "skip"})
	out, err := cfg.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Backup.VerifyMode != "sha256" || out.Logging.Level != "debug" || out.Backup.OnConflict != "skip" {
		t.Fatalf("apply: %+v", out.Backup)
	}
	if cfg.Backup.VerifyMode != "basic" {
		t.Fatal("original must be untouched")
	}
	if err := out.Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestSetValuesPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	src := `# 顶部注释
mode: "simulation"   # 行尾注释

server:
  listen: "127.0.0.1:8080"
  auth:
    enabled: false   # 开发时免登录

backup:
  verify_mode: "basic"   # basic | sha256
  retry_count: 3
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SetValues(path, map[string]any{
		"backup.verify_mode":            "sha256",
		"backup.retry_count":            7,
		"server.auth.enabled":           true,
		"server.auth.password_hash":     "$2a$12$abc",
		"server.auth.username":          "admin",
		"backup.reconcile_interval_sec": 15,
		"logging.level":                 "debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	for _, want := range []string{
		"# 顶部注释", "# 行尾注释", "# 开发时免登录", "# basic | sha256",
		"verify_mode: sha256", "retry_count: 7", "enabled: true", `password_hash: "$2a$12$abc"`,
		"username: admin", "reconcile_interval_sec: 15", "logging:", "level: debug",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	cfg, err := Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.VerifyMode != "sha256" || cfg.Backup.RetryCount != 7 || !cfg.Server.Auth.Enabled || cfg.Server.Auth.PasswordHash != "$2a$12$abc" || cfg.Logging.Level != "debug" {
		t.Fatalf("reparsed: %+v", cfg)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: %v", info.Mode())
	}
	if err := SetValues(path, map[string]any{"mode.x": 1}); err == nil {
		t.Fatal("setting child of scalar must fail")
	}
}
