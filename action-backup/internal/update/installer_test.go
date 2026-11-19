package update

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
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
	"sync/atomic"
	"testing"
	"time"
)

// fakeOps 假的系统操作：记录 systemctl 调用，install.sh 的效果用回调模拟。
type fakeOps struct {
	calls []string
	// active 各单元是否 active；healthy 之外的回滚阶段也用它。
	inactive map[string]bool
	// versionOut 包内二进制 --version 的输出。
	versionOut string
	// install 模拟 install.sh：参数是解包目录。
	install func(dir string) (string, error)
	// failSystemctl 让匹配前缀的 systemctl 调用失败。
	failSystemctl string
	// installedPath / installedVersionOut 安装位置上的二进制 --version 输出（为空则与 versionOut 相同）。
	installedPath, installedVersionOut string
}

func (f *fakeOps) CopyNoFollow(src, dst string, limit int64) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() > limit {
		return fmt.Errorf("%s exceeds limit", src)
	}
	return copyFile(src, dst, 0o600)
}

func (f *fakeOps) Systemctl(_ context.Context, args ...string) error {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failSystemctl != "" && strings.HasPrefix(call, f.failSystemctl) {
		return errors.New("systemctl failed")
	}
	return nil
}

func (f *fakeOps) IsActive(_ context.Context, unit string) bool { return !f.inactive[unit] }

func (f *fakeOps) RunInstaller(_ context.Context, dir string) (string, error) {
	f.calls = append(f.calls, "install.sh")
	return f.install(dir)
}

func (f *fakeOps) BinaryVersion(_ context.Context, path string) (string, error) {
	if f.installedVersionOut != "" && path == f.installedPath {
		return f.installedVersionOut, nil
	}
	return f.versionOut, nil
}

// rig 一套临时目录里的「设备」：已安装的旧文件、下载目录、健康接口。
type rig struct {
	t        *testing.T
	in       *Installer
	ops      *fakeOps
	rel      *testRelease
	priv     ed25519.PrivateKey
	bin      string
	unit     string
	newUnit  string
	db       string
	wal      string
	dbOK     atomic.Bool
	healthUp atomic.Bool
}

func newRig(t *testing.T, current, target string) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t}
	r.rel, r.priv = newTestRelease(t, target)
	r.bin, r.unit, r.newUnit = filepath.Join(root, "opt/action-backup"), filepath.Join(root, "etc/action-backup.service"), filepath.Join(root, "etc/action-backup-update.timer")
	r.db, r.wal = filepath.Join(root, "var/backup.db"), filepath.Join(root, "var/backup.db-wal")
	for path, body := range map[string]string{r.bin: "OLD-BINARY", r.unit: "OLD-UNIT", r.db: "OLD-DB"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.dbOK.Store(true)
	r.healthUp.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !r.healthUp.Load() {
			panic(http.ErrAbortHandler)
		}
		// NAS 不在线时接口是 503，但数据库正常：不应因此回滚
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "degraded", "database": map[string]any{"ok": r.dbOK.Load()}}})
	}))
	t.Cleanup(srv.Close)

	r.ops = &fakeOps{inactive: map[string]bool{}, versionOut: fmt.Sprintf("action-backup %s (commit abc, built now, faultinject=false)\n", target)}
	// 默认的 install.sh：换二进制与单元、新增一个单元，新版本启动后还会改库、产生 wal
	r.ops.install = func(dir string) (string, error) {
		if got, _ := os.ReadFile(filepath.Join(dir, "action-backup")); string(got) != "NEW-BINARY-"+target {
			return "", fmt.Errorf("unexpected package content %q", got)
		}
		for path, body := range map[string]string{r.bin: "NEW-BINARY-" + target, r.unit: "NEW-UNIT", r.newUnit: "NEW-TIMER", r.db: "MIGRATED-DB", r.wal: "WAL"} {
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return "", err
			}
		}
		return "安装完成，尚未启动。\n", nil
	}
	upd := filepath.Join(root, "var/update")
	r.in = &Installer{
		Paths: InstallPaths{UpdateDir: upd, WorkDir: filepath.Join(root, "opt/update-work"), PrevDir: filepath.Join(root, "opt/prev"),
			ResultPath: filepath.Join(root, "opt/update-result.json"), Managed: []string{r.bin, r.unit, r.newUnit, r.db, r.wal}},
		Ops: r.ops, PublicKey: r.rel.pub, Current: current, OS: "linux", Arch: "arm64", BinaryPath: r.bin,
		MainUnit: "action-backup.service", Units: []string{"action-backup-mounter.service", "action-backup.service"},
		HealthURL: srv.URL, HealthTimeout: 60 * time.Millisecond, HealthInterval: 10 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r.stageDownloads()
	return r
}

