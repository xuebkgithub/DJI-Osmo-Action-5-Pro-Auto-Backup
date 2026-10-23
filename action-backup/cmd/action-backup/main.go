// Command action-backup 是 DJI Osmo Action 5 Pro 自动备份服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"action-backup/internal/app"
	"action-backup/internal/config"
	"action-backup/internal/faultinject"
	"action-backup/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("action-backup", flag.ContinueOnError)
	cfgPath := fs.String("config", "/etc/action-backup/config.yaml", "配置文件路径")
	showVersion := fs.Bool("version", false, "打印版本并退出")
	checkOnly := fs.Bool("check", false, "只校验配置并退出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Printf("action-backup %s (commit %s, built %s, faultinject=%v)\n", version.Version, version.Commit, version.BuildTime, faultinject.Enabled)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(faultinject.Enabled); err != nil {
		return fmt.Errorf("invalid config %s:\n%w", cfg.Path, err)
	}
	if *checkOnly {
		fmt.Printf("config ok: %s (mode %s)\n", cfg.Path, cfg.Mode)
		return nil
	}

	log := newLogger(cfg.Logging)
	slog.SetDefault(log)
	log.Info("action-backup starting", "version", version.Version, "config", cfg.Path, "mode", cfg.Mode, "faultinject", faultinject.Enabled)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("action-backup stopped")
	return nil
}

func newLogger(c config.LoggingConfig) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(c.Level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if strings.ToLower(c.Format) == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
