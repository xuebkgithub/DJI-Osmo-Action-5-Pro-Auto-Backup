package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"action-backup/internal/events"
)

// 更新目录里的文件名。目录本身由无特权的主服务所有，root 侧只把它当作不可信输入。
const (
	// RequestName 安装请求标记。内容只供排查，root 侧不读其中的版本号。
	RequestName = "request"
	// reportedName 记录已经发过事件的安装结果（finished_at），避免每次启动重复提示。
	reportedName = "reported"
)

// DirFor 由数据库路径推出更新目录：与数据库同级的 update/（设备上即 /var/lib/action-backup/update）。
func DirFor(databasePath string) string { return filepath.Join(filepath.Dir(databasePath), "update") }

// ResultPath root 侧写安装结果的位置：安装目录下、只有 root 可写、主服务可读。
// 不写进更新目录：那个目录归无特权用户所有，root 往里写文件会被符号链接利用。
func ResultPath(installDir string) string { return filepath.Join(installDir, "update-result.json") }

// TimerUnitPath 更新定时任务的单元文件；它存在才说明设备装了能自更新的版本。
const TimerUnitPath = "/etc/systemd/system/action-backup-update.timer"

// 安装阶段。
const (
	StageIdle        = "idle"
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	// StageWaitingIdle 包已就绪，等备份任务结束后再提交安装请求。
	StageWaitingIdle = "waiting_idle"
	// StageRequested 已写请求标记，等 root 定时任务（每分钟一次）接手；接手后服务会被重启。
	StageRequested = "requested"
	StageFailed    = "failed"
)

// InstallStatus 下载与安装请求的状态。
type InstallStatus struct {
	Supported bool   `json:"supported"`
	Stage     string `json:"stage"`
	// Version 正在处理的目标版本。
	Version         string `json:"version,omitempty"`
	DownloadedBytes int64  `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64  `json:"total_bytes,omitempty"`
	Error           string `json:"error,omitempty"`
	RequestedAt     string `json:"requested_at,omitempty"`
	// LastResult root 侧上一次安装的结果（成功或回滚）。
	LastResult *Result `json:"last_result,omitempty"`
}

// Result root 侧一次安装的结果，写在 InstallOptions.ResultPath。
type Result struct {
	OK   bool   `json:"ok"`
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	// Step 失败发生在哪一步。
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
	// RolledBack 是否执行了回滚；RollbackError 非空表示回滚本身也出了问题，需要人工处理。
	RolledBack    bool   `json:"rolled_back,omitempty"`
	RollbackError string `json:"rollback_error,omitempty"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	// Log install.sh 输出的末尾若干行。
	Log string `json:"log,omitempty"`
}

// InstallOptions 主服务侧的安装参数。
type InstallOptions struct {
	// Supported 本进程能否提交安装请求（Linux、正式构建、装了更新定时任务）。
	Supported bool
	// Dir 更新目录；Supported 为 true 时必填。
	Dir string
	// ResultPath root 侧写结果的文件；为空则不读结果。
	ResultPath string
	// Busy 是否有备份任务正在传输；为 true 时推迟提交安装请求。nil 视为不忙。
	Busy func() bool
	// PublicKey 验签公钥，默认内嵌的发布公钥；测试可替换。
	PublicKey ed25519.PublicKey
	// Client 下载用的 HTTP 客户端，默认不设总超时（靠 ctx 与 DownloadTimeout）。
	Client *http.Client
	// DownloadTimeout 一次安装请求里下载阶段的总时限，默认 30 分钟。
	DownloadTimeout time.Duration
	// Attempts / Backoff 单个文件的下载重试次数与间隔基数，默认 4 次 / 5 秒。
	Attempts int
	Backoff  time.Duration
	// IdlePoll 等待空闲时的轮询间隔，默认 5 秒。
	IdlePoll time.Duration
}

func (o InstallOptions) withDefaults() InstallOptions {
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	if o.DownloadTimeout <= 0 {
		o.DownloadTimeout = 30 * time.Minute
	}
	if o.Attempts <= 0 {
		o.Attempts = 4
	}
	if o.Backoff <= 0 {
		o.Backoff = 5 * time.Second
	}
	if o.IdlePoll <= 0 {
		o.IdlePoll = 5 * time.Second
	}
	return o
}

type installState struct {
	ctx         context.Context
	stage       string
	version     string
	done, total int64
	err         string
	requestedAt time.Time
	lastResult  *Result
	lastEmit    time.Time
}

// 安装请求的错误；API 据此给出不同的状态码。
var (
	ErrInstallUnsupported = errors.New("one-click install is not supported on this build or platform")
	ErrNoUpdate           = errors.New("no newer version to install; run a check first")
	ErrNotInstallable     = errors.New("release cannot be installed automatically")
	ErrInstallInProgress  = errors.New("an install is already in progress")
)

