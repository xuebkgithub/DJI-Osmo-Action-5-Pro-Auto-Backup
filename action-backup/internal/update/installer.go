package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// InstallOps root 侧需要的操作系统能力，由 internal/platform/linux 实现；测试用假实现。
type InstallOps interface {
	// CopyNoFollow 把 src 复制到 dst。src 来自不可信目录：必须是普通文件，不跟随符号链接，超过 limit 报错。
	CopyNoFollow(src, dst string, limit int64) error
	// Systemctl 执行 systemctl <args...>。
	Systemctl(ctx context.Context, args ...string) error
	// IsActive 单元是否处于 active。
	IsActive(ctx context.Context, unit string) bool
	// RunInstaller 在解包目录里执行 install.sh，返回合并输出。
	RunInstaller(ctx context.Context, dir string) (string, error)
	// BinaryVersion 执行 <path> --version 并返回输出。
	BinaryVersion(ctx context.Context, path string) (string, error)
}

// InstallPaths 安装涉及的路径。除 UpdateDir 外都只有 root 可写。
type InstallPaths struct {
	// UpdateDir 主服务的下载目录，不可信输入。
	UpdateDir string
	// WorkDir root 专属工作目录：验签、解包都在这里做。
	WorkDir string
	// PrevDir 安装前的备份，回滚用。
	PrevDir string
	// ResultPath 结果文件，主服务只读。
	ResultPath string
	// Managed 安装会改动、回滚时要恢复的文件（二进制、单元、udev 规则、数据库及其 -wal / -shm）。
	Managed []string
}

// Installer root 侧的安装流程。
type Installer struct {
	Paths InstallPaths
	Ops   InstallOps
	// PublicKey 验签公钥；nil 时用内嵌的发布公钥。
	PublicKey ed25519.PublicKey
	// Current 当前安装的版本（即执行本流程的二进制的版本）。
	Current  string
	OS, Arch string
	// BinaryPath 安装后的二进制位置；非空时在 install.sh 之后再跑一次 --version，确认装上的确实是目标版本。
	BinaryPath string
	// MainUnit 安装前要先停的主服务；Units 安装后要重启并检查的全部单元（顺序即重启顺序）。
	MainUnit string
	Units    []string
	// HealthURL 主服务健康检查地址，如 http://127.0.0.1:8080/api/v1/system/health。
	HealthURL string
	// HealthTimeout 重启后等待健康的时限，默认 90 秒；HealthInterval 轮询间隔，默认 3 秒。
	HealthTimeout  time.Duration
	HealthInterval time.Duration
	HTTP           *http.Client
	Log            *slog.Logger
	Now            func() time.Time
}

// installingFlag 备份完成后、安装确认成功前存在于 PrevDir；启动时发现它说明上次安装被打断，先回滚。
const installingFlag = "installing"

const manifestName = "manifest.json"