// stageDownloads 模拟主服务下载好三个文件并写了请求标记。
func (r *rig) stageDownloads() {
	r.t.Helper()
	d := r.in.Paths.UpdateDir
	if err := os.MkdirAll(d, 0o750); err != nil {
		r.t.Fatal(err)
	}
	for name, body := range map[string][]byte{SumsName: r.rel.sums, SigName: r.rel.sig, r.rel.pkgName: r.rel.pkg, RequestName: []byte(`{"version":"99.0.0"}`)} {
		if err := os.WriteFile(filepath.Join(d, name), body, 0o640); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) read(path string) string {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "<absent>"
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// assertOld 一切回到安装前：旧二进制、旧单元、旧库，新版本新增的单元与 wal 被删掉。
func (r *rig) assertOld() {
	r.t.Helper()
	got := []string{r.read(r.bin), r.read(r.unit), r.read(r.newUnit), r.read(r.db), r.read(r.wal)}
	want := []string{"OLD-BINARY", "OLD-UNIT", "<absent>", "OLD-DB", "<absent>"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		r.t.Fatalf("files after rollback: %v, want %v", got, want)
	}
}

func (r *rig) result() Result {
	r.t.Helper()
	var res Result
	if err := json.Unmarshal([]byte(r.read(r.in.Paths.ResultPath)), &res); err != nil {
		r.t.Fatalf("result file: %v", err)
	}
	return res
}

func (r *rig) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestInstallerNoRequest(t *testing.T) {
	r := newRig(t, "0.8.0", "0.9.0")
	_ = os.Remove(filepath.Join(r.in.Paths.UpdateDir, RequestName))
	res, err := r.in.Run(context.Background())
	if res != nil || err != nil || len(r.ops.calls) != 0 || r.exists(r.in.Paths.ResultPath) {
		t.Fatalf("no request must be a no-op: %+v %v %v", res, err, r.ops.calls)
	}
}

func TestInstallerSuccess(t *testing.T) {
	r := newRig(t, "0.8.0", "0.9.0")
	res, err := r.in.Run(context.Background())
	if err != nil || !res.OK || res.From != "0.8.0" || res.To != "0.9.0" || res.RolledBack || !strings.Contains(res.Log, "安装完成") {
		t.Fatalf("result: %+v err %v", res, err)
	}
	want := "stop action-backup.service|install.sh|daemon-reload|restart action-backup-mounter.service action-backup.service"
	if got := strings.Join(r.ops.calls, "|"); got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
	if r.read(r.bin) != "NEW-BINARY-0.9.0" || r.read(r.unit) != "NEW-UNIT" || r.read(r.db) != "MIGRATED-DB" {
		t.Fatal("new files must stay in place")
	}
	if onDisk := r.result(); !onDisk.OK || onDisk.To != "0.9.0" || onDisk.FinishedAt == "" {
		t.Fatalf("result file: %+v", onDisk)
	}
	// 请求标记、下载的包、工作目录都清掉；备份留着但没有 installing 标记
	for _, p := range []string{filepath.Join(r.in.Paths.UpdateDir, RequestName), filepath.Join(r.in.Paths.UpdateDir, r.rel.pkgName),
		filepath.Join(r.in.Paths.UpdateDir, SumsName), r.in.Paths.WorkDir, filepath.Join(r.in.Paths.PrevDir, installingFlag)} {
		if r.exists(p) {
			t.Fatalf("%s should be gone", p)
		}
	}
	if !r.exists(filepath.Join(r.in.Paths.PrevDir, manifestName)) {
		t.Fatal("backup manifest should be kept")
	}
	// 再跑一次：没有请求，什么都不做
	r.ops.calls = nil
	if res, err := r.in.Run(context.Background()); res != nil || err != nil || len(r.ops.calls) != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
}

// 验签阶段的各种拒绝：都发生在停服务之前，系统文件一个都不动。
func TestInstallerRejectsBeforeTouchingSystem(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *rig)
		step   string
		errHas string
	}{
		"签名被篡改": {func(r *rig) {
			_ = os.WriteFile(filepath.Join(r.in.Paths.UpdateDir, SigName), make([]byte, ed25519.SignatureSize), 0o640)
		}, "verify", "signature verification failed"},
		"清单被改（签名不再匹配）": {func(r *rig) {
			_ = os.WriteFile(filepath.Join(r.in.Paths.UpdateDir, SumsName), append([]byte("x"), r.rel.sums...), 0o640)
		}, "verify", "signature verification failed"},
		"包被替换": {func(r *rig) {
			_ = os.WriteFile(filepath.Join(r.in.Paths.UpdateDir, r.rel.pkgName), []byte("evil"), 0o640)
		}, "verify", "sha256 mismatch"},
		"缺签名文件": {func(r *rig) { _ = os.Remove(filepath.Join(r.in.Paths.UpdateDir, SigName)) }, "stage", "SHA256SUMS.sig"},
		"包是符号链接": {func(r *rig) {
			p := filepath.Join(r.in.Paths.UpdateDir, r.rel.pkgName)
			_ = os.Remove(p)
			_ = os.Symlink(r.db, p)
		}, "stage", "not a regular file"},
		"版本不高于当前（降级）":  {func(r *rig) { r.in.Current = "0.9.0" }, "verify", "not newer"},
		"测试构建":         {func(r *rig) { r.ops.versionOut = "action-backup 0.9.0 (commit a, built b, faultinject=true)" }, "inspect", "faultinject"},
		"包内二进制版本与清单不符": {func(r *rig) { r.ops.versionOut = "action-backup 0.9.1 (commit a, built b, faultinject=false)" }, "inspect", "expected 0.9.0"},
		"包里有符号链接": {func(r *rig) {
			r.rel.pkg = buildPackage(t, strings.TrimSuffix(r.rel.pkgName, ".tar.gz"), map[string]string{"action-backup": "x", "install.sh": "x"}, func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
			})
			r.rel.resign(r.priv)
			r.stageDownloads()
		}, "extract", "unsupported type"},
		"包里有越界路径": {func(r *rig) {
			r.rel.pkg = buildPackage(t, strings.TrimSuffix(r.rel.pkgName, ".tar.gz"), map[string]string{"action-backup": "x", "install.sh": "x"}, func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Name: "../../escape", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("x"))
			})
			r.rel.resign(r.priv)
			r.stageDownloads()
		}, "extract", "escapes destination"},
		"包里没有 install.sh": {func(r *rig) {
			r.rel.pkg = buildPackage(t, strings.TrimSuffix(r.rel.pkgName, ".tar.gz"), map[string]string{"action-backup": "x"}, nil)
			r.rel.resign(r.priv)
			r.stageDownloads()
		}, "extract", "missing install.sh"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "0.8.0", "0.9.0")
			tc.mutate(r)
			res, err := r.in.Run(context.Background())
			if err == nil || res == nil || res.OK || res.Step != tc.step || !strings.Contains(res.Error, tc.errHas) {
				t.Fatalf("result %+v err %v; want step %s containing %q", res, err, tc.step, tc.errHas)
			}
			if len(r.ops.calls) != 0 || res.RolledBack {
				t.Fatalf("must fail before touching the system: calls %v rolledBack %v", r.ops.calls, res.RolledBack)
			}
			if r.read(r.bin) != "OLD-BINARY" || r.exists(r.in.Paths.PrevDir) {
				t.Fatal("system files must be untouched")
			}
			if r.exists(filepath.Join(r.in.Paths.UpdateDir, RequestName)) || r.exists(r.in.Paths.WorkDir) {
				t.Fatal("request marker and work dir must be removed so the failure is not retried every minute")
			}
			if onDisk := r.result(); onDisk.OK || onDisk.Step != tc.step {
				t.Fatalf("result file: %+v", onDisk)
			}
		})
	}
}

