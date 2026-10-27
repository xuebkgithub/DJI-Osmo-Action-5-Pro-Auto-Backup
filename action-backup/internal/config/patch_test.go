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

func TestNotificationPatchAndSecret(t *testing.T) {
	cfg, err := Parse([]byte("mode: simulation\nsimulation: {device_source: fixtures, fixtures: {devices: [{key: a, serial: s, volumes: [{type: sdcard, path: /tmp/x}]}]}, allow_local_destination: true}\nstorage: {type: local, local_root: /tmp/nas, destination_root: X}\ndatabase: {path: /tmp/t.db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n := cfg.Notifications; n.Enabled || !n.On.NotifyDeviceAttached() || !n.On.NotifyBackupFailed() || n.MeoW.BaseURL != DefaultMeoWBaseURL || n.Telegram.BaseURL != DefaultTelegramBaseURL {
		t.Fatalf("defaults: %+v", n)
	}
	// 只开总开关、不开渠道 → 校验拒绝
	p, err := NormalizePatch(map[string]any{"notifications.enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := cfg.Apply(p)
	if err := out.Validate(true); err == nil || !strings.Contains(err.Error(), "no channel") {
		t.Fatalf("expected no-channel error, got %v", err)
	}
	// 完整开启 MeoW + Telegram
	p, err = NormalizePatch(map[string]any{
		"notifications.enabled": true, "notifications.on.backup_started": "false", "notifications.site_url": "http://172.20.31.6:8080",
		"notifications.meow.enabled": true, "notifications.meow.nickname": " 熊猫 ",
		"notifications.telegram.enabled": true, "notifications.telegram.bot_token": "123:abc", "notifications.telegram.chat_id": "42", "notifications.telegram.proxy_url": "socks5://10.0.0.1:1080",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = cfg.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(true); err != nil {
		t.Fatal(err)
	}
	n := out.Notifications
	if !n.Enabled || n.On.NotifyBackupStarted() || !n.On.NotifyBackupFinished() || n.MeoW.Nickname != "熊猫" || n.Telegram.BotToken != "123:abc" || n.Telegram.ProxyURL != "socks5://10.0.0.1:1080" {
		t.Fatalf("applied: %+v", n)
	}
	if cfg.Notifications.Enabled || cfg.Notifications.On.BackupStarted == nil || !*cfg.Notifications.On.BackupStarted {
		t.Fatal("original must be untouched")
	}
	// 脱敏与占位回传
	if r := out.Redacted(); r.Notifications.Telegram.BotToken != Masked || out.Notifications.Telegram.BotToken == Masked {
		t.Fatal("bot_token must be redacted in copy only")
	}
	p, err = NormalizePatch(map[string]any{"notifications.telegram.bot_token": Masked, "notifications.telegram.chat_id": "43"})
	if err != nil || len(p) != 1 || p["notifications.telegram.chat_id"] != "43" {
		t.Fatalf("masked token must be dropped: %v %v", p, err)
	}
	if _, err := NormalizePatch(map[string]any{"notifications.telegram.bot_token": Masked}); err == nil {
		t.Fatal("masked-only patch is empty")
	}
	if !IsSecretKey("notifications.telegram.bot_token") || IsSecretKey("notifications.telegram.chat_id") {
		t.Fatal("IsSecretKey")
	}
	// 非法值
	for _, bad := range []map[string]any{
		{"notifications.meow.enabled": true, "notifications.meow.nickname": ""},
		{"notifications.meow.nickname": "a/b"},
		{"notifications.telegram.enabled": true, "notifications.telegram.bot_token": "t"},
		{"notifications.telegram.proxy_url": "10.0.0.1:1080"},
		{"notifications.site_url": "172.20.31.6:8080"},
	} {
		p, err := NormalizePatch(bad)
		if err != nil {
			t.Fatal(err)
		}
		o, _ := cfg.Apply(p)
		if err := o.Validate(true); err == nil {
			t.Fatalf("expected validation error for %v", bad)
		}
	}
	// 写回文件：嵌套键自动创建
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("mode: simulation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetValues(path, map[string]any{"notifications.enabled": true, "notifications.on.backup_failed": false, "notifications.telegram.bot_token": "123:abc"}); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(path)
	if !got.Notifications.Enabled || got.Notifications.On.NotifyBackupFailed() || got.Notifications.Telegram.BotToken != "123:abc" {
		t.Fatalf("written: %+v", got.Notifications)
	}
}
