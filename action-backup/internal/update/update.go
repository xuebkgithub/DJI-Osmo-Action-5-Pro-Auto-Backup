// Package update 检查 GitHub Releases 上是否有新版本，并把结果提供给 API 与事件总线。
//
// 检查与提示（update.go）只发 HTTPS GET；下载与安装请求（install.go）只写自己的更新目录，
// 这两部分都跑在无特权的主服务里。真正的安装由 root 定时任务执行 Installer（installer.go）：
// 只信内嵌公钥验过签名的内容，执行包内 install.sh、重启服务、健康检查、失败回滚。见架构文档 V1.6 第 19 章。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/mod/semver"

	"action-backup/internal/config"
	"action-backup/internal/events"
)

// Repository 发布仓库（owner/name）。写死在代码里，不进配置白名单：网页不能把更新源指到别处。
const Repository = "xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup"

const (
	// maxResponseBytes Releases 列表响应体上限。
	maxResponseBytes = 4 << 20
	// maxNotesBytes 发布说明经 API 输出的上限，超出截断。
	maxNotesBytes = 16 << 10
	// releasesPerPage 一次取回的 Release 数；只看最近这些就足够找到最新版。
	releasesPerPage = 30
)

// Asset 与本机平台匹配的安装包。
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// Release 一个可用的发布版本。
type Release struct {
	// Version 不带前导 v，如 0.9.0。
	Version        string `json:"version"`
	Tag            string `json:"tag"`
	Name           string `json:"name,omitempty"`
	URL            string `json:"url"`
	Notes          string `json:"notes,omitempty"`
	NotesTruncated bool   `json:"notes_truncated,omitempty"`
	Prerelease     bool   `json:"prerelease"`
	PublishedAt    string `json:"published_at,omitempty"`
	// Asset 本机平台对应的安装包；该版本没有发布本平台的包时为空。
	Asset *Asset `json:"asset,omitempty"`
	// Signed 该版本是否带 SHA256SUMS 与 SHA256SUMS.sig；没有签名的版本只能手动安装。
	Signed bool `json:"signed"`

	sumsURL, sigURL string
}

// Status 更新检查的当前状态，GET /api/v1/system/update 原样输出。
type Status struct {
	CurrentVersion string `json:"current_version"`
	// DevBuild 当前是开发构建：不做定期检查，也不判定「有新版本」。
	DevBuild bool `json:"dev_build"`
	// Platform 本机平台，如 linux-arm64。
	Platform           string `json:"platform"`
	Repository         string `json:"repository"`
	CheckEnabled       bool   `json:"check_enabled"`
	CheckIntervalHours int    `json:"check_interval_hours"`
	Channel            string `json:"channel"`
	Checking           bool   `json:"checking"`
	// CheckedAt 最近一次成功检查的时间。
	CheckedAt string `json:"checked_at,omitempty"`
	// NextCheckAt 下一次定期检查的计划时间；未启用或开发构建时为空。
	NextCheckAt string `json:"next_check_at,omitempty"`
	// Latest 当前通道上的最新版本；从未成功检查过时为空。
	Latest          *Release `json:"latest,omitempty"`
	UpdateAvailable bool     `json:"update_available"`
	// LastError 最近一次检查失败的原因；之后检查成功即清空。
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
	// InstallSupported 是否支持网页一键安装（Linux 正式构建且装了更新定时任务）；否则新版本需手动安装。
	InstallSupported bool `json:"install_supported"`
	// Install 下载 / 安装请求的进度与上一次安装的结果。
	Install InstallStatus `json:"install"`
}

// Options 构造参数；零值取默认。
type Options struct {
	// Current 当前运行的版本串（version.Version）。
	Current string
	// OS / Arch 本机平台，默认 runtime.GOOS / runtime.GOARCH。
	OS, Arch string
	// Repository 默认 Repository 常量；测试可替换。
	Repository string
	// Client HTTP 客户端，默认 20 秒超时、走环境变量代理（HTTPS_PROXY）。
	Client *http.Client
	// StartDelay 启动后首次定期检查前的等待，默认 30 秒（等网络就绪，也避免开机抢资源）。
	StartDelay time.Duration
	// RetryDelay 定期检查失败后的重试间隔上限，默认 30 分钟；实际取它与检查间隔的较小值。
	RetryDelay time.Duration
	// Now 取当前时间，测试可替换。
	Now func() time.Time

	// Install 安装请求相关参数；零值表示本进程不支持安装。
	Install InstallOptions
}

