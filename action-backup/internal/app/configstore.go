package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/faultinject"
	"action-backup/internal/scheduler"
)

// configStore 实现 api.ConfigStore：白名单修改先校验、再写文件、最后应用到运行中的组件。
type configStore App

func (c *configStore) app() *App { return (*App)(c) }

// Current 返回脱敏副本。
func (c *configStore) Current() config.Config {
	a := c.app()
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg.Redacted()
}

// Path 配置文件路径；由字节解析（测试）时为空。
func (c *configStore) Path() string {
	a := c.app()
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg.Path
}

// Patch 应用白名单修改。
func (c *configStore) Patch(ctx context.Context, p config.Patch) (config.Config, error) {
	a := c.app()
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	next, err := a.cfg.Apply(p)
	if err != nil {
		return config.Config{}, err
	}
	if err := next.Validate(faultinject.Enabled); err != nil {
		return config.Config{}, &config.ValidationError{Err: err}
	}
	if a.cfg.Path != "" {
		if err := config.SetValues(a.cfg.Path, map[string]any(p)); err != nil {
			return config.Config{}, fmt.Errorf("write config: %w", err)
		}
	}
	a.applyRuntime(next, p)
	*a.cfg = *next
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	a.bus.Publish(ctx, eventConfigChanged(keys, p))
	return a.cfg.Redacted(), nil
}

// applyRuntime 把修改同步到已装配的组件。
func (a *App) applyRuntime(next *config.Config, p config.Patch) {
	for k := range p {
		switch k {
		case "device.auto_backup":
			a.autoBackup.Store(next.Device.AutoBackup)
		case "backup.verify_mode":
			a.service.SetVerifyMode(domain.VerifyMode(next.Backup.VerifyMode))
		case "backup.on_conflict":
			a.service.SetConflictPolicy(next.Backup.OnConflict)
		case "backup.retry_count", "backup.reconcile_interval_sec":
			a.sched.Configure(scheduler.Options{MaxRetries: next.Backup.RetryCount, ReconcileInterval: time.Duration(next.Backup.ReconcileIntervalSec) * time.Second}, nil)
		case "logging.level":
			if a.logLevel != nil {
				a.logLevel.Set(parseLevel(next.Logging.Level))
			}
		}
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func eventConfigChanged(keys []string, p config.Patch) events.Event {
	return events.Event{Type: "config.changed", Message: "configuration changed: " + strings.Join(keys, ", "), Data: map[string]any{"changes": map[string]any(p)}}
}
