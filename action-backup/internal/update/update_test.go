package update

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/events"
)

func TestIsDevBuild(t *testing.T) {
	for v, want := range map[string]bool{
		"0.8.0":                   false,
		"v0.8.0":                  false,
		"0.9.0-rc.1":              false,
		"v1.0.0-beta":             false,
		"0.1.0-dev":               true,
		"v0.8.0-dirty":            true,
		"v0.8.0-3-g76121de":       true,
		"v0.8.0-3-g76121de-dirty": true,
		"0.9.0-rc.1-2-gabcdef0":   true,
		"76121de":                 true,
		"":                        true,
		"unknown":                 true,
	} {
		if got := IsDevBuild(v); got != want {
			t.Errorf("IsDevBuild(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cand, cur string
		want      bool
	}{
		{"0.9.0", "0.8.0", true},
		{"v0.9.0", "0.8.0", true},
		{"0.8.0", "0.8.0", false},
		{"0.7.1", "0.8.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.9.0", "0.9.0-rc.1", true},
		{"0.9.0-rc.1", "0.9.0", false},
		{"0.9.0-rc.10", "0.9.0-rc.2", true},
		{"1.0.0", "0.99.99", true},
		{"garbage", "0.8.0", false},
		{"0.9.0", "garbage", false},
	} {
		if got := newer(c.cand, c.cur); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	s := strings.Repeat("更", 10) // 每个字 3 字节
	got, cut := truncate(s, 10)
	if !cut || got != "更更更" {
		t.Fatalf("truncate = %q, %v", got, cut)
	}
	if got, cut := truncate("abc", 10); cut || got != "abc" {
		t.Fatalf("no truncation expected: %q %v", got, cut)
	}
}

type fakeRelease struct {
	Tag        string
	Draft, Pre bool
	Assets     []string
}

// fakeGitHub 起一个只认 Releases 列表的假 API；handler 为空时按 releases 输出。
func fakeGitHub(t *testing.T, releases *[]fakeRelease, hits *atomic.Int32, override http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if override != nil {
			override(w, r)
			return
		}
		if r.URL.Path != "/repos/owner/repo/releases" || r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
			http.Error(w, "bad request: "+r.URL.String(), http.StatusBadRequest)
			return
		}
		var out []map[string]any
		for _, fr := range *releases {
			assets := []map[string]any{}
			for _, a := range fr.Assets {
				assets = append(assets, map[string]any{"name": a, "size": 4096, "browser_download_url": "https://example.invalid/dl/" + a})
			}
			out = append(out, map[string]any{
				"tag_name": fr.Tag, "name": "action-backup " + fr.Tag, "draft": fr.Draft, "prerelease": fr.Pre,
				"published_at": "2026-11-18T02:00:00Z", "html_url": "https://example.invalid/releases/" + fr.Tag,
				"body": "### 变更\n- " + fr.Tag, "assets": assets,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newChecker(t *testing.T, base, current, channel string, bus *events.Broadcaster) *Checker {
	t.Helper()
	enabled := true
	cfg := config.UpdateConfig{CheckEnabled: &enabled, CheckIntervalHours: 12, Channel: channel, APIBaseURL: base}
	return New(cfg, bus, slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{Current: current, OS: "linux", Arch: "arm64", Repository: "owner/repo", StartDelay: 10 * time.Millisecond})
}

func TestCheckPicksHighestStable(t *testing.T) {
	rels := []fakeRelease{
		{Tag: "v0.8.0", Assets: []string{"action-backup-0.8.0-linux-arm64.tar.gz", "SHA256SUMS"}},
		{Tag: "v0.10.0", Assets: []string{"action-backup-0.10.0-linux-amd64.tar.gz", "action-backup-0.10.0-linux-arm64.tar.gz", "SHA256SUMS"}},
		{Tag: "v0.9.0", Assets: []string{"action-backup-0.9.0-linux-arm64.tar.gz"}},
		{Tag: "v0.11.0-rc.1", Pre: true},
		{Tag: "v0.12.0-beta"}, // 版本号带预发布段但没标 prerelease，stable 通道同样跳过
		{Tag: "v9.9.9", Draft: true},
		{Tag: "nightly"},
	}
	srv := fakeGitHub(t, &rels, nil, nil)
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(8)
	defer cancel()

	c := newChecker(t, srv.URL, "0.8.0", config.UpdateChannelStable, bus)
	if st := c.Status(); st.Latest != nil || st.UpdateAvailable || st.CheckedAt != "" || st.DevBuild || st.InstallSupported {
		t.Fatalf("initial status: %+v", st)
	}
	st, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.UpdateAvailable || st.Latest == nil || st.Latest.Version != "0.10.0" || st.Latest.Tag != "v0.10.0" || st.Latest.Prerelease {
		t.Fatalf("status: %+v latest %+v", st, st.Latest)
	}
	if a := st.Latest.Asset; a == nil || a.Name != "action-backup-0.10.0-linux-arm64.tar.gz" || a.Size != 4096 {
		t.Fatalf("asset: %+v", st.Latest.Asset)
	}
	if st.CheckedAt == "" || st.LastError != "" || st.Platform != "linux-arm64" || st.CurrentVersion != "0.8.0" || !strings.Contains(st.Latest.Notes, "v0.10.0") {
		t.Fatalf("status fields: %+v", st)
	}
	select {
	case e := <-ch:
		if e.Type != "update.available" || e.Data["version"] != "0.10.0" || e.Data["current"] != "0.8.0" {
			t.Fatalf("event: %+v", e)
		}
	default:
		t.Fatal("expected update.available event")
	}
	// 同一版本再查一次不重复发事件
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected second event: %+v", e)
	default:
	}

	// 切到 prerelease 通道：旧结果作废，重新检查后拿到 rc / beta 中最高的
	enabled := true
	c.Configure(config.UpdateConfig{CheckEnabled: &enabled, CheckIntervalHours: 12, Channel: config.UpdateChannelPrerelease, APIBaseURL: srv.URL})
	if st := c.Status(); st.Latest != nil || st.Channel != config.UpdateChannelPrerelease {
		t.Fatalf("channel switch must drop stale result: %+v", st)
	}
	st, err = c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Latest.Version != "0.12.0-beta" || !st.Latest.Prerelease || st.Latest.Asset != nil || !st.UpdateAvailable {
		t.Fatalf("prerelease channel: %+v", st.Latest)
	}
}

func TestCheckUpToDateAndDevBuild(t *testing.T) {
	rels := []fakeRelease{{Tag: "v0.8.0"}, {Tag: "v0.7.1"}}
	srv := fakeGitHub(t, &rels, nil, nil)
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(8)
	defer cancel()

	st, err := newChecker(t, srv.URL, "v0.8.0", config.UpdateChannelStable, bus).Check(context.Background())
	if err != nil || st.UpdateAvailable || st.Latest.Version != "0.8.0" {
		t.Fatalf("up to date: %+v err %v", st, err)
	}
	// 开发构建：能查到最新版，但不判定有更新、不发事件
	st, err = newChecker(t, srv.URL, "v0.7.0-3-gabcdef0-dirty", config.UpdateChannelStable, bus).Check(context.Background())
	if err != nil || !st.DevBuild || st.UpdateAvailable || st.Latest == nil || st.NextCheckAt != "" {
		t.Fatalf("dev build: %+v err %v", st, err)
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected event: %+v", e)
	default:
	}
}

func TestCheckFailures(t *testing.T) {
	var mode atomic.Int32
	rels := []fakeRelease{{Tag: "v0.9.0"}}
	var srv *httptest.Server
	srv = fakeGitHub(t, &rels, nil, func(w http.ResponseWriter, _ *http.Request) {
		switch mode.Load() {
		case 0:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1795000000")
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		case 1:
			http.Error(w, "not found", http.StatusNotFound)
		case 2:
			_, _ = w.Write([]byte("<html>not json"))
		case 3:
			_, _ = w.Write([]byte(`[{"tag_name":"v9.0.0","draft":true}]`))
		default:
			_, _ = w.Write([]byte(`[{"tag_name":"v0.9.0","html_url":"` + srv.URL + `/r"}]`))
		}
	})
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(8)
	defer cancel()
	c := newChecker(t, srv.URL, "0.8.0", config.UpdateChannelStable, bus)

	for i, want := range []string{"rate limit exceeded, resets at", "HTTP 404", "decode releases", "no release found"} {
		mode.Store(int32(i))
		st, err := c.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("mode %d: err = %v, want %q", i, err, want)
		}
		if st.LastError == "" || st.LastErrorAt == "" || st.UpdateAvailable || st.Checking {
			t.Fatalf("mode %d: status %+v", i, st)
		}
	}
	// 连续失败只发一次 update.check_failed
	n := 0
	for {
		select {
		case e := <-ch:
			if e.Type != "update.check_failed" || e.Level != "warn" {
				t.Fatalf("event: %+v", e)
			}
			n++
			continue
		default:
		}
		break
	}
	if n != 1 {
		t.Fatalf("update.check_failed events = %d, want 1", n)
	}
	// 恢复后错误清空
	mode.Store(9)
	st, err := c.Check(context.Background())
	if err != nil || st.LastError != "" || !st.UpdateAvailable {
		t.Fatalf("recovered: %+v err %v", st, err)
	}
	// 再次失败：保留上一次成功的结果
	mode.Store(1)
	st, err = c.Check(context.Background())
	if err == nil || st.Latest == nil || !st.UpdateAvailable || st.LastError == "" {
		t.Fatalf("failure must keep last good result: %+v err %v", st, err)
	}
}

func TestRunPeriodic(t *testing.T) {
	var hits atomic.Int32
	rels := []fakeRelease{{Tag: "v0.9.0"}}
	srv := fakeGitHub(t, &rels, &hits, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 开发构建不做定期检查
	dev := newChecker(t, srv.URL, "0.1.0-dev", config.UpdateChannelStable, nil)
	go dev.Run(ctx)
	// 关闭定期检查的正式构建同样不查
	off := false
	disabled := newChecker(t, srv.URL, "0.8.0", config.UpdateChannelStable, nil)
	disabled.Configure(config.UpdateConfig{CheckEnabled: &off, CheckIntervalHours: 12, Channel: config.UpdateChannelStable, APIBaseURL: srv.URL})
	go disabled.Run(ctx)
	time.Sleep(150 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Fatalf("dev / disabled checker must not poll, got %d requests", n)
	}

	// 运行中打开开关：按启动延迟查一次，之后等满一个周期
	on := true
	disabled.Configure(config.UpdateConfig{CheckEnabled: &on, CheckIntervalHours: 12, Channel: config.UpdateChannelStable, APIBaseURL: srv.URL})
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("enabled checker requests = %d, want 1", n)
	}
	st := disabled.Status()
	if !st.UpdateAvailable || st.NextCheckAt == "" {
		t.Fatalf("status after periodic check: %+v", st)
	}
	next, err := time.Parse(time.RFC3339, st.NextCheckAt)
	if err != nil || time.Until(next) < 11*time.Hour {
		t.Fatalf("next check should be ~12h away: %s (%v)", st.NextCheckAt, err)
	}
}