// Checker 更新检查器。
type Checker struct {
	bus  *events.Broadcaster
	log  *slog.Logger
	opts Options

	// checkMu 串行化检查：定期检查与手动检查不并发。
	checkMu sync.Mutex

	mu          sync.Mutex
	cfg         config.UpdateConfig
	checking    bool
	checkedAt   time.Time
	nextCheckAt time.Time
	latest      *Release
	lastErr     string
	lastErrAt   time.Time
	// notified 已发过 update.available 事件的版本，避免每次检查都重复提示。
	notified string
	// failing 上一次检查是失败的；只在「成功 → 失败」时发一次 update.check_failed。
	failing bool

	wake chan struct{}

	inst installState
}

// New 创建检查器；Run 之后才会定期检查。
func New(cfg config.UpdateConfig, bus *events.Broadcaster, log *slog.Logger, opts Options) *Checker {
	if log == nil {
		log = slog.Default()
	}
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}
	if opts.Repository == "" {
		opts.Repository = Repository
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if opts.StartDelay <= 0 {
		opts.StartDelay = 30 * time.Second
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 30 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Install = opts.Install.withDefaults()
	return &Checker{bus: bus, log: log, opts: opts, cfg: cfg, wake: make(chan struct{}, 1)}
}

// Configure 应用（新）配置。通道变化时丢弃旧结果，并唤醒定期循环按新配置重新排期。
func (c *Checker) Configure(cfg config.UpdateConfig) {
	c.mu.Lock()
	if cfg.Channel != c.cfg.Channel || cfg.APIBaseURL != c.cfg.APIBaseURL {
		c.latest = nil
		c.checkedAt = time.Time{}
	}
	c.cfg = cfg
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Status 返回当前状态快照。
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Checker) statusLocked() Status {
	dev := IsDevBuild(c.opts.Current)
	st := Status{
		CurrentVersion:     displayVersion(c.opts.Current),
		DevBuild:           dev,
		Platform:           c.opts.OS + "-" + c.opts.Arch,
		Repository:         c.opts.Repository,
		CheckEnabled:       c.cfg.ChecksEnabled(),
		CheckIntervalHours: c.cfg.CheckIntervalHours,
		Channel:            c.cfg.Channel,
		Checking:           c.checking,
		LastError:          c.lastErr,
		CheckedAt:          rfc3339(c.checkedAt),
		LastErrorAt:        rfc3339(c.lastErrAt),
		InstallSupported:   c.opts.Install.Supported,
		Install:            c.installStatusLocked(),
	}
	if c.periodicLocked() {
		st.NextCheckAt = rfc3339(c.nextCheckAt)
	}
	if c.latest != nil {
		r := *c.latest
		st.Latest = &r
		st.UpdateAvailable = !dev && newer(r.Version, c.opts.Current)
	}
	return st
}

// periodicLocked 是否应做定期检查：配置开启且不是开发构建。
func (c *Checker) periodicLocked() bool {
	return c.cfg.ChecksEnabled() && !IsDevBuild(c.opts.Current)
}

// Run 定期检查，直到 ctx 取消。未启用或开发构建时只等待配置变化。
func (c *Checker) Run(ctx context.Context) {
	c.startInstall(ctx)
	delay := c.opts.StartDelay
	for {
		c.mu.Lock()
		active := c.periodicLocked()
		if active {
			c.nextCheckAt = c.opts.Now().Add(delay)
		} else {
			c.nextCheckAt = time.Time{}
		}
		c.mu.Unlock()

		var timer <-chan time.Time
		var t *time.Timer
		if active {
			t = time.NewTimer(delay)
			timer = t.C
		}
		select {
		case <-ctx.Done():
			if t != nil {
				t.Stop()
			}
			return
		case <-c.wake:
			if t != nil {
				t.Stop()
			}
			// 配置变了：重新排期。刚启用时不必等满一个周期，沿用启动延迟尽快查一次。
			delay = c.opts.StartDelay
			continue
		case <-timer:
		}

		_, err := c.Check(ctx)
		c.mu.Lock()
		interval := time.Duration(c.cfg.CheckIntervalHours) * time.Hour
		c.mu.Unlock()
		if interval <= 0 {
			interval = 12 * time.Hour
		}
		delay = interval
		if err != nil && c.opts.RetryDelay < interval {
			delay = c.opts.RetryDelay
		}
	}
}

// Check 立即检查一次并返回最新状态。失败时状态里仍带上一次成功的结果，错误同时写进 LastError。
func (c *Checker) Check(ctx context.Context) (Status, error) {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()

	c.mu.Lock()
	cfg := c.cfg
	c.checking = true
	c.mu.Unlock()

	rel, err := c.fetchLatest(ctx, cfg)

	c.mu.Lock()
	c.checking = false
	now := c.opts.Now()
	var publish []events.Event
	if err != nil {
		c.lastErr, c.lastErrAt = err.Error(), now
		if !c.failing {
			c.failing = true
			publish = append(publish, events.Event{Level: "warn", Type: "update.check_failed",
				Message: "update check failed: " + err.Error(), Data: map[string]any{"error": err.Error()}})
		}
	} else {
		c.failing = false
		c.lastErr, c.lastErrAt = "", time.Time{}
		c.checkedAt = now
		c.latest = rel
		if rel != nil && !IsDevBuild(c.opts.Current) && newer(rel.Version, c.opts.Current) && c.notified != rel.Version {
			c.notified = rel.Version
			data := map[string]any{"current": displayVersion(c.opts.Current), "version": rel.Version, "tag": rel.Tag,
				"url": rel.URL, "prerelease": rel.Prerelease}
			if rel.PublishedAt != "" {
				data["published_at"] = rel.PublishedAt
			}
			publish = append(publish, events.Event{Type: "update.available",
				Message: fmt.Sprintf("new version %s available (running %s)", rel.Version, displayVersion(c.opts.Current)), Data: data})
		}
	}
	st := c.statusLocked()
	c.mu.Unlock()

	if c.bus != nil {
		for _, e := range publish {
			c.bus.Publish(context.WithoutCancel(ctx), e)
		}
	}
	if err != nil {
		c.log.Warn("update check failed", "err", err)
	} else {
		c.log.Debug("update check done", "update_available", st.UpdateAvailable)
	}
	return st, err
}

// ghRelease GitHub Releases API 的响应项（只取用到的字段）。
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// ErrNoRelease 仓库在当前通道上没有任何可用发布。
var ErrNoRelease = errors.New("no release found")

// fetchLatest 取回当前通道上版本号最高的 Release。
func (c *Checker) fetchLatest(ctx context.Context, cfg config.UpdateConfig) (*Release, error) {
	base := strings.TrimRight(cfg.APIBaseURL, "/")
	if base == "" {
		base = config.DefaultUpdateAPIBaseURL
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", base, c.opts.Repository, releasesPerPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "action-backup/"+displayVersion(c.opts.Current))
	resp, err := c.opts.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, statusError(resp)
	}
	var list []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	rel := c.pick(list, cfg.Channel)
	if rel == nil {
		return nil, ErrNoRelease
	}
	return rel, nil
}

// statusError 把非 200 响应翻译成可读错误；匿名访问被限流时给出恢复时间。
func statusError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return errors.New("repository or releases not found (HTTP 404)")
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if sec, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
				return fmt.Errorf("GitHub API rate limit exceeded, resets at %s", time.Unix(sec, 0).UTC().Format(time.RFC3339))
			}
			return errors.New("GitHub API rate limit exceeded")
		}
	}
	return fmt.Errorf("unexpected response: HTTP %d", resp.StatusCode)
}