func (c *Checker) installStatusLocked() InstallStatus {
	st := InstallStatus{Supported: c.opts.Install.Supported, Stage: c.inst.stage, Version: c.inst.version,
		DownloadedBytes: c.inst.done, TotalBytes: c.inst.total, Error: c.inst.err, RequestedAt: rfc3339(c.inst.requestedAt)}
	if st.Stage == "" {
		st.Stage = StageIdle
	}
	if c.inst.lastResult != nil {
		r := *c.inst.lastResult
		st.LastResult = &r
	}
	return st
}

// startInstall 在 Run 开头调用：记下生命周期 ctx，读取上一次安装的结果并（只一次）发事件，
// 发现遗留的请求标记则把阶段恢复成 requested。
func (c *Checker) startInstall(ctx context.Context) {
	c.mu.Lock()
	c.inst.ctx = ctx
	o := c.opts.Install
	c.mu.Unlock()
	if o.ResultPath != "" {
		c.reportResult(ctx, o)
	}
	if !o.Supported || o.Dir == "" {
		return
	}
	if info, err := os.Stat(filepath.Join(o.Dir, RequestName)); err == nil {
		c.mu.Lock()
		c.inst.stage, c.inst.requestedAt = StageRequested, info.ModTime()
		c.mu.Unlock()
	}
}

