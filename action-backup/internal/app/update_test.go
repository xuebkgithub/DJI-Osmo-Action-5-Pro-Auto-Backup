package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/update"
	"action-backup/internal/version"
)

// TestUpdateEndpoints 用假的 GitHub API 走通「检查 → 状态 → 事件 → 在线改配置」。
func TestUpdateEndpoints(t *testing.T) {
	old := version.Version
	version.Version = "0.7.0"
	t.Cleanup(func() { version.Version = old })

	var fail atomic.Bool
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/"+update.Repository+"/releases" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
			return
		}
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[
		  {"tag_name":"v0.8.0","name":"action-backup 0.8.0","html_url":"https://example.invalid/r/v0.8.0","published_at":"2026-11-19T02:30:00Z","body":"### 变更\n- USB 协议版本显示",
		   "assets":[{"name":"action-backup-0.8.0-linux-arm64.tar.gz","size":4889552,"browser_download_url":"https://example.invalid/dl/a"},{"name":"SHA256SUMS","size":400,"browser_download_url":"https://example.invalid/dl/s"}]},
		  {"tag_name":"v0.9.0-rc.1","prerelease":true,"html_url":"https://example.invalid/r/v0.9.0-rc.1"},
		  {"tag_name":"v0.7.1"}
		]`))
	}))
	defer gh.Close()

	cfg := writeTestConfig(t, true)
	if err := config.SetValues(cfg.Path, map[string]any{"update.api_base_url": gh.URL}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(t, cfg)
	c := &client{t: t, h: a.Handler()}
	if code, _ := c.login("admin", "password123"); code != 200 {
		t.Fatal("login")
	}

	// 未检查过：只有当前版本，没有网络请求
	code, out, _ := c.do("GET", "/api/v1/system/update", "")
	st := out["data"].(map[string]any)
	if code != 200 || st["current_version"] != "0.7.0" || st["dev_build"] != false || st["update_available"] != false || st["latest"] != nil ||
		st["check_enabled"] != false || st["channel"] != "stable" || st["install_supported"] != false || st["repository"] != update.Repository {
		t.Fatalf("initial status: %d %v", code, out)
	}
	if hits.Load() != 0 {
		t.Fatal("GET status must not hit the network")
	}

	// 手动检查：定期检查关着也能查
	code, out, _ = c.do("POST", "/api/v1/system/update/check", "")
	st = out["data"].(map[string]any)
	if code != 200 || st["update_available"] != true || st["checked_at"] == nil {
		t.Fatalf("check: %d %v", code, out)
	}
	latest := st["latest"].(map[string]any)
	if latest["version"] != "0.8.0" || latest["tag"] != "v0.8.0" || latest["prerelease"] != false || !strings.Contains(latest["notes"].(string), "USB") {
		t.Fatalf("latest: %v", latest)
	}
	// 安装包按运行平台匹配：测试机不是 linux-arm64 时没有对应资产
	if asset, ok := latest["asset"].(map[string]any); ok && asset["name"] != "action-backup-0.8.0-"+st["platform"].(string)+".tar.gz" {
		t.Fatalf("asset: %v", asset)
	}
	code, out, _ = c.do("GET", "/api/v1/events?limit=5", "")
	ev := out["data"].([]any)[0].(map[string]any)
	if code != 200 || ev["type"] != "update.available" || ev["data"].(map[string]any)["version"] != "0.8.0" {
		t.Fatalf("event: %d %v", code, ev)
	}

	// 在线切换通道：立即作用于检查器并写回文件
	code, out, _ = c.do("PATCH", "/api/v1/config", `{"update.channel":"prerelease","update.check_interval_hours":24}`)
	if code != 200 || out["data"].(map[string]any)["config"].(map[string]any)["update"].(map[string]any)["channel"] != "prerelease" {
		t.Fatalf("patch: %d %v", code, out)
	}
	if st := a.Updater().Status(); st.Channel != "prerelease" || st.CheckIntervalHours != 24 || st.Latest != nil {
		t.Fatalf("checker not reconfigured: %+v", st)
	}
	raw, _ := os.ReadFile(cfg.Path)
	if s := string(raw); !strings.Contains(s, "channel: prerelease") || !strings.Contains(s, "check_interval_hours: 24") {
		t.Fatalf("config file not updated:\n%s", s)
	}
	code, out, _ = c.do("POST", "/api/v1/system/update/check", "")
	if latest := out["data"].(map[string]any)["latest"].(map[string]any); code != 200 || latest["version"] != "0.9.0-rc.1" || latest["prerelease"] != true {
		t.Fatalf("prerelease check: %d %v", code, out)
	}
	for _, bad := range []string{`{"update.api_base_url":"https://evil.example"}`, `{"update.channel":"nightly"}`} {
		if code, out, _ := c.do("PATCH", "/api/v1/config", bad); code != 400 {
			t.Fatalf("bad patch %s: %d %v", bad, code, out)
		}
	}

	// 检查失败：502，同时带回上一次成功的结果
	fail.Store(true)
	code, out, _ = c.do("POST", "/api/v1/system/update/check", "")
	e := out["error"].(map[string]any)
	st = out["data"].(map[string]any)
	if code != 502 || e["code"] != "UPDATE_CHECK_FAILED" || e["retryable"] != true || st["last_error"] == nil || st["latest"] == nil {
		t.Fatalf("failed check: %d %v", code, out)
	}
}

// TestUpdateDevBuildNeverPrompts 默认版本串（开发构建）下不提示更新。
func TestUpdateDevBuildNeverPrompts(t *testing.T) {
	a := newApp(t, writeTestConfig(t, false))
	if st := a.Updater().Status(); !st.DevBuild || st.UpdateAvailable || st.NextCheckAt != "" {
		t.Fatalf("dev build status: %+v", st)
	}
}

// TestUpdateApplyEndpoint 安装接口：默认（mac / 开发构建）不支持；注入安装参数后走通下载、验签、提交请求。
func TestUpdateApplyEndpoint(t *testing.T) {
	old := version.Version
	version.Version = "0.8.0"
	t.Cleanup(func() { version.Version = old })

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkgName := update.AssetName("0.9.0", runtime.GOOS, runtime.GOARCH)
	pkg := []byte("not really a tarball; the unprivileged side only checks the hash")
	sum := sha256.Sum256(pkg)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + pkgName + "\n")
	sig := ed25519.Sign(priv, sums)
	var signed atomic.Bool
	signed.Store(true)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + update.Repository + "/releases":
			assets := `{"name":"` + pkgName + `","size":` + strconv.Itoa(len(pkg)) + `,"browser_download_url":"` + gh.URL + `/dl/pkg"}`
			if signed.Load() {
				assets += `,{"name":"SHA256SUMS","browser_download_url":"` + gh.URL + `/dl/sums"},{"name":"SHA256SUMS.sig","browser_download_url":"` + gh.URL + `/dl/sig"}`
			}
			_, _ = w.Write([]byte(`[{"tag_name":"v0.9.0","html_url":"https://example.invalid/r","assets":[` + assets + `]}]`))
		case "/dl/pkg":
			_, _ = w.Write(pkg)
		case "/dl/sums":
			_, _ = w.Write(sums)
		case "/dl/sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	mk := func(opts ...Option) (*App, *client) {
		cfg := writeTestConfig(t, true)
		if err := config.SetValues(cfg.Path, map[string]any{"update.api_base_url": gh.URL}); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(cfg.Path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
		if err != nil {
			t.Fatal(err)
		}
		c := &client{t: t, h: a.Handler()}
		if code, _ := c.login("admin", "password123"); code != 200 {
			t.Fatal("login")
		}
		return a, c
	}

	// 默认装配：测试机不是「Linux 正式构建 + 更新定时任务」，接口明确拒绝
	_, c := mk()
	if code, out, _ := c.do("POST", "/api/v1/system/update/check", ""); code != 200 || out["data"].(map[string]any)["install_supported"] != false {
		t.Fatalf("check: %d %v", code, out)
	}
	if code, out, _ := c.do("POST", "/api/v1/system/update/apply", ""); code != 501 || out["error"].(map[string]any)["code"] != "INSTALL_NOT_SUPPORTED" {
		t.Fatalf("apply on unsupported platform: %d %v", code, out)
	}

	dir := filepath.Join(t.TempDir(), "update")
	a, c := mk(WithUpdateInstall(update.InstallOptions{Supported: true, Dir: dir, PublicKey: pub, Backoff: time.Millisecond}))
	// 还没检查过
	if code, out, _ := c.do("POST", "/api/v1/system/update/apply", ""); code != 409 || out["error"].(map[string]any)["code"] != "NO_UPDATE" {
		t.Fatalf("apply before check: %d %v", code, out)
	}
	// 没签名的发布不能自动安装
	signed.Store(false)
	if code, _, _ := c.do("POST", "/api/v1/system/update/check", ""); code != 200 {
		t.Fatal("check")
	}
	if code, out, _ := c.do("POST", "/api/v1/system/update/apply", ""); code != 409 || out["error"].(map[string]any)["code"] != "NOT_INSTALLABLE" {
		t.Fatalf("apply unsigned: %d %v", code, out)
	}
	signed.Store(true)
	code, out, _ := c.do("POST", "/api/v1/system/update/check", "")
	st := out["data"].(map[string]any)
	if code != 200 || st["install_supported"] != true || st["latest"].(map[string]any)["signed"] != true || st["install"].(map[string]any)["stage"] != "idle" {
		t.Fatalf("check: %d %v", code, out)
	}
	code, out, _ = c.do("POST", "/api/v1/system/update/apply", "")
	if code != 202 || out["data"].(map[string]any)["install"].(map[string]any)["version"] != "0.9.0" {
		t.Fatalf("apply: %d %v", code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.Updater().Status().Install.Stage != update.StageRequested {
		if st := a.Updater().Status().Install; st.Stage == update.StageFailed || time.Now().After(deadline) {
			t.Fatalf("install did not reach requested: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, out, _ := c.do("POST", "/api/v1/system/update/apply", ""); code != 409 || out["error"].(map[string]any)["code"] != "INSTALL_IN_PROGRESS" {
		t.Fatalf("apply twice: %d %v", code, out)
	}
	for _, f := range []string{update.RequestName, update.SumsName, update.SigName, pkgName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	code, out, _ = c.do("GET", "/api/v1/events?limit=3", "")
	if ev := out["data"].([]any)[0].(map[string]any); code != 200 || ev["type"] != "update.requested" || ev["data"].(map[string]any)["to"] != "0.9.0" {
		t.Fatalf("event: %d %v", code, ev)
	}
}
