package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"action-backup/internal/config"
)

func TestRunPasswd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	src := "mode: dry-run\nserver:\n  auth:\n    enabled: true   # keep\n    username: admin\n    password_hash: \"$2a$12$REPLACE\"\nstorage: {type: premounted, mount_point: /tmp/x, destination_root: D}\ndatabase: {path: /tmp/t.db}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := runPasswd([]string{"--config", path, "--stdin"}, strings.NewReader("short\n"), &stderr); err == nil {
		t.Fatal("short password must be rejected")
	}
	if err := runPasswd([]string{"--config", path, "--stdin", "--username", "ops"}, strings.NewReader("correct horse battery\n"), &stderr); err != nil {
		t.Fatalf("passwd: %v (%s)", err, stderr.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Auth.Username != "ops" || !cfg.Server.Auth.Enabled {
		t.Fatalf("auth section: %+v", cfg.Server.Auth)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(cfg.Server.Auth.PasswordHash), []byte("correct horse battery")); err != nil {
		t.Fatalf("hash does not verify: %v", err)
	}
	if cost, _ := bcrypt.Cost([]byte(cfg.Server.Auth.PasswordHash)); cost != 12 {
		t.Fatalf("cost = %d", cost)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("config should now validate: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# keep") {
		t.Fatalf("comment lost:\n%s", raw)
	}
	if err := runPasswd([]string{"--config", path}, strings.NewReader("x"), &stderr); err == nil {
		t.Fatal("non-terminal stdin without --stdin must fail")
	}
	if err := runPasswd([]string{"--config", path, "--stdin", "plaintext"}, strings.NewReader("x\n"), &stderr); err == nil {
		t.Fatal("positional argument must be rejected")
	}
}