// pick 在列表里选出通道允许的最高版本。draft 一律跳过；stable 通道跳过预发布。
func (c *Checker) pick(list []ghRelease, channel string) *Release {
	var best *ghRelease
	var bestVer string
	for i := range list {
		r := &list[i]
		v := canonical(r.TagName)
		if v == "" || r.Draft {
			continue
		}
		pre := r.Prerelease || isPrereleaseVersion(v)
		if pre && channel != config.UpdateChannelPrerelease {
			continue
		}
		if best == nil || semver.Compare(v, bestVer) > 0 {
			best, bestVer = r, v
		}
	}
	if best == nil {
		return nil
	}
	ver := displayVersion(bestVer)
	out := &Release{Version: ver, Tag: best.TagName, Name: best.Name, URL: best.HTMLURL,
		Prerelease: best.Prerelease || isPrereleaseVersion(bestVer), PublishedAt: rfc3339(best.PublishedAt)}
	out.Notes, out.NotesTruncated = truncate(best.Body, maxNotesBytes)
	want := AssetName(ver, c.opts.OS, c.opts.Arch)
	for _, a := range best.Assets {
		switch a.Name {
		case want:
			out.Asset = &Asset{Name: a.Name, Size: a.Size, URL: a.BrowserDownloadURL}
		case SumsName:
			out.sumsURL = a.BrowserDownloadURL
		case SigName:
			out.sigURL = a.BrowserDownloadURL
		}
	}
	out.Signed = out.sumsURL != "" && out.sigURL != ""
	return out
}

// AssetName 发布流水线（scripts/package.sh）产出的安装包文件名。
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("action-backup-%s-%s-%s.tar.gz", displayVersion(version), goos, goarch)
}

// truncate 按字节上限截断，不切断 UTF-8 字符。
func truncate(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
