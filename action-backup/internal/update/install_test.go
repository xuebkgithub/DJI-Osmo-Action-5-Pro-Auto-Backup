package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/events"
)

// testRelease 一套签好名的发布物：安装包、SHA256SUMS、SHA256SUMS.sig。
type testRelease struct {
	pub     ed25519.PublicKey
	version string
	pkgName string
	pkg     []byte
	sums    []byte
	sig     []byte
}

// buildPackage 生成与 scripts/package.sh 同结构的 tar.gz。extra 可追加条目（测试恶意包）。
func buildPackage(t *testing.T, top string, files map[string]string, extra func(tw *tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		mode := int64(0o644)
		if name == "action-backup" || name == "install.sh" {
			mode = 0o755
		}
		_ = tw.WriteHeader(&tar.Header{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	if extra != nil {
		extra(tw)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newTestRelease(t *testing.T, version string) (*testRelease, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &testRelease{pub: pub, version: version, pkgName: AssetName(version, "linux", "arm64")}
	r.pkg = buildPackage(t, strings.TrimSuffix(r.pkgName, ".tar.gz"),
		map[string]string{"action-backup": "NEW-BINARY-" + version, "install.sh": "#!/bin/bash\n", "action-backup.service": "[Unit]\n"}, nil)
	r.resign(priv)
	return r, priv
}

// resign 按当前 pkg 重新生成 SHA256SUMS 与签名。
func (r *testRelease) resign(priv ed25519.PrivateKey) {
	sum := sha256.Sum256(r.pkg)
	other := sha256.Sum256([]byte("other"))
	r.sums = []byte(fmt.Sprintf("%s  %s\n%s  %s\n", hex.EncodeToString(other[:]), AssetName(r.version, "darwin", "arm64"), hex.EncodeToString(sum[:]), r.pkgName))
	r.sig = ed25519.Sign(priv, r.sums)
}

func TestSignHelpers(t *testing.T) {
	if _, err := ReleasePublicKey(); err != nil {
		t.Fatalf("embedded release public key must parse: %v", err)
	}
	r, _ := newTestRelease(t, "0.9.0")
	if err := VerifySums(r.pub, r.sums, r.sig); err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), r.sums...)
	tampered[0] ^= 1
	if err := VerifySums(r.pub, tampered, r.sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered sums: %v", err)
	}
	if err := VerifySums(r.pub, r.sums, r.sig[:10]); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("short sig: %v", err)
	}
	embedded, _ := ReleasePublicKey()
	if err := VerifySums(embedded, r.sums, r.sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("signature from another key must fail: %v", err)
	}
	name, sum, ver, err := PlatformPackage(r.sums, "linux", "arm64")
	if err != nil || name != r.pkgName || ver != "0.9.0" || len(sum) != 64 {
		t.Fatalf("PlatformPackage: %q %q %q %v", name, sum, ver, err)
	}
	if _, _, _, err := PlatformPackage(r.sums, "linux", "riscv64"); !errors.Is(err, ErrSumNotFound) {
		t.Fatalf("missing platform: %v", err)
	}
	dup := append(append([]byte(nil), r.sums...), []byte(strings.Repeat("a", 64)+"  action-backup-9.9.9-linux-arm64.tar.gz\n")...)
	if _, _, _, err := PlatformPackage(dup, "linux", "arm64"); err == nil {
		t.Fatal("two packages for one platform must be rejected")
	}
	evil := []byte(strings.Repeat("a", 64) + "  ../action-backup-9.9.9-linux-arm64.tar.gz\n" + strings.Repeat("b", 64) + "  action-backup-notaversion-linux-arm64.tar.gz\n")
	if _, _, _, err := PlatformPackage(evil, "linux", "arm64"); err == nil {
		t.Fatal("path-like or non-semver names must be ignored")
	}
	if v, fi, err := ParseVersionOutput("action-backup 0.9.0 (commit abc, built 2026-11-19T00:00:00Z, faultinject=false)\n"); err != nil || v != "0.9.0" || fi {
		t.Fatalf("ParseVersionOutput: %q %v %v", v, fi, err)
	}
	if _, fi, _ := ParseVersionOutput("action-backup v0.9.0-dirty (commit x, built y, faultinject=true)"); !fi {
		t.Fatal("faultinject flag not parsed")
	}
	if _, _, err := ParseVersionOutput("something else"); err == nil {
		t.Fatal("garbage must fail")
	}
}

// releaseServer 假 GitHub：Releases 列表 + 资产下载（支持 Range）。
type releaseServer struct {
	*httptest.Server
	mu       sync.Mutex
	rel      *testRelease
	unsigned bool
	// cutAfter > 0 时安装包的第一次响应只发这么多字节就断开，用来测续传。
	cutAfter  int
	pkgHits   atomic.Int32
	rangeSeen atomic.Value // string
}

func newReleaseServer(t *testing.T, rel *testRelease) *releaseServer {
	t.Helper()
	s := &releaseServer{rel: rel}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rel, unsigned, cut := s.rel, s.unsigned, s.cutAfter
		s.mu.Unlock()
		switch r.URL.Path {
		case "/repos/owner/repo/releases":
			assets := []map[string]any{{"name": rel.pkgName, "size": len(rel.pkg), "browser_download_url": s.URL + "/dl/" + rel.pkgName}}
			if !unsigned {
				assets = append(assets,
					map[string]any{"name": SumsName, "size": len(rel.sums), "browser_download_url": s.URL + "/dl/" + SumsName},
					map[string]any{"name": SigName, "size": len(rel.sig), "browser_download_url": s.URL + "/dl/" + SigName})
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v" + rel.version, "html_url": s.URL + "/r", "assets": assets}})
		case "/dl/" + SumsName:
			_, _ = w.Write(rel.sums)
		case "/dl/" + SigName:
			_, _ = w.Write(rel.sig)
		case "/dl/" + rel.pkgName:
			n := s.pkgHits.Add(1)
			if rg := r.Header.Get("Range"); rg != "" {
				s.rangeSeen.Store(rg)
				var from int
				if _, err := fmt.Sscanf(rg, "bytes=%d-", &from); err != nil || from > len(rel.pkg) {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(rel.pkg)-1, len(rel.pkg)))
				w.Header().Set("Content-Length", fmt.Sprint(len(rel.pkg)-from))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(rel.pkg[from:])
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(rel.pkg)))
			if cut > 0 && n == 1 {
				_, _ = w.Write(rel.pkg[:cut])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				panic(http.ErrAbortHandler) // 中途断开连接
			}
			_, _ = w.Write(rel.pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newInstallChecker(t *testing.T, srv *releaseServer, current string, bus *events.Broadcaster, mod func(*InstallOptions)) (*Checker, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "update")
	enabled := false
	inst := InstallOptions{Supported: true, Dir: dir, PublicKey: srv.rel.pub, Attempts: 4, Backoff: time.Millisecond, IdlePoll: 5 * time.Millisecond,
		ResultPath: filepath.Join(t.TempDir(), "update-result.json")}
	if mod != nil {
		mod(&inst)
	}
	c := New(config.UpdateConfig{CheckEnabled: &enabled, CheckIntervalHours: 12, Channel: config.UpdateChannelStable, APIBaseURL: srv.URL}, bus,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{Current: current, OS: "linux", Arch: "arm64", Repository: "owner/repo", Install: inst})
	return c, dir
}

func waitStage(t *testing.T, c *Checker, want ...string) InstallStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := c.Status().Install
		for _, w := range want {
			if st.Stage == w {
				return st
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage = %q (error %q), want one of %v", st.Stage, st.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestApplyDownloadsVerifiesAndRequests(t *testing.T) {
	rel, _ := newTestRelease(t, "0.9.0")
	srv := newReleaseServer(t, rel)
	srv.cutAfter = len(rel.pkg) / 2
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(64)
	defer cancel()
	var busy atomic.Bool
	busy.Store(true)
	c, dir := newInstallChecker(t, srv, "0.8.0", bus, func(o *InstallOptions) { o.Busy = busy.Load })

	if _, err := c.Apply(); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("apply before check: %v", err)
	}
	st, err := c.Check(context.Background())
	if err != nil || !st.UpdateAvailable || !st.Latest.Signed || !st.InstallSupported || st.Install.Stage != StageIdle {
		t.Fatalf("check: %+v err %v", st, err)
	}
	st, err = c.Apply()
	if err != nil || st.Install.Stage != StageDownloading || st.Install.Version != "0.9.0" {
		t.Fatalf("apply: %+v err %v", st.Install, err)
	}
	if _, err := c.Apply(); !errors.Is(err, ErrInstallInProgress) {
		t.Fatalf("second apply: %v", err)
	}
	// 备份在跑：包就绪后停在 waiting_idle，不写请求标记
	inst := waitStage(t, c, StageWaitingIdle)
	if inst.DownloadedBytes != int64(len(rel.pkg)) || inst.TotalBytes != int64(len(rel.pkg)) {
		t.Fatalf("progress: %+v", inst)
	}
	if _, err := os.Stat(filepath.Join(dir, RequestName)); err == nil {
		t.Fatal("request marker must not exist while busy")
	}
	if rg, _ := srv.rangeSeen.Load().(string); rg != fmt.Sprintf("bytes=%d-", srv.cutAfter) || srv.pkgHits.Load() != 2 {
		t.Fatalf("resume: range %q hits %d", rg, srv.pkgHits.Load())
	}
	busy.Store(false)
	inst = waitStage(t, c, StageRequested)
	if inst.RequestedAt == "" || inst.Error != "" {
		t.Fatalf("requested: %+v", inst)
	}
	for _, f := range []string{RequestName, SumsName, SigName, rel.pkgName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, rel.pkgName)); !bytes.Equal(got, rel.pkg) {
		t.Fatal("downloaded package differs")
	}
	if _, err := os.Stat(filepath.Join(dir, rel.pkgName+".part")); err == nil {
		t.Fatal(".part must be renamed away")
	}
	var types []string
	for len(ch) > 0 {
		types = append(types, (<-ch).Type)
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "update.available") || !strings.Contains(joined, "update.progress") || !strings.HasSuffix(joined, "update.requested") {
		t.Fatalf("events: %s", joined)
	}
	// 重启后的新进程：看到遗留的请求标记，阶段恢复为 requested
	c2, _ := newInstallChecker(t, srv, "0.8.0", nil, func(o *InstallOptions) { o.Dir = dir })
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go c2.Run(ctx)
	waitStage(t, c2, StageRequested)
}

func TestApplyRejections(t *testing.T) {
	rel, priv := newTestRelease(t, "0.9.0")
	srv := newReleaseServer(t, rel)

	// 不支持安装的进程
	c, _ := newInstallChecker(t, srv, "0.8.0", nil, func(o *InstallOptions) { o.Supported = false })
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Apply(); !errors.Is(err, ErrInstallUnsupported) || st.InstallSupported {
		t.Fatalf("unsupported: %v", err)
	}
	// 已是最新 / 开发构建
	for _, cur := range []string{"0.9.0", "v0.8.0-dirty"} {
		c, _ = newInstallChecker(t, srv, cur, nil, nil)
		if _, err := c.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Apply(); !errors.Is(err, ErrNoUpdate) {
			t.Fatalf("current %s: %v", cur, err)
		}
	}
	// 没签名的发布
	srv.mu.Lock()
	srv.unsigned = true
	srv.mu.Unlock()
	c, _ = newInstallChecker(t, srv, "0.8.0", nil, nil)
	if st, err := c.Check(context.Background()); err != nil || st.Latest.Signed {
		t.Fatalf("unsigned check: %+v %v", st.Latest, err)
	}
	if _, err := c.Apply(); !errors.Is(err, ErrNotInstallable) {
		t.Fatalf("unsigned: %v", err)
	}
	srv.mu.Lock()
	srv.unsigned = false
	srv.mu.Unlock()

	// 签名来自别的密钥：下载大文件之前就失败
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(64)
	defer cancel()
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	before := srv.pkgHits.Load()
	c, dir := newInstallChecker(t, srv, "0.8.0", bus, func(o *InstallOptions) { o.PublicKey = otherPub })
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(); err != nil {
		t.Fatal(err)
	}
	inst := waitStage(t, c, StageFailed)
	if !strings.Contains(inst.Error, "signature verification failed") || srv.pkgHits.Load() != before {
		t.Fatalf("bad signature: %+v, package hits %d → %d", inst, before, srv.pkgHits.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, RequestName)); err == nil {
		t.Fatal("request marker must not exist after failure")
	}
	var failed *events.Event
	for len(ch) > 0 {
		if e := <-ch; e.Type == "update.failed" {
			failed = &e
		}
	}
	if failed == nil || failed.Level != "error" || failed.Data["step"] != "verify" {
		t.Fatalf("update.failed event: %+v", failed)
	}
	// 失败后可以再试
	if _, err := c.Apply(); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	waitStage(t, c, StageFailed)

	// 包内容与清单不符（清单签名是对的）
	bad := *rel
	bad.pkg = append([]byte(nil), rel.pkg...)
	bad.pkg[len(bad.pkg)-1] ^= 0xff
	srv.mu.Lock()
	srv.rel = &bad
	srv.mu.Unlock()
	_ = priv
	c, dir = newInstallChecker(t, srv, "0.8.0", nil, nil)
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(); err != nil {
		t.Fatal(err)
	}
	inst = waitStage(t, c, StageFailed)
	if !strings.Contains(inst.Error, "sha256 mismatch") {
		t.Fatalf("corrupt package: %+v", inst)
	}
	if _, err := os.Stat(filepath.Join(dir, rel.pkgName)); err == nil {
		t.Fatal("corrupt package must be deleted")
	}
}

func TestReportResultOnce(t *testing.T) {
	rel, _ := newTestRelease(t, "0.9.0")
	srv := newReleaseServer(t, rel)
	bus := events.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, cancel := bus.Subscribe(16)
	defer cancel()
	var resultPath, dir string
	mk := func() *Checker {
		c, d := newInstallChecker(t, srv, "0.9.0", bus, func(o *InstallOptions) {
			if resultPath != "" {
				o.ResultPath, o.Dir = resultPath, dir
			} else {
				resultPath = o.ResultPath
			}
		})
		if dir == "" {
			dir = d
		}
		return c
	}
	run := func(c *Checker) {
		ctx, stop := context.WithCancel(context.Background())
		c.startInstall(ctx)
		stop()
	}
	c := mk()
	run(c) // 没有结果文件
	if c.Status().Install.LastResult != nil || len(ch) != 0 {
		t.Fatal("no result expected")
	}
	write := func(r Result) {
		data, _ := json.Marshal(r)
		if err := os.WriteFile(resultPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(Result{OK: true, From: "0.8.0", To: "0.9.0", FinishedAt: "2026-11-20T01:00:00Z"})
	c = mk()
	run(c)
	if r := c.Status().Install.LastResult; r == nil || !r.OK || r.To != "0.9.0" {
		t.Fatalf("last result: %+v", r)
	}
	if e := <-ch; e.Type != "update.installed" || e.Data["to"] != "0.9.0" {
		t.Fatalf("event: %+v", e)
	}
	c = mk()
	run(c) // 同一结果第二次启动不再发事件，但状态里仍然有
	if len(ch) != 0 || c.Status().Install.LastResult == nil {
		t.Fatal("result must be reported once but stay visible")
	}
	write(Result{From: "0.9.0", To: "0.10.0", Step: "health", Error: "unit not active", RolledBack: true, FinishedAt: "2026-11-21T01:00:00Z"})
	c = mk()
	run(c)
	if e := <-ch; e.Type != "update.failed" || e.Level != "error" || e.Data["step"] != "health" || e.Data["rolled_back"] != true {
		t.Fatalf("failure event: %+v", e)
	}
}
