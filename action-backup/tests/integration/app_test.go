//go:build integration

// Package integration 以 simulation 模式启动完整服务并通过真实 HTTP 验证。
package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSimulationServer(t *testing.T) {
	if !faultinject.Enabled {
		t.Skip("integration tests require -tags faultinject")
	}
	dir := t.TempDir()
	for _, p := range []string{"cam/internal/DCIM/DJI_003", "cam/sdcard/DCIM/DJI_003", "nas"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sums := map[string]string{}
	for i, name := range []string{
		"cam/internal/DCIM/DJI_003/DJI_20261023080100_0001_D.MP4",
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080100_0001_D.MP4",
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080200_0002_D.MP4",
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080300_0003_D.MP4",
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080300_0003_D.LRF",
	} {
		data := make([]byte, (1<<20)*(i+1)+i)
		_, _ = rand.Read(data)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(sum[:])
	}
	y := strings.ReplaceAll(`
mode: simulation
server: { listen: "127.0.0.1:0" }
device: { auto_detect: true }
simulation:
  device_source: fixtures
  fixtures:
    attached_on_start: false
    devices:
      - key: sim-cam-1
        serial: SIM0001
        volumes:
          - { type: internal, path: "DIR/cam/internal" }
          - { type: sdcard, path: "DIR/cam/sdcard" }
  allow_local_destination: true
storage: { type: local, local_root: "DIR/nas", destination_root: "DJI-TEST" }
faults: { enabled: true }
database: { path: "DIR/t.db" }
logging: { level: error }
`, "DIR", dir)
	cfg, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := app.New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for a.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	base := "http://" + a.Addr()
	if a.Addr() == "" {
		t.Fatal("server did not start")
	}

	call := func(method, path string) (int, map[string]any) {
		req, _ := http.NewRequest(method, base+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if code, out := call("GET", "/api/v1/system/health"); code != 200 || out["data"].(map[string]any)["status"] != "ok" {
		t.Fatalf("health %d %v", code, out)
	}
	if code, out := call("GET", "/api/v1/devices"); code != 200 || len(out["data"].([]any)) != 0 {
		t.Fatalf("expected no devices before attach: %d %v", code, out)
	}
	if code, _ := call("POST", "/api/v1/test/sim/devices/sim-cam-1/attach"); code != 200 {
		t.Fatalf("attach %d", code)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		code, out := call("GET", "/api/v1/devices")
		if code == 200 && len(out["data"].([]any)) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device did not appear: %d %v", code, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	code, out := call("POST", "/api/v1/backups/plan")
	if code != 200 {
		t.Fatalf("plan %d %v", code, out)
	}
	pd := out["data"].(map[string]any)
	if pd["upload"].(map[string]any)["files"] != float64(4) || pd["skip"].(map[string]any)["files"] != float64(0) || pd["target_checked"] != true {
		t.Fatalf("plan over fixtures: %v", pd)
	}
	for _, it := range pd["items"].([]any) {
		if p := it.(map[string]any)["path"].(string); !strings.HasSuffix(p, ".MP4") {
			t.Fatalf("non-MP4 in plan: %s", p)
		}
	}
	// 真正备份：4 个 MP4 全部 COMPLETED，目标内容与源一致，无残留 .part
	code, out = call("POST", "/api/v1/backups/start")
	if code != 200 || out["data"].(map[string]any)["tasks_created"] != float64(4) || out["data"].(map[string]any)["tasks_enqueued"] != float64(4) {
		t.Fatalf("start %d %v", code, out)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, st := call("GET", "/api/v1/system/status")
		tasks := st["data"].(map[string]any)["tasks"].(map[string]any)
		if tasks["COMPLETED"] == float64(4) {
			break
		}
		if tasks["FAILED"].(float64) > 0 || tasks["INTERRUPTED"].(float64) > 0 || time.Now().After(deadline) {
			_, tl := call("GET", "/api/v1/tasks")
			t.Fatalf("backup did not complete: %v\n%v", tasks, tl)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for name, sum := range sums {
		if strings.HasSuffix(name, ".LRF") {
			continue
		}
		vol := "sdcard"
		if strings.Contains(name, "/internal/") {
			vol = "internal"
		}
		rel := strings.SplitN(name, "/", 2)[1] // internal/DCIM/... 去掉 cam/
		rel = strings.SplitN(rel, "/", 2)[1]   // DCIM/...
		dst := filepath.Join(dir, "nas", "DJI-TEST", vol, filepath.FromSlash(rel))
		b, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("destination missing %s: %v", dst, err)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum {
			t.Fatalf("content mismatch for %s", dst)
		}
		if _, err := os.Stat(dst + ".part"); err == nil {
			t.Fatalf("stale part file %s", dst)
		}
	}
	_, hist := call("GET", "/api/v1/backups/history")
	if hist["data"].(map[string]any)["total"] != float64(4) {
		t.Fatalf("history %v", hist)
	}
	// 再次 start：全部 skip，不新建任务
	code, out = call("POST", "/api/v1/backups/start")
	d2 := out["data"].(map[string]any)
	if code != 200 || d2["tasks_created"] != float64(0) || d2["plan"].(map[string]any)["skip"].(map[string]any)["files"] != float64(4) {
		t.Fatalf("second start should skip everything: %d %v", code, d2)
	}
	// 任务详情与非法转移
	_, tk := call("GET", "/api/v1/tasks/1")
	if tk["data"].(map[string]any)["task"].(map[string]any)["status"] != "COMPLETED" {
		t.Fatalf("task 1 %v", tk)
	}
	if code, _ := call("POST", "/api/v1/tasks/1/retry"); code != 409 {
		t.Fatalf("retry of COMPLETED must 409, got %d", code)
	}
	if code, _ := call("POST", "/api/v1/tasks/999/cancel"); code != 404 {
		t.Fatalf("cancel of unknown must 404, got %d", code)
	}
	if code, _ := call("POST", "/api/v1/backups/pause"); code != 200 {
		t.Fatalf("pause %d", code)
	}
	if _, st := call("GET", "/api/v1/system/status"); st["data"].(map[string]any)["scheduler"].(map[string]any)["paused"] != true {
		t.Fatal("scheduler should be paused")
	}
	if code, _ := call("POST", "/api/v1/backups/resume"); code != 200 {
		t.Fatalf("resume %d", code)
	}

	if code, _ := call("POST", "/api/v1/test/sim/devices/sim-cam-1/detach"); code != 200 {
		t.Fatalf("detach %d", code)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		_, out := call("GET", "/api/v1/devices")
		if len(out["data"].([]any)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("device did not disappear")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, out = call("GET", "/api/v1/events?limit=50")
	types := map[string]bool{}
	for _, e := range out["data"].([]any) {
		types[e.(map[string]any)["type"].(string)] = true
	}
	for _, want := range []string{"system.started", "device.attached", "device.detached", "plan.generated", "backup.started", "task.started", "backup.completed", "backup.finished"} {
		if !types[want] {
			t.Errorf("missing event %s in %v", want, types)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}
