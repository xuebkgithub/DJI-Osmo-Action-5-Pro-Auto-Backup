// Package notify 把事件总线上的关键事件推送到外部渠道（MeoW、Telegram）。
// 分发在独立 goroutine 中进行，发送失败只记事件，绝不阻塞备份流程。
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/events"
)

// Message 一条推送。
type Message struct {
	Title string
	Body  string
	// URL 可选链接（网页地址）。
	URL string
}

// Channel 推送渠道。
type Channel interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// 渠道名。
const (
	ChannelMeoW     = "meow"
	ChannelTelegram = "telegram"
)

// ErrChannelNotConfigured 渠道缺少必要配置。
var ErrChannelNotConfigured = errors.New("channel not configured")

// Options 分发参数；零值取默认。
type Options struct {
	// SendTimeout 单次 HTTP 请求超时。默认 10 秒。
	SendTimeout time.Duration
	// Retries 发送失败后的重试次数。默认 3。
	Retries int
	// RetryBackoff 首次重试等待，之后翻倍。默认 2 秒。
	RetryBackoff time.Duration
	// CoalesceWindow 失败事件合并窗口。默认 30 秒。
	CoalesceWindow time.Duration
	// MinInterval 同一渠道两条消息的最小间隔（MeoW 限 3 条 / 3 秒）。默认 1 秒。
	MinInterval time.Duration
	// QueueSize 待发送队列长度，满则丢弃最旧之外的新消息并记日志。默认 64。
	QueueSize int
}

func (o Options) withDefaults() Options {
	if o.SendTimeout <= 0 {
		o.SendTimeout = 10 * time.Second
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = 3
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = 2 * time.Second
	}
	if o.CoalesceWindow <= 0 {
		o.CoalesceWindow = 30 * time.Second
	}
	if o.MinInterval <= 0 {
		o.MinInterval = time.Second
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 64
	}
	return o
}

// Dispatcher 订阅事件总线、按配置过滤并推送。
type Dispatcher struct {
	bus  *events.Broadcaster
	log  *slog.Logger
	opts Options

	mu       sync.Mutex
	cfg      config.NotificationsConfig
	channels []Channel

	queue chan Message

	// failures 失败合并桶
	failMu    sync.Mutex
	failCount int
	failLast  string
	failTimer *time.Timer

	// newChannel 构造渠道；测试可替换。
	newChannel func(name string, cfg config.NotificationsConfig, timeout time.Duration) (Channel, error)

	// sent 测试观察：每次成功发送后调用。
	sent func(channel string, m Message)
}

// New 创建分发器；Configure 之后才会发送。
func New(bus *events.Broadcaster, log *slog.Logger, opts Options) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	opts = opts.withDefaults()
	return &Dispatcher{bus: bus, log: log, opts: opts, queue: make(chan Message, opts.QueueSize), newChannel: buildChannel}
}

// Configure 应用（新）配置：重建渠道列表。可在运行时反复调用。
func (d *Dispatcher) Configure(cfg config.NotificationsConfig) {
	var chans []Channel
	if cfg.Enabled {
		for _, name := range []string{ChannelMeoW, ChannelTelegram} {
			if !channelEnabled(name, cfg) {
				continue
			}
			c, err := d.newChannel(name, cfg, d.opts.SendTimeout)
			if err != nil {
				d.log.Warn("notification channel disabled", "channel", name, "err", err)
				continue
			}
			chans = append(chans, c)
		}
	}
	d.mu.Lock()
	d.cfg = cfg
	d.channels = chans
	d.mu.Unlock()
	names := make([]string, 0, len(chans))
	for _, c := range chans {
		names = append(names, c.Name())
	}
	d.log.Info("notifications configured", "enabled", cfg.Enabled, "channels", names)
}

func channelEnabled(name string, cfg config.NotificationsConfig) bool {
	switch name {
	case ChannelMeoW:
		return cfg.MeoW.Enabled
	case ChannelTelegram:
		return cfg.Telegram.Enabled
	}
	return false
}

// buildChannel 按名字构造渠道；缺配置返回 ErrChannelNotConfigured。
func buildChannel(name string, cfg config.NotificationsConfig, timeout time.Duration) (Channel, error) {
	switch name {
	case ChannelMeoW:
		return NewMeoW(cfg.MeoW, timeout)
	case ChannelTelegram:
		return NewTelegram(cfg.Telegram, timeout)
	}
	return nil, fmt.Errorf("unknown channel %q", name)
}

// Start 订阅总线并启动分发与发送 goroutine，直到 ctx 取消。
func (d *Dispatcher) Start(ctx context.Context) {
	ch, cancel := d.bus.Subscribe(256)
	go d.sender(ctx)
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				d.handle(e)
			}
		}
	}()
}