// reportResult 读 root 侧的结果文件；该结果没报告过就发 update.installed / update.failed。
func (c *Checker) reportResult(ctx context.Context, o InstallOptions) {
	data, err := os.ReadFile(o.ResultPath)
	if err != nil {
		return
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil || r.FinishedAt == "" {
		c.log.Warn("unreadable update result", "path", o.ResultPath, "err", err)
		return
	}
	c.mu.Lock()
	c.inst.lastResult = &r
	c.mu.Unlock()
	if o.Dir == "" {
		return
	}
	marker := filepath.Join(o.Dir, reportedName)
	if prev, err := os.ReadFile(marker); err == nil && string(prev) == r.FinishedAt {
		return
	}
	if err := os.MkdirAll(o.Dir, 0o750); err == nil {
		_ = os.WriteFile(marker, []byte(r.FinishedAt), 0o640)
	}
	if c.bus == nil {
		return
	}
	data2 := map[string]any{"from": r.From, "to": r.To, "rolled_back": r.RolledBack, "finished_at": r.FinishedAt}
	if r.OK {
		c.bus.Publish(ctx, events.Event{Type: "update.installed",
			Message: fmt.Sprintf("updated from %s to %s", r.From, r.To), Data: data2})
		return
	}
	data2["step"], data2["error"] = r.Step, r.Error
	if r.RollbackError != "" {
		data2["rollback_error"] = r.RollbackError
	}
	c.bus.Publish(ctx, events.Event{Level: "error", Type: "update.failed",
		Message: fmt.Sprintf("update to %s failed at %s: %s", r.To, r.Step, r.Error), Data: data2})
}

// Apply 开始「下载 → 验签 → 等空闲 → 提交安装请求」，立即返回；进度看 Status().Install。
// 真正的安装由 root 定时任务完成，届时本服务会被重启。
func (c *Checker) Apply() (Status, error) {
	c.mu.Lock()
	o := c.opts.Install
	fail := func(err error) (Status, error) {
		st := c.statusLocked()
		c.mu.Unlock()
		return st, err
	}
	if !o.Supported || o.Dir == "" {
		return fail(ErrInstallUnsupported)
	}
	switch c.inst.stage {
	case StageDownloading, StageVerifying, StageWaitingIdle, StageRequested:
		return fail(ErrInstallInProgress)
	}
	rel := c.latest
	if rel == nil || IsDevBuild(c.opts.Current) || !newer(rel.Version, c.opts.Current) {
		return fail(ErrNoUpdate)
	}
	if rel.Asset == nil {
		return fail(fmt.Errorf("%w: no %s-%s package in release %s", ErrNotInstallable, c.opts.OS, c.opts.Arch, rel.Tag))
	}
	if !rel.Signed {
		return fail(fmt.Errorf("%w: release %s has no %s; install it manually", ErrNotInstallable, rel.Tag, SigName))
	}
	target := *rel
	ctx := c.inst.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	c.inst.stage, c.inst.version, c.inst.err = StageDownloading, target.Version, ""
	c.inst.done, c.inst.total = 0, target.Asset.Size
	st := c.statusLocked()
	c.mu.Unlock()

	go c.runInstall(ctx, o, target)
	return st, nil
}

func (c *Checker) setStage(stage string) {
	c.mu.Lock()
	c.inst.stage = stage
	c.mu.Unlock()
	c.emitProgress(true)
}

// emitProgress 向 SSE 推一条不落库的进度事件；非强制时每 500 ms 最多一条。
func (c *Checker) emitProgress(force bool) {
	if c.bus == nil {
		return
	}
	c.mu.Lock()
	now := c.opts.Now()
	if !force && now.Sub(c.inst.lastEmit) < 500*time.Millisecond {
		c.mu.Unlock()
		return
	}
	c.inst.lastEmit = now
	st := c.installStatusLocked()
	c.mu.Unlock()
	c.bus.PublishTransient(events.Event{Type: "update.progress", Message: "update " + st.Stage, Data: map[string]any{
		"stage": st.Stage, "version": st.Version, "downloaded_bytes": st.DownloadedBytes, "total_bytes": st.TotalBytes}})
}

func (c *Checker) runInstall(ctx context.Context, o InstallOptions, rel Release) {
	step, err := c.prepareInstall(ctx, o, rel)
	if err == nil {
		return
	}
	c.mu.Lock()
	c.inst.stage, c.inst.err = StageFailed, err.Error()
	c.mu.Unlock()
	c.emitProgress(true)
	c.log.Warn("update install request failed", "step", step, "version", rel.Version, "err", err)
	if c.bus != nil {
		c.bus.Publish(context.WithoutCancel(ctx), events.Event{Level: "error", Type: "update.failed",
			Message: fmt.Sprintf("update to %s failed at %s: %s", rel.Version, step, err),
			Data:    map[string]any{"from": displayVersion(c.opts.Current), "to": rel.Version, "step": step, "error": err.Error()}})
	}
}

// prepareInstall 返回失败所在的步骤名与错误；成功时已写好请求标记。
func (c *Checker) prepareInstall(ctx context.Context, o InstallOptions, rel Release) (string, error) {
	if err := os.MkdirAll(o.Dir, 0o750); err != nil {
		return "prepare", err
	}
	ua := "action-backup/" + displayVersion(c.opts.Current)
	sumsPath, sigPath, pkgPath := filepath.Join(o.Dir, SumsName), filepath.Join(o.Dir, SigName), filepath.Join(o.Dir, rel.Asset.Name)
	// 清掉别的版本留下的包与半截文件；同名的 .part 留着续传
	if entries, err := os.ReadDir(o.Dir); err == nil {
		for _, e := range entries {
			switch e.Name() {
			case rel.Asset.Name, rel.Asset.Name + ".part", reportedName:
			default:
				_ = os.RemoveAll(filepath.Join(o.Dir, e.Name()))
			}
		}
	}
	dctx, cancel := context.WithTimeout(ctx, o.DownloadTimeout)
	defer cancel()
	if err := fetchFile(dctx, o.Client, rel.sumsURL, sumsPath, ua, maxSumsBytes, o.Attempts, o.Backoff, nil); err != nil {
		return "download", err
	}
	if err := fetchFile(dctx, o.Client, rel.sigURL, sigPath, ua, maxSigBytes, o.Attempts, o.Backoff, nil); err != nil {
		return "download", err
	}
	// 先验签再下大文件：签名不对就不必浪费流量
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return "verify", err
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return "verify", err
	}
	pub := o.PublicKey
	if pub == nil {
		if pub, err = ReleasePublicKey(); err != nil {
			return "verify", err
		}
	}
	if err := VerifySums(pub, sums, sig); err != nil {
		return "verify", err
	}
	want, err := FindSum(sums, rel.Asset.Name)
	if err != nil {
		return "verify", err
	}
	// 上次下载好的包如果哈希对得上就直接用
	if VerifyFile(pkgPath, want) != nil {
		_ = os.Remove(pkgPath)
		err := fetchFile(dctx, o.Client, rel.Asset.URL, pkgPath, ua, maxPackageBytes, o.Attempts, o.Backoff, func(done, total int64) {
			c.mu.Lock()
			c.inst.done = done
			if total > 0 {
				c.inst.total = total
			}
			c.mu.Unlock()
			c.emitProgress(false)
		})
		if err != nil {
			return "download", err
		}
		c.setStage(StageVerifying)
		if err := VerifyFile(pkgPath, want); err != nil {
			_ = os.Remove(pkgPath)
			return "verify", err
		}
	}

	if o.Busy != nil && o.Busy() {
		c.setStage(StageWaitingIdle)
		t := time.NewTicker(o.IdlePoll)
		defer t.Stop()
		for o.Busy() {
			select {
			case <-ctx.Done():
				return "wait_idle", ctx.Err()
			case <-t.C:
			}
		}
	}

	now := c.opts.Now()
	body, _ := json.Marshal(map[string]string{"version": rel.Version, "requested_at": rfc3339(now)})
	tmp := filepath.Join(o.Dir, "."+RequestName+".tmp")
	if err := os.WriteFile(tmp, body, 0o640); err != nil {
		return "request", err
	}
	if err := os.Rename(tmp, filepath.Join(o.Dir, RequestName)); err != nil {
		return "request", err
	}
	c.mu.Lock()
	c.inst.stage, c.inst.requestedAt = StageRequested, now
	c.mu.Unlock()
	c.emitProgress(true)
	if c.bus != nil {
		c.bus.Publish(context.WithoutCancel(ctx), events.Event{Type: "update.requested",
			Message: fmt.Sprintf("update to %s downloaded and verified, waiting for installer", rel.Version),
			Data:    map[string]any{"from": displayVersion(c.opts.Current), "to": rel.Version}})
	}
	return "", nil
}
