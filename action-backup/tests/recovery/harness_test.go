//go:build faultinject && recovery

// Package recovery 用故障注入验证断点恢复、重试与状态机。
package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"action-backup/internal/app"
	"action-backup/internal/config"
	"action-backup/internal/faultinject"
)

const baseYAML = `
mode: simulation
server: { listen: "127.0.0.1:0" }
device: { auto_detect: true, auto_backup: false }
simulation:
  device_source: fixtures
  fixtures:
    attached_on_start: true
    devices:
      - key: sim-cam-1
        serial: SIM0001
        volumes:
          - { type: internal, path: "DIR/cam/internal" }
          - { type: sdcard, path: "DIR/cam/sdcard" }
  allow_local_destination: true
storage: { type: local, local_root: "DIR/nas", destination_root: "DJI-TEST" }
backup:
  verify_mode: sha256
  buffer_size_mb: 1
  checkpoint_bytes_mb: 1
  checkpoint_interval_sec: 1
  retry_count: 3
  reconcile_interval_sec: 1
faults: { enabled: true, rules: [] }
database: { path: "DIR/t.db" }
logging: { level: error }
`

type harness struct {
	t      *testing.T
	dir    string
	cfg    *config.Config
	app    *app.App
	base   string
	cancel context.CancelFunc
	done   chan error
	sums   map[string]string // relative path (DCIM/...) -> sha256
}

// newHarness 生成源文件、配置并启动服务。files: 相对 sdcard 卷根的路径 -> 字节数。
func newHarness(t *testing.T, files map[string]int, mutate func(*config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"cam/internal/DCIM/DJI_003", "cam/sdcard/DCIM/DJI_003", "nas"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{t: t, dir: dir, sums: map[string]string{}}
	for rel, n := range files {
		data := make([]byte, n)
		_, _ = io.ReadFull(rand.Reader, data)
		p := filepath.Join(dir, "cam", "sdcard", filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(data)
		h.sums[rel] = hex.EncodeToString(s[:])
	}
	cfg, err := config.Parse([]byte(strings.ReplaceAll(baseYAML, "DIR", dir)))
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(true); err != nil {
		t.Fatal(err)
	}
	h.cfg = cfg
	h.start()
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, h.cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		cancel()
		h.t.Fatal(err)
	}
	h.app, h.cancel, h.done = a, cancel, make(chan error, 1)
	go func() { h.done <- a.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for a.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Addr() == "" {
		h.t.Fatal("server did not start")
	}
	h.base = "http://" + a.Addr()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("shutdown timed out")
	}
	h.cancel = nil
}

func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) addRule(r faultinject.Rule) {
	h.t.Helper()
	if code, out := h.call("POST", "/api/v1/test/faults", r); code != 200 {
		h.t.Fatalf("add rule %s: %d %v", r.ID, code, out)
	}
}

func (h *harness) startBackup() map[string]any {
	h.t.Helper()
	code, out := h.call("POST", "/api/v1/backups/start", nil)
	if code != 200 {
		h.t.Fatalf("start: %d %v", code, out)
	}
	return out["data"].(map[string]any)
}

func (h *harness) task(id int64) map[string]any {
	h.t.Helper()
	code, out := h.call("GET", fmt.Sprintf("/api/v1/tasks/%d", id), nil)
	if code != 200 {
		h.t.Fatalf("task %d: %d %v", id, code, out)
	}
	return out["data"].(map[string]any)["task"].(map[string]any)
}

// waitStatus 等待任务进入某状态。
func (h *harness) waitStatus(id int64, timeout time.Duration, want string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = h.task(id)
		if last["status"] == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("task %d did not reach %s within %v; last=%v", id, want, timeout, last)
	return nil
}

func (h *harness) waitStatusIn(id int64, timeout time.Duration, want ...string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = h.task(id)
		for _, w := range want {
			if last["status"] == w {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("task %d did not reach %v within %v; last=%v", id, want, timeout, last)
	return nil
}

func (h *harness) destPath(rel string) string {
	return filepath.Join(h.dir, "nas", "DJI-TEST", "sdcard", filepath.FromSlash(rel))
}

func (h *harness) assertDestination(rel string) {
	h.t.Helper()
	b, err := os.ReadFile(h.destPath(rel))
	if err != nil {
		h.t.Fatalf("destination %s: %v", rel, err)
	}
	s := sha256.Sum256(b)
	if hex.EncodeToString(s[:]) != h.sums[rel] {
		h.t.Fatalf("destination %s content mismatch", rel)
	}
	if _, err := os.Stat(h.destPath(rel) + ".part"); err == nil {
		h.t.Fatalf("stale part file for %s", rel)
	}
}

func (h *harness) events(limit int) []map[string]any {
	h.t.Helper()
	_, out := h.call("GET", fmt.Sprintf("/api/v1/events?limit=%d", limit), nil)
	var evs []map[string]any
	for _, e := range out["data"].([]any) {
		evs = append(evs, e.(map[string]any))
	}
	return evs
}

func (h *harness) hasEvent(typ string, pred func(map[string]any) bool) bool {
	for _, e := range h.events(200) {
		if e["type"] == typ && (pred == nil || pred(e)) {
			return true
		}
	}
	return false
}

func mib(n int) int { return n << 20 }

const bigFile = "DCIM/DJI_003/DJI_20261023080100_0001_D.MP4"