type manifestEntry struct {
	Path string `json:"path"`
	// Present 备份时该文件是否存在；不存在的在回滚时要删掉（例如新版本新增的单元）。
	Present bool        `json:"present"`
	Backup  string      `json:"backup,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

// stepError 带步骤名的错误，步骤名进结果文件。
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func at(step string, err error) error {
	if err == nil {
		return nil
	}
	return &stepError{step: step, err: err}
}

func (in *Installer) defaults() {
	if in.HealthTimeout <= 0 {
		in.HealthTimeout = 90 * time.Second
	}
	if in.HealthInterval <= 0 {
		in.HealthInterval = 3 * time.Second
	}
	if in.HTTP == nil {
		in.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if in.Log == nil {
		in.Log = slog.Default()
	}
	if in.Now == nil {
		in.Now = time.Now
	}
}

// Run 处理一次安装请求。没有请求时返回 (nil, nil)。
// 有请求时一定写结果文件：成功返回 (result, nil)；失败返回 (result, err)，此时已尽力回滚。
func (in *Installer) Run(ctx context.Context) (*Result, error) {
	in.defaults()
	started := in.Now()

	// 上次安装被打断（断电、进程被杀）：先恢复到安装前的状态
	if _, err := os.Stat(filepath.Join(in.Paths.PrevDir, installingFlag)); err == nil {
		in.Log.Warn("interrupted install found, rolling back")
		res := &Result{From: displayVersion(in.Current), Step: "recover", Error: "previous install was interrupted", StartedAt: rfc3339(started)}
		in.rollback(ctx, res)
		_ = os.Remove(filepath.Join(in.Paths.UpdateDir, RequestName))
		in.finish(res)
		return res, errors.New("previous install was interrupted; rolled back")
	}

	request := filepath.Join(in.Paths.UpdateDir, RequestName)
	if _, err := os.Lstat(request); err != nil {
		return nil, nil
	}
	// 先删标记：无论成败都只处理一次，失败不会每分钟重试刷屏
	if err := os.Remove(request); err != nil {
		return nil, fmt.Errorf("remove request marker: %w", err)
	}

	res := &Result{From: displayVersion(in.Current), StartedAt: rfc3339(started)}
	err := in.install(ctx, res)
	if err != nil {
		var se *stepError
		if errors.As(err, &se) {
			res.Step, res.Error = se.step, se.err.Error()
		} else {
			res.Step, res.Error = "install", err.Error()
		}
	} else {
		res.OK = true
		in.cleanupDownloads()
	}
	_ = os.RemoveAll(in.Paths.WorkDir)
	in.finish(res)
	return res, err
}

func (in *Installer) finish(res *Result) {
	res.FinishedAt = rfc3339(in.Now())
	data, _ := json.MarshalIndent(res, "", "  ")
	if err := writeFileAtomic(in.Paths.ResultPath, data, 0o644); err != nil {
		in.Log.Error("write update result failed", "path", in.Paths.ResultPath, "err", err)
	}
}

// cleanupDownloads 成功后清掉下载目录里的包；只删固定名字与已验签清单里的文件名，失败无所谓。
func (in *Installer) cleanupDownloads() {
	entries, err := os.ReadDir(in.Paths.UpdateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if n == SumsName || n == SigName || strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".part") {
			_ = os.Remove(filepath.Join(in.Paths.UpdateDir, n))
		}
	}
}

func (in *Installer) install(ctx context.Context, res *Result) error {
	p := in.Paths
	if err := os.RemoveAll(p.WorkDir); err != nil {
		return at("prepare", err)
	}
	if err := os.MkdirAll(p.WorkDir, 0o700); err != nil {
		return at("prepare", err)
	}

	// 1. 把不可信目录里的文件复制进 root 专属目录，之后只看副本
	sumsPath, sigPath := filepath.Join(p.WorkDir, SumsName), filepath.Join(p.WorkDir, SigName)
	if err := in.Ops.CopyNoFollow(filepath.Join(p.UpdateDir, SumsName), sumsPath, maxSumsBytes); err != nil {
		return at("stage", err)
	}
	if err := in.Ops.CopyNoFollow(filepath.Join(p.UpdateDir, SigName), sigPath, maxSigBytes); err != nil {
		return at("stage", err)
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return at("stage", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return at("stage", err)
	}

	// 2. 验签；包名与版本只取自验过签的清单
	pub := in.PublicKey
	if pub == nil {
		if pub, err = ReleasePublicKey(); err != nil {
			return at("verify", err)
		}
	}
	if err := VerifySums(pub, sums, sig); err != nil {
		return at("verify", err)
	}
	name, sum, ver, err := PlatformPackage(sums, in.OS, in.Arch)
	if err != nil {
		return at("verify", err)
	}
	res.To = ver
	if !newer(ver, in.Current) {
		return at("verify", fmt.Errorf("package version %s is not newer than installed %s", ver, displayVersion(in.Current)))
	}
	pkgPath := filepath.Join(p.WorkDir, name)
	if err := in.Ops.CopyNoFollow(filepath.Join(p.UpdateDir, name), pkgPath, maxPackageBytes); err != nil {
		return at("stage", err)
	}
	if err := VerifyFile(pkgPath, sum); err != nil {
		return at("verify", err)
	}

	// 3. 解包并核对包内二进制
	pkgDir := filepath.Join(p.WorkDir, "pkg")
	if err := extractTarGz(pkgPath, pkgDir); err != nil {
		return at("extract", err)
	}
	root := filepath.Join(pkgDir, strings.TrimSuffix(name, ".tar.gz"))
	for _, f := range []string{"action-backup", "install.sh"} {
		if info, err := os.Lstat(filepath.Join(root, f)); err != nil || !info.Mode().IsRegular() {
			return at("extract", fmt.Errorf("package is missing %s", f))
		}
	}
	out, err := in.Ops.BinaryVersion(ctx, filepath.Join(root, "action-backup"))
	if err != nil {
		return at("inspect", fmt.Errorf("run packaged binary: %w", err))
	}
	binVer, faultinject, err := ParseVersionOutput(out)
	if err != nil {
		return at("inspect", err)
	}
	if faultinject {
		return at("inspect", errors.New("package contains a faultinject test build"))
	}
	if canonical(binVer) != canonical(ver) {
		return at("inspect", fmt.Errorf("packaged binary reports version %s, expected %s", binVer, ver))
	}

	// 4. 停主服务并备份。此后任何失败都要回滚
	if err := in.Ops.Systemctl(ctx, "stop", in.MainUnit); err != nil {
		return at("stop", err)
	}
	if err := in.backup(); err != nil {
		// 还没动任何东西：把主服务拉回来即可
		_ = in.Ops.Systemctl(ctx, "start", in.MainUnit)
		return at("backup", err)
	}

	// 5. 安装、重启、确认
	log, err := in.Ops.RunInstaller(ctx, root)
	res.Log = tail(log, 4<<10)
	if err == nil {
		err = in.checkInstalled(ctx, ver)
	}
	if err == nil {
		err = at("restart", in.restart(ctx))
	} else {
		err = at("install", err)
	}
	if err == nil {
		err = at("health", in.waitHealthy(ctx))
	}
	if err != nil {
		in.rollback(ctx, res)
		return err
	}
	return os.Remove(filepath.Join(p.PrevDir, installingFlag))
}

// checkInstalled install.sh 报告成功后，确认安装位置上的二进制就是目标版本。
func (in *Installer) checkInstalled(ctx context.Context, want string) error {
	if in.BinaryPath == "" {
		return nil
	}
	out, err := in.Ops.BinaryVersion(ctx, in.BinaryPath)
	if err != nil {
		return fmt.Errorf("run installed binary: %w", err)
	}
	got, _, err := ParseVersionOutput(out)
	if err != nil {
		return err
	}
	if canonical(got) != canonical(want) {
		return fmt.Errorf("installed binary reports version %s, expected %s", got, want)
	}
	return nil
}

func (in *Installer) restart(ctx context.Context) error {
	if err := in.Ops.Systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return in.Ops.Systemctl(ctx, append([]string{"restart"}, in.Units...)...)
}

// backup 把 Managed 里的文件复制到 PrevDir，最后才落 installing 标记：标记存在即表示备份完整。
func (in *Installer) backup() error {
	p := in.Paths
	if err := os.RemoveAll(p.PrevDir); err != nil {
		return err
	}
	if err := os.MkdirAll(p.PrevDir, 0o700); err != nil {
		return err
	}
	var manifest []manifestEntry
	for i, path := range p.Managed {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			manifest = append(manifest, manifestEntry{Path: path})
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		name := fmt.Sprintf("f%02d", i)
		if err := copyFile(path, filepath.Join(p.PrevDir, name), 0o600); err != nil {
			return err
		}
		manifest = append(manifest, manifestEntry{Path: path, Present: true, Backup: name, Mode: info.Mode().Perm()})
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writeFileAtomic(filepath.Join(p.PrevDir, manifestName), data, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.PrevDir, installingFlag), []byte(rfc3339(in.Now())), 0o600)
}

// rollback 按备份清单恢复文件并重启服务。尽力而为：每一步的错误都记下来，但不中断后面的步骤。
func (in *Installer) rollback(ctx context.Context, res *Result) {
	res.RolledBack = true
	var errs []string
	note := func(what string, err error) {
		if err != nil {
			errs = append(errs, what+": "+err.Error())
			in.Log.Error("rollback step failed", "step", what, "err", err)
		}
	}
	p := in.Paths
	note("stop", in.Ops.Systemctl(ctx, "stop", in.MainUnit))
	data, err := os.ReadFile(filepath.Join(p.PrevDir, manifestName))
	var manifest []manifestEntry
	if err == nil {
		err = json.Unmarshal(data, &manifest)
	}
	note("manifest", err)
	for _, e := range manifest {
		if !e.Present {
			if err := os.Remove(e.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				note("remove "+e.Path, err)
			}
			continue
		}
		// 属主沿用现存文件的（rename 前先写同目录临时文件）；数据库的属主由 restoreFile 保留
		note("restore "+e.Path, restoreFile(filepath.Join(p.PrevDir, e.Backup), e.Path, e.Mode))
	}
	note("restart", in.restart(ctx))
	if len(errs) == 0 {
		note("health", in.waitHealthy(ctx))
	}
	if len(errs) == 0 {
		// 回滚干净：标记可以去掉。否则留着现场，下次定时任务会再试一次回滚
		_ = os.Remove(filepath.Join(p.PrevDir, installingFlag))
	}
	res.RollbackError = strings.Join(errs, "; ")
}

type healthBody struct {
	Data struct {
		Database struct {
			OK bool `json:"ok"`
		} `json:"database"`
	} `json:"data"`
}

// waitHealthy 等全部单元 active 且主服务的健康接口应答、数据库正常。
// 不看接口的 HTTP 状态码：NAS 不在线时接口是 503（degraded），那不是更新造成的，不能因此回滚。
func (in *Installer) waitHealthy(ctx context.Context) error {
	deadline := in.Now().Add(in.HealthTimeout)
	var last error
	for {
		last = in.healthOnce(ctx)
		if last == nil {
			return nil
		}
		if !in.Now().Before(deadline) {
			return fmt.Errorf("not healthy after %s: %w", in.HealthTimeout, last)
		}
		t := time.NewTimer(in.HealthInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (in *Installer) healthOnce(ctx context.Context) error {
	for _, u := range in.Units {
		if !in.Ops.IsActive(ctx, u) {
			return fmt.Errorf("unit %s is not active", u)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.HealthURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body healthBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return fmt.Errorf("health response (HTTP %d): %w", resp.StatusCode, err)
	}
	if !body.Data.Database.OK {
		return fmt.Errorf("health reports database not ok (HTTP %d)", resp.StatusCode)
	}
	return nil
}

var versionLine = regexp.MustCompile(`(?m)^action-backup (\S+) \(.*faultinject=(true|false)\)\s*$`)

// ParseVersionOutput 解析 `action-backup --version` 的输出。
func ParseVersionOutput(out string) (ver string, faultinject bool, err error) {
	m := versionLine.FindStringSubmatch(out)
	if m == nil {
		return "", false, fmt.Errorf("unrecognized --version output: %q", tail(strings.TrimSpace(out), 200))
	}
	return m[1], m[2] == "true", nil
}

// 解包上限：单包展开后的总字节数与条目数。
const (
	maxExtractBytes   = 512 << 20
	maxExtractEntries = 2000
)

// extractTarGz 把 tar.gz 解到 dest。只接受目录与普通文件，拒绝链接、设备等其它类型，拒绝越出 dest 的路径。
func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if n >= maxExtractEntries {
			return errors.New("archive has too many entries")
		}
		if !filepath.IsLocal(h.Name) {
			return fmt.Errorf("archive entry escapes destination: %q", h.Name)
		}
		target := filepath.Join(dest, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; h.Size < 0 || total > maxExtractBytes {
				return errors.New("archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, h.Size)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %q", h.Name, string(h.Typeflag))
		}
	}
}

// copyFile 复制到一个新文件（root 自己的目录里）。
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// restoreFile 把备份写回原位：目标已存在时原地覆盖内容（保留属主，数据库文件属于服务用户）；
// 运行中的二进制不能原地写（ETXTBSY），所以先尝试「同目录临时文件 + rename」只在目标不存在或覆盖失败时使用。
func restoreFile(backup, dst string, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if _, err := os.Lstat(dst); err == nil {
		if err := copyFile(backup, dst, mode); err == nil {
			return os.Chmod(dst, mode)
		}
	}
	tmp := dst + ".rollback"
	if err := copyFile(backup, tmp, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// tail 取字符串末尾至多 n 字节（不保证落在字符边界，只用于日志摘要）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-n:], "")
}