func TestInstallerRollsBack(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *rig)
		step   string
	}{
		"install.sh 失败": {func(r *rig) {
			orig := r.ops.install
			r.ops.install = func(dir string) (string, error) {
				_, _ = orig(dir) // 装到一半才失败
				return "sha256sum: WARNING: 1 computed checksum did NOT match\n", errors.New("exit status 1")
			}
		}, "install"},
		"install.sh 成功但装上的不是目标版本": {func(r *rig) {
			r.ops.installedPath, r.ops.installedVersionOut = r.bin, "action-backup 0.8.0 (commit a, built b, faultinject=false)"
		}, "install"},
		"重启失败": {func(r *rig) { r.ops.failSystemctl = "restart" }, "restart"},
		"重启后主服务没起来": {func(r *rig) {
			orig := r.ops.install
			r.ops.install = func(dir string) (string, error) {
				// 新版本起不来
				r.ops.inactive["action-backup.service"] = true
				return orig(dir)
			}
		}, "health"},
		"重启后数据库不正常": {func(r *rig) {
			orig := r.ops.install
			r.ops.install = func(dir string) (string, error) { r.dbOK.Store(false); return orig(dir) }
		}, "health"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "0.8.0", "0.9.0")
			tc.mutate(r)
			res, err := r.in.Run(context.Background())
			if err == nil || res.OK || res.Step != tc.step || !res.RolledBack {
				t.Fatalf("result %+v err %v; want rollback at %s", res, err, tc.step)
			}
			r.assertOld()
			if !strings.Contains(strings.Join(r.ops.calls, "|"), "install.sh") || r.ops.calls[0] != "stop action-backup.service" {
				t.Fatalf("calls: %v", r.ops.calls)
			}
			last := r.ops.calls[len(r.ops.calls)-1]
			if last != "restart action-backup-mounter.service action-backup.service" {
				t.Fatalf("rollback must end with a restart, got %q", last)
			}
			if onDisk := r.result(); onDisk.OK || !onDisk.RolledBack || onDisk.Step != tc.step || onDisk.To != "0.9.0" {
				t.Fatalf("result file: %+v", onDisk)
			}
		})
	}
}

