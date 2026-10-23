package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"action-backup/internal/config"
	"action-backup/internal/faultinject"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"cam/internal/DCIM/DJI_003", "cam/sdcard/DCIM/DJI_003", "nas"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, n := range map[string]int{
		"cam/internal/DCIM/DJI_003/DJI_20261023080100_0001_D.MP4": 10,
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080200_0002_D.MP4":   20,
		"cam/sdcard/DCIM/DJI_003/DJI_20261023080200_0002_D.LRF":   5,
		"cam/sdcard/DCIM/DJI_003/._DJI_20261023080200_0002_D.MP4": 1,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	y := strings.ReplaceAll(`
mode: simulation
server: { listen: "127.0.0.1:0" }
device: { auto_detect: true }
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
storage:
  type: local
  local_root: "DIR/nas"
  destination_root: "DJI-TEST"
database: { path: "DIR/t.db" }
logging: { level: error, format: text }
`, "DIR", dir)
	cfg, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(faultinject.Enabled); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func cfgDir(t *testing.T, a *App) string {
	t.Helper()
	return a.cfg.Database.Path
}

func get(t *testing.T, h http.Handler, method, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s %s: bad json %q: %v", method, path, body, err)
	}
	return rec.Code, out
}

func TestAppEndpoints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, testConfig(t), log)
	if err != nil {
		t.Fatal(err)
	}
	// 不调用 Run（避免真实监听），手动启动设备管理器以验证事件链路
	if err := a.devices.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	code, out := get(t, h, "GET", "/api/v1/system/mode")
	data := out["data"].(map[string]any)
	if code != 200 || data["mode"] != "simulation" || data["storage_backend"] != "local" || data["destination_write_allowed"] != true {
		t.Fatalf("mode: %d %v", code, out)
	}
	if data["faultinject_build"] != faultinject.Enabled {
		t.Fatalf("faultinject_build mismatch: %v", data)
	}

	code, out = get(t, h, "GET", "/api/v1/system/health")
	data = out["data"].(map[string]any)
	if code != 200 || data["status"] != "ok" {
		t.Fatalf("health: %d %v", code, out)
	}
	if st := data["storage"].(map[string]any); st["ok"] != true || st["kind"] != "local" {
		t.Fatalf("storage health: %v", st)
	}

	code, out = get(t, h, "GET", "/api/v1/system/status")
	data = out["data"].(map[string]any)
	if code != 200 || data["schema_version"] != float64(1) || data["devices_attached"] != float64(1) {
		t.Fatalf("status: %d %v", code, out)
	}
	if tasks := data["tasks"].(map[string]any); len(tasks) != 9 {
		t.Fatalf("tasks map should list all statuses: %v", tasks)
	}

	code, out = get(t, h, "GET", "/api/v1/devices")
	devs := out["data"].([]any)
	if code != 200 || len(devs) != 1 {
		t.Fatalf("devices: %d %v", code, out)
	}
	dev := devs[0].(map[string]any)
	vols := dev["volumes"].([]any)
	if dev["key"] != "0506:8002:SIM0001" || len(vols) != 2 || vols[0].(map[string]any)["type"] != "internal" || vols[1].(map[string]any)["type"] != "sdcard" {
		t.Fatalf("device: %v", dev)
	}

	code, out = get(t, h, "GET", "/api/v1/events?limit=10")
	evs := out["data"].([]any)
	if code != 200 || len(evs) == 0 || evs[0].(map[string]any)["type"] != "device.attached" {
		t.Fatalf("events: %d %v", code, out)
	}
	if code, _ := get(t, h, "GET", "/api/v1/events?limit=0"); code != 400 {
		t.Fatalf("bad limit should 400, got %d", code)
	}
	if code, _ := get(t, h, "GET", "/api/v1/nope"); code != 404 {
		t.Fatalf("unknown api should 404, got %d", code)
	}

	// 计划：2 个 MP4 上传，LRF 与 ._ 文件被忽略
	if code, _ := get(t, h, "GET", "/api/v1/backups/plan"); code != 404 {
		t.Fatalf("no plan yet should 404, got %d", code)
	}
	code, out = get(t, h, "POST", "/api/v1/backups/plan")
	data = out["data"].(map[string]any)
	if code != 200 || data["target_checked"] != true || data["scanned_files"] != float64(2) {
		t.Fatalf("plan: %d %v", code, out)
	}
	if up := data["upload"].(map[string]any); up["files"] != float64(2) || up["bytes"] != float64(30) {
		t.Fatalf("plan upload: %v", up)
	}
	items := data["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["destination"] != "internal/DCIM/DJI_003/DJI_20261023080100_0001_D.MP4" || items[0].(map[string]any)["reason"] != "not_backed_up" {
		t.Fatalf("plan items: %v", items)
	}
	if code, out := get(t, h, "GET", "/api/v1/backups/plan"); code != 200 || out["data"].(map[string]any)["volumes"] != float64(2) {
		t.Fatalf("last plan: %d %v", code, out)
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(cfgDir(t, a)), "nas")); len(entries) != 0 {
		t.Fatalf("plan must not write to destination: %v", entries)
	}

	// 测试接口只在 faultinject 构建中存在
	code, out = get(t, h, "POST", "/api/v1/test/sim/devices/sim-cam-1/detach")
	if faultinject.Enabled {
		if code != 200 || out["data"].(map[string]any)["attached"] != false {
			t.Fatalf("detach: %d %v", code, out)
		}
		if code, _ := get(t, h, "POST", "/api/v1/test/sim/devices/nope/attach"); code != 404 {
			t.Fatalf("unknown sim device should 404, got %d", code)
		}
	} else if code != 404 {
		t.Fatalf("test endpoints must be absent without faultinject tag, got %d", code)
	}

	// 静态占位页
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Action Backup") {
		t.Fatalf("index: %d", rec.Code)
	}
}
