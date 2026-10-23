package config

import (
	"strings"
	"testing"
)

const base = `
mode: "%s"
server:
  listen: "127.0.0.1:0"
  auth:
    enabled: %v
    username: "admin"
    password_hash: "%s"
device:
  auto_detect: true
%s
backup:
  file_extensions: ["MP4", ".lrf"]
storage:
  type: "%s"
  share: "//192.168.1.100/VideoBackup"
  mount_point: "/mnt/nas"
  credentials_file: "/etc/action-backup/smb.credentials"
  local_root: "./dev/nas"
  destination_root: "%s"
  destination_layout: "%s"
%s
database:
  path: "./dev/x.db"
`

const simBlock = `
simulation:
  device_source: fixtures
  fixtures:
    devices:
      - key: cam
        serial: SIM1
        volumes:
          - { type: internal, path: ./a }
          - { type: sdcard, path: ./b }
  allow_local_destination: true
`

const bcrypt = "$2a$12$abcdefghijklmnopqrstuvABCDEFGHIJKLMNOPQRSTUVWXYZ01234"

func build(mode string, auth bool, hash, sim, storage, root, layout, faults string) string {
	return strings.TrimSpace(strings.ReplaceAll(
		sprintf(base, mode, auth, hash, sim, storage, root, layout, faults), "\t", "  "))
}

func sprintf(f string, a ...any) string { return fmtSprintf(f, a...) }

func TestDefaultsAndNormalization(t *testing.T) {
	cfg, err := Parse([]byte(build("simulation", false, "", simBlock, "local", "X-TEST", "{volume_type}/{relative_path}", "")))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Backup.FileExtensions; len(got) != 2 || got[0] != ".mp4" || got[1] != ".lrf" {
		t.Errorf("extensions not normalized: %v", got)
	}
	if cfg.Backup.WorkerCount != 1 || cfg.Backup.BufferSizeMB != 4 || cfg.Storage.SMBVersion != "auto" || cfg.Logging.Level != "info" {
		t.Errorf("defaults missing: %+v", cfg.Backup)
	}
	if !strings.HasPrefix(cfg.Database.Path, "/") || !strings.HasPrefix(cfg.Simulation.Fixtures.Devices[0].Volumes[0].Path, "/") {
		t.Errorf("relative paths must be resolved: %s", cfg.Database.Path)
	}
	if len(cfg.Backup.IgnorePatterns) == 0 {
		t.Error("default ignore patterns missing")
	}
	if !cfg.Backup.IncludesInternal() || !cfg.Backup.IncludesSDCard() {
		t.Error("include flags default to true")
	}
	off, _ := Parse([]byte("mode: dry-run\nbackup: {include_internal: false}\n"))
	if off.Backup.IncludesInternal() || !off.Backup.IncludesSDCard() {
		t.Error("explicit false must be honoured")
	}
	if err := cfg.Validate(true); err != nil {
		t.Fatalf("valid simulation config rejected: %v", err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	if _, err := Parse([]byte("mode: simulation\nbogus: 1\n")); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestModeCombinations(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		fiBuild bool
		wantErr string
	}{
		{"production ok", build("production", true, bcrypt, "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, ""},
		{"production forbids local", build("production", true, bcrypt, "", "local", "DJI", "{volume_type}/{relative_path}", ""), false, "forbids storage.type local"},
		{"production forbids simulation", build("production", true, bcrypt, simBlock, "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, "forbids the simulation section"},
		{"production requires auth", build("production", false, "", "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, "requires server.auth.enabled"},
		{"auth needs bcrypt", build("production", true, "plain", "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, "bcrypt"},
		{"dry-run ok", build("dry-run", false, "", "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, ""},
		{"dry-run forbids local", build("dry-run", false, "", "", "local", "DJI", "{volume_type}/{relative_path}", ""), false, "forbids storage.type local"},
		{"simulation needs block", build("simulation", false, "", "", "local", "X-TEST", "{volume_type}/{relative_path}", ""), true, "requires the simulation section"},
		{"simulation real share needs -TEST", build("simulation", false, "", simBlock, "premounted", "DJI", "{volume_type}/{relative_path}", ""), true, "-TEST"},
		{"simulation premounted -TEST ok", build("simulation", false, "", simBlock, "premounted", "DJI-TEST", "{volume_type}/{relative_path}", ""), true, ""},
		{"faults need build tag", build("simulation", false, "", simBlock, "local", "X-TEST", "{volume_type}/{relative_path}", "faults:\n  enabled: true"), false, "faultinject"},
		{"faults with build tag ok", build("simulation", false, "", simBlock, "local", "X-TEST", "{volume_type}/{relative_path}", "faults:\n  enabled: true"), true, ""},
		{"bad layout", build("simulation", false, "", simBlock, "local", "X-TEST", "{volume_type}", ""), true, "relative_path"},
		{"smb1 forbidden", strings.Replace(build("dry-run", false, "", "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), "credentials_file", "smb_version: \"1.0\"\n  credentials_file", 1), false, "smb_version 1.0"},
		{"bad mode", build("test", false, "", "", "cifs", "DJI", "{volume_type}/{relative_path}", ""), false, "unknown mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.yaml))
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, c.yaml)
			}
			err = cfg.Validate(c.fiBuild)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestShippedConfigs(t *testing.T) {
	for _, c := range []struct {
		file    string
		fiBuild bool
	}{
		{"../../configs/config.dev.yaml", true},
		{"../../configs/config.dryrun.yaml", false},
	} {
		cfg, err := Load(c.file)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if err := cfg.Validate(c.fiBuild); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
	}
	// production 样例的 password_hash 是占位符，必须被拒绝，防止直接上线
	cfg, err := Load("../../configs/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "bcrypt") {
		t.Fatalf("example config placeholder hash must be rejected, got %v", err)
	}
}

func TestRedacted(t *testing.T) {
	cfg, _ := Parse([]byte(build("production", true, bcrypt, "", "cifs", "DJI", "{volume_type}/{relative_path}", "")))
	if cfg.Redacted().Server.Auth.PasswordHash != "***" || cfg.Server.Auth.PasswordHash == "***" {
		t.Fatal("redaction must not mutate original")
	}
}
