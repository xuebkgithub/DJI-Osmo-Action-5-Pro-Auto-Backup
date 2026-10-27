package app

import (
	"bufio"
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
	"time"

	"golang.org/x/crypto/bcrypt"

	"action-backup/internal/api/auth"
	"action-backup/internal/config"
	"action-backup/internal/events"
	"action-backup/internal/faultinject"
)

// writeTestConfig 把 simulation 配置写到文件并 Load，使 cfg.Path 非空（PATCH /config 会写回文件）。
func writeTestConfig(t *testing.T, authEnabled bool) *config.Config {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"cam/sdcard/DCIM/DJI_003", "nas"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cam/sdcard/DCIM/DJI_003/DJI_20261023080200_0002_D.MP4"), make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	y := strings.NewReplacer("DIR", dir, "HASH", string(hash), "AUTH", map[bool]string{true: "true", false: "false"}[authEnabled]).Replace(`# 测试配置
mode: simulation
server:
  listen: "127.0.0.1:0"
  auth:
    enabled: AUTH   # 认证开关
    username: admin
    password_hash: "HASH"
    login_rate_limit_per_min: 5
device: { auto_detect: true, auto_backup: false }
simulation:
  device_source: fixtures
  fixtures:
    attached_on_start: true
    devices:
      - key: sim-cam-1
        serial: SIM0001
        volumes:
          - { type: sdcard, path: "DIR/cam/sdcard" }
  allow_local_destination: true
storage:
  type: local
  local_root: "DIR/nas"
  destination_root: "DJI-TEST"
backup:
  verify_mode: basic   # basic | sha256
database: { path: "DIR/t.db" }
logging: { level: error, format: text }
`)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(faultinject.Enabled); err != nil {
		t.Fatal(err)
	}
	return cfg
}

type client struct {
	t      *testing.T
	h      http.Handler
	cookie string
	csrf   string
}

func (c *client) do(method, path, body string) (int, map[string]any, *http.Response) {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "192.168.1.50:40000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
	}
	if c.csrf != "" {
		req.Header.Set(auth.CSRFHeader, c.csrf)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Result()
}

func (c *client) login(user, pass string) (int, map[string]any) {
	c.t.Helper()
	code, out, resp := c.do("POST", "/api/v1/auth/login", `{"username":"`+user+`","password":"`+pass+`"}`)
	if code == 200 {
		for _, ck := range resp.Cookies() {
			if ck.Name == auth.CookieName {
				c.cookie = ck.Value
				if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
					c.t.Fatalf("cookie attributes: %+v", ck)
				}
			}
		}
		c.csrf, _ = out["data"].(map[string]any)["csrf_token"].(string)
	}
	return code, out
}

func newApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), WithLogLevel(new(slog.LevelVar)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.devices.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return a
}

// protectedRoutes 除登录与健康检查外所有 API 路由，未登录必须 401。
var protectedRoutes = [][2]string{
	{"GET", "/api/v1/system/status"}, {"GET", "/api/v1/system/mode"}, {"GET", "/api/v1/auth/me"}, {"POST", "/api/v1/auth/logout"},
	{"GET", "/api/v1/devices"}, {"GET", "/api/v1/devices/1/volumes"}, {"POST", "/api/v1/devices/rescan"},
	{"GET", "/api/v1/events"}, {"GET", "/api/v1/events/stream"},
	{"POST", "/api/v1/backups/plan"}, {"GET", "/api/v1/backups/plan"}, {"POST", "/api/v1/backups/start"},
	{"POST", "/api/v1/backups/pause"}, {"POST", "/api/v1/backups/resume"}, {"GET", "/api/v1/backups/history"}, {"POST", "/api/v1/backups/retry-failed"},
	{"GET", "/api/v1/tasks"}, {"GET", "/api/v1/tasks/1"}, {"POST", "/api/v1/tasks/1/cancel"}, {"POST", "/api/v1/tasks/1/retry"},
	{"GET", "/api/v1/config"}, {"PATCH", "/api/v1/config"}, {"POST", "/api/v1/storage/test"},
	{"POST", "/api/v1/storage/mount"}, {"POST", "/api/v1/storage/remount"}, {"POST", "/api/v1/storage/unmount"}, {"POST", "/api/v1/notifications/test"},
	{"GET", "/api/v1/test/faults"}, {"POST", "/api/v1/test/faults"}, {"DELETE", "/api/v1/test/faults/x"}, {"POST", "/api/v1/test/faults/reset"},
	{"GET", "/api/v1/test/sim/devices"}, {"POST", "/api/v1/test/sim/devices/sim-cam-1/detach"},
	{"GET", "/api/v1/unknown"},
}

func TestAuthRequiredEverywhere(t *testing.T) {
	a := newApp(t, writeTestConfig(t, true))
	c := &client{t: t, h: a.Handler()}

	for _, r := range protectedRoutes {
		if code, out, _ := c.do(r[0], r[1], ""); code != 401 || out["error"].(map[string]any)["code"] != "UNAUTHENTICATED" {
			t.Fatalf("%s %s without session: %d %v", r[0], r[1], code, out)
		}
	}
	if code, _, _ := c.do("GET", "/api/v1/system/health", ""); code != 200 {
		t.Fatalf("health must be public: %d", code)
	}
	if code, _, _ := c.do("GET", "/", ""); code != 200 {
		t.Fatalf("static must be public: %d", code)
	}
	c.cookie = "forged"
	if code, _, resp := c.do("GET", "/api/v1/devices", ""); code != 401 || len(resp.Cookies()) == 0 || resp.Cookies()[0].MaxAge != -1 {
		t.Fatalf("forged cookie must be rejected and cleared: %d", code)
	}
	c.cookie = ""

	if code, out := c.login("admin", "nope"); code != 401 || out["error"].(map[string]any)["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("bad login: %d %v", code, out)
	}
	if code, _ := c.login("root", "password123"); code != 401 {
		t.Fatalf("wrong user: %d", code)
	}
	code, out := c.login("admin", "password123")
	if code != 200 || c.cookie == "" || c.csrf == "" || out["data"].(map[string]any)["username"] != "admin" {
		t.Fatalf("login: %d %v", code, out)
	}
	code, out, _ = c.do("GET", "/api/v1/auth/me", "")
	if code != 200 || out["data"].(map[string]any)["csrf_token"] != c.csrf || out["data"].(map[string]any)["auth_enabled"] != true {
		t.Fatalf("me: %d %v", code, out)
	}
	for _, r := range protectedRoutes {
		if r[1] == "/api/v1/events/stream" || r[1] == "/api/v1/auth/logout" {
			continue
		}
		code, out, _ := c.do(r[0], r[1], "")
		if code == 401 {
			t.Fatalf("%s %s with session: %d %v", r[0], r[1], code, out)
		}
		if r[0] == "GET" && code == 403 {
			t.Fatalf("GET must not need csrf: %s %v", r[1], out)
		}
	}
	// 写操作必须带 CSRF
	csrf := c.csrf
	c.csrf = ""
	if code, out, _ := c.do("POST", "/api/v1/backups/plan", ""); code != 403 || out["error"].(map[string]any)["code"] != "CSRF_INVALID" {
		t.Fatalf("POST without csrf: %d %v", code, out)
	}
	c.csrf = "wrong"
	if code, _, _ := c.do("POST", "/api/v1/backups/plan", ""); code != 403 {
		t.Fatalf("POST wrong csrf: %d", code)
	}
	c.csrf = csrf
	if code, out, _ := c.do("POST", "/api/v1/backups/plan", ""); code != 200 {
		t.Fatalf("POST with csrf: %d %v", code, out)
	}
	code, out, _ = c.do("GET", "/api/v1/system/status", "")
	if code != 200 || out["data"].(map[string]any)["auth"].(map[string]any)["active_sessions"] != float64(1) {
		t.Fatalf("status auth: %d %v", code, out)
	}

	// 注销后一切回到 401
	if code, _, _ := c.do("POST", "/api/v1/auth/logout", ""); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/devices", ""); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
	c.cookie, c.csrf = "", ""

	// 登录限速：同一 IP 每分钟 5 次（上面已消耗 3 次）
	c.login("admin", "x")
	c.login("admin", "x")
	if code, out := c.login("admin", "password123"); code != 429 || out["error"].(map[string]any)["code"] != "RATE_LIMITED" {
		t.Fatalf("rate limit: %d %v", code, out)
	}
	if got := a.auth.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions after logout = %d", got)
	}
}

func TestAuthDisabledPassthrough(t *testing.T) {
	a := newApp(t, writeTestConfig(t, false))
	c := &client{t: t, h: a.Handler()}
	code, out, _ := c.do("GET", "/api/v1/auth/me", "")
	if code != 200 || out["data"].(map[string]any)["auth_enabled"] != false || out["data"].(map[string]any)["authenticated"] != true {
		t.Fatalf("me without auth: %d %v", code, out)
	}
	if code, _, _ := c.do("POST", "/api/v1/backups/plan", ""); code != 200 {
		t.Fatalf("writes need no csrf when auth disabled: %d", code)
	}
	if code, out, _ := c.do("POST", "/api/v1/auth/login", `{"username":"a","password":"b"}`); code != 200 || out["data"].(map[string]any)["auth_enabled"] != false {
		t.Fatalf("login when disabled: %d %v", code, out)
	}
}

func TestConfigAndStorageEndpoints(t *testing.T) {
	cfg := writeTestConfig(t, true)
	a := newApp(t, cfg)
	c := &client{t: t, h: a.Handler()}
	if code, _ := c.login("admin", "password123"); code != 200 {
		t.Fatal("login")
	}

	code, out, _ := c.do("GET", "/api/v1/config", "")
	data := out["data"].(map[string]any)
	if code != 200 || data["path"] != cfg.Path {
		t.Fatalf("config: %d %v", code, out)
	}
	srv := data["config"].(map[string]any)["server"].(map[string]any)["auth"].(map[string]any)
	if srv["password_hash"] != "***" || srv["username"] != "admin" {
		t.Fatalf("password hash must be redacted: %v", srv)
	}
	if p := data["patchable"].([]any); len(p) == 0 || p[0].(map[string]any)["key"] != "device.auto_backup" {
		t.Fatalf("patchable: %v", p)
	}

	for _, bad := range []string{`{"server.auth.password_hash":"x"}`, `{"backup.verify_mode":"md5"}`, `{}`, `not json`, `{"backup.retry_count":-1}`} {
		if code, out, _ := c.do("PATCH", "/api/v1/config", bad); code != 400 {
			t.Fatalf("bad patch %s: %d %v", bad, code, out)
		}
	}
	code, out, _ = c.do("PATCH", "/api/v1/config", `{"backup.verify_mode":"sha256","device.auto_backup":true,"backup.on_conflict":"skip","backup.retry_count":9,"backup.reconcile_interval_sec":7,"logging.level":"debug"}`)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, out)
	}
	got := out["data"].(map[string]any)["config"].(map[string]any)
	if got["backup"].(map[string]any)["verify_mode"] != "sha256" || got["device"].(map[string]any)["auto_backup"] != true {
		t.Fatalf("patched config: %v", got)
	}
	// 运行时生效
	if vm, oc := a.service.Policy(); string(vm) != "sha256" || oc != "skip" {
		t.Fatalf("service policy: %s %s", vm, oc)
	}
	if !a.autoBackup.Load() {
		t.Fatal("auto backup flag")
	}
	if o := a.sched.Options(); o.MaxRetries != 9 || o.ReconcileInterval != 7*time.Second {
		t.Fatalf("scheduler options: %+v", o)
	}
	if a.logLevel.Level() != slog.LevelDebug {
		t.Fatalf("log level: %v", a.logLevel.Level())
	}
	// 写回文件且保留注释
	raw, _ := os.ReadFile(cfg.Path)
	s := string(raw)
	for _, want := range []string{"# 测试配置", "# 认证开关", "# basic | sha256", "verify_mode: sha256", "auto_backup: true", "retry_count: 9", "level: debug"} {
		if !strings.Contains(s, want) {
			t.Fatalf("file missing %q:\n%s", want, s)
		}
	}
	reloaded, err := config.Load(cfg.Path)
	if err != nil || reloaded.Backup.VerifyMode != "sha256" || !reloaded.Server.Auth.Enabled {
		t.Fatalf("reload: %v %+v", err, reloaded)
	}
	if err := reloaded.Validate(faultinject.Enabled); err != nil {
		t.Fatal(err)
	}
	// config.changed 事件
	code, out, _ = c.do("GET", "/api/v1/events?limit=5", "")
	if code != 200 || out["data"].([]any)[0].(map[string]any)["type"] != "config.changed" {
		t.Fatalf("events: %d %v", code, out)
	}

	code, out, _ = c.do("POST", "/api/v1/storage/test", "")
	st := out["data"].(map[string]any)
	if code != 200 || st["ok"] != true || st["kind"] != "local" || st["write_allowed"] != true || st["root_exists"] != false {
		t.Fatalf("storage test: %d %v", code, out)
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(cfg.Path), "nas")); len(entries) != 0 {
		t.Fatalf("storage test must not write: %v", entries)
	}

	code, out, _ = c.do("GET", "/api/v1/devices/0506:8002:SIM0001/volumes", "")
	if code != 200 || len(out["data"].([]any)) != 1 {
		t.Fatalf("volumes by key: %d %v", code, out)
	}
	if code, _, _ := c.do("GET", "/api/v1/devices/1/volumes", ""); code != 200 {
		t.Fatalf("volumes by id: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/devices/999/volumes", ""); code != 404 {
		t.Fatalf("unknown device: %d", code)
	}
}

func TestEventsStream(t *testing.T) {
	a := newApp(t, writeTestConfig(t, true))
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	// 未登录 401
	resp, err := http.Get(srv.URL + "/api/v1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("stream without session: %d", resp.StatusCode)
	}
	s, err := a.auth.Login(context.Background(), "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/events/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: s.ID})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(resp.Body)
	readEvent := func() (string, map[string]any) {
		t.Helper()
		var name string
		var data map[string]any
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
					t.Fatal(err)
				}
			case line == "" && name != "":
				return name, data
			}
		}
	}
	if name, data := readEvent(); name != "hello" || data["mode"] != "simulation" {
		t.Fatalf("first event: %s %v", name, data)
	}
	tid := int64(42)
	a.bus.Publish(context.Background(), events.Event{Type: "test.ping", Message: "hi", Data: map[string]any{"n": 1}})
	a.bus.PublishTransient(events.Event{Type: "backup.progress", TaskID: &tid, Data: map[string]any{"percent": 50}})
	if name, data := readEvent(); name != "test.ping" || data["message"] != "hi" || data["id"] == float64(0) {
		t.Fatalf("published event: %s %v", name, data)
	}
	if name, data := readEvent(); name != "backup.progress" || data["task_id"] != float64(42) {
		t.Fatalf("progress event: %s %v", name, data)
	}
}