// handle 把事件翻译成消息；只做轻量工作，不阻塞总线。
func (d *Dispatcher) handle(e events.Event) {
	d.mu.Lock()
	cfg := d.cfg
	active := cfg.Enabled && len(d.channels) > 0
	d.mu.Unlock()
	if !active {
		return
	}
	switch e.Type {
	case "device.attached":
		if cfg.On.NotifyDeviceAttached() {
			d.enqueue(formatDeviceAttached(e, cfg.SiteURL))
		}
	case "backup.started":
		if cfg.On.NotifyBackupStarted() {
			d.enqueue(formatBackupStarted(e, cfg.SiteURL))
		}
	case "backup.finished":
		if cfg.On.NotifyBackupFinished() {
			d.enqueue(formatBackupFinished(e, cfg.SiteURL))
		}
	case "task.failed", "task.interrupted":
		if cfg.On.NotifyBackupFailed() {
			d.coalesceFailure(e, cfg.SiteURL)
		}
	}
}

// coalesceFailure 在窗口内合并失败事件，窗口到期发一条汇总。
func (d *Dispatcher) coalesceFailure(e events.Event, siteURL string) {
	d.failMu.Lock()
	defer d.failMu.Unlock()
	d.failCount++
	d.failLast = e.Message
	if d.failTimer != nil {
		return
	}
	d.failTimer = time.AfterFunc(d.opts.CoalesceWindow, func() {
		d.failMu.Lock()
		n, last := d.failCount, d.failLast
		d.failCount, d.failLast, d.failTimer = 0, "", nil
		d.failMu.Unlock()
		d.enqueue(formatFailures(n, last, siteURL))
	})
}

// Flush 立即触发尚未到期的失败合并（测试与停机用）。
func (d *Dispatcher) Flush() {
	d.failMu.Lock()
	t := d.failTimer
	d.failMu.Unlock()
	if t != nil && t.Stop() {
		d.mu.Lock()
		site := d.cfg.SiteURL
		d.mu.Unlock()
		d.failMu.Lock()
		n, last := d.failCount, d.failLast
		d.failCount, d.failLast, d.failTimer = 0, "", nil
		d.failMu.Unlock()
		if n > 0 {
			d.enqueue(formatFailures(n, last, site))
		}
	}
}

func (d *Dispatcher) enqueue(m Message) {
	select {
	case d.queue <- m:
	default:
		d.log.Warn("notification queue full, dropping", "title", m.Title)
	}
}

// sender 串行发送：每条消息发到所有已启用渠道，失败重试，最终失败记 notify.failed 事件。
func (d *Dispatcher) sender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-d.queue:
			d.mu.Lock()
			chans := d.channels
			d.mu.Unlock()
			for _, c := range chans {
				d.sendWithRetry(ctx, c, m)
				if !sleep(ctx, d.opts.MinInterval) {
					return
				}
			}
		}
	}
}

func (d *Dispatcher) sendWithRetry(ctx context.Context, c Channel, m Message) {
	var err error
	wait := d.opts.RetryBackoff
	for attempt := 0; attempt <= d.opts.Retries; attempt++ {
		if attempt > 0 {
			if !sleep(ctx, wait) {
				return
			}
			wait *= 2
		}
		sendCtx, cancel := context.WithTimeout(ctx, d.opts.SendTimeout)
		err = c.Send(sendCtx, m)
		cancel()
		if err == nil {
			d.log.Debug("notification sent", "channel", c.Name(), "title", m.Title, "attempt", attempt+1)
			if d.sent != nil {
				d.sent(c.Name(), m)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("notification send failed", "channel", c.Name(), "attempt", attempt+1, "err", err)
	}
	d.bus.Publish(context.WithoutCancel(ctx), events.Event{Level: "warn", Type: "notify.failed",
		Message: fmt.Sprintf("notification via %s failed after %d attempts: %s", c.Name(), d.opts.Retries+1, err),
		Data:    map[string]any{"channel": c.Name(), "title": m.Title, "error": err.Error()}})
}

func sleep(ctx context.Context, dur time.Duration) bool {
	if dur <= 0 {
		return true
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// TestResult 一次测试发送的结果。
type TestResult struct {
	Channel   string `json:"channel"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// Test 用当前配置向指定渠道立即发一条测试消息，不看总开关与渠道开关，只要求字段齐全。
func (d *Dispatcher) Test(ctx context.Context, channel string) TestResult {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	res := TestResult{Channel: channel}
	c, err := d.newChannel(channel, cfg, d.opts.SendTimeout)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	start := time.Now()
	sendCtx, cancel := context.WithTimeout(ctx, d.opts.SendTimeout)
	defer cancel()
	err = c.Send(sendCtx, formatTest(cfg.SiteURL))
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}