// 回滚后仍不健康：保留 installing 标记（现场），下一次定时触发再回滚一遍。
func TestInstallerRollbackUnhealthyKeepsFlagAndRecovers(t *testing.T) {
	r := newRig(t, "0.8.0", "0.9.0")
	r.ops.inactive["action-backup.service"] = true
	res, err := r.in.Run(context.Background())
	if err == nil || !res.RolledBack || res.RollbackError == "" || !strings.Contains(res.RollbackError, "not active") {
		t.Fatalf("result %+v err %v", res, err)
	}
	r.assertOld()
	flag := filepath.Join(r.in.Paths.PrevDir, installingFlag)
	if !r.exists(flag) {
		t.Fatal("installing flag must stay when rollback could not be confirmed")
	}

	// 下一次触发（没有新请求）：发现标记，先恢复；这次服务正常，标记清除
	r.ops.inactive["action-backup.service"] = false
	_ = os.WriteFile(r.bin, []byte("HALF-WRITTEN"), 0o755) // 模拟断电留下的半截文件
	r.ops.calls = nil
	res, err = r.in.Run(context.Background())
	if err == nil || res.Step != "recover" || !res.RolledBack || res.RollbackError != "" {
		t.Fatalf("recover result %+v err %v", res, err)
	}
	r.assertOld()
	if r.exists(flag) {
		t.Fatal("installing flag must be cleared after a clean recovery")
	}
	r.ops.calls = nil
	if res, err := r.in.Run(context.Background()); res != nil || err != nil || len(r.ops.calls) != 0 {
		t.Fatalf("after recovery the installer must be idle: %+v %v %v", res, err, r.ops.calls)
	}
}

// 停服务失败或备份失败：还没动系统文件，不需要回滚。
func TestInstallerStopFailure(t *testing.T) {
	r := newRig(t, "0.8.0", "0.9.0")
	r.ops.failSystemctl = "stop"
	res, err := r.in.Run(context.Background())
	if err == nil || res.Step != "stop" || res.RolledBack || r.read(r.bin) != "OLD-BINARY" {
		t.Fatalf("result %+v err %v", res, err)
	}
	if strings.Contains(strings.Join(r.ops.calls, "|"), "install.sh") {
		t.Fatalf("installer must not run: %v", r.ops.calls)
	}
}
