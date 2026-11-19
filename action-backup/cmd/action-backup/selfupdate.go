package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"action-backup/internal/config"
	"action-backup/internal/platform/linux"
	"action-backup/internal/update"
	"action-backup/internal/version"
)

// 设备上的固定安装位置，与 deployments/install.sh 一致。
const (
	installDir      = "/opt/action-backup"
	systemdUnitDir  = "/etc/systemd/system"
	udevRulesDir    = "/etc/udev/rules.d"
	mainUnit        = "action-backup.service"
	mounterUnit     = "action-backup-mounter.service"
	updateUnit      = "action-backup-update.service"
	updateTimerUnit = "action-backup-update.timer"
)

// runSelfUpdate 实现 `action-backup self-update --apply`：由 action-backup-update.timer 每分钟以 root 拉起。
// 没有安装请求时立即退出；有请求时验签、执行包内 install.sh、重启服务、健康检查，失败回滚。
// 本命令不联网，也不接受版本号或下载地址：装什么完全由验过签名的 SHA256SUMS 决定。
func runSelfUpdate(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("action-backup self-update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/action-backup/config.yaml", "配置文件路径")
	apply := fs.Bool("apply", false, "处理待安装的更新请求")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*apply {
		return fmt.Errorf("usage: action-backup self-update --apply [--config <file>]")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("self-update must run as root (euid %d)", os.Geteuid())
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ops, err := linux.NewUpdateOps()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db := cfg.Database.Path
	in := &update.Installer{
		Paths: update.InstallPaths{
			UpdateDir:  update.DirFor(db),
			WorkDir:    installDir + "/update-work",
			PrevDir:    installDir + "/prev",
			ResultPath: update.ResultPath(installDir),
			Managed: []string{
				installDir + "/action-backup",
				systemdUnitDir + "/" + mainUnit, systemdUnitDir + "/" + mounterUnit,
				systemdUnitDir + "/" + updateUnit, systemdUnitDir + "/" + updateTimerUnit,
				udevRulesDir + "/99-action-backup.rules",
				db, db + "-wal", db + "-shm",
			},
		},
		Ops: ops, Current: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		BinaryPath: installDir + "/action-backup",
		MainUnit:   mainUnit, Units: []string{mounterUnit, mainUnit},
		HealthURL: healthURL(cfg.Server.Listen), Log: log,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := in.Run(ctx)
	if res == nil && err == nil {
		return nil // 没有请求
	}
	if res != nil {
		log.Info("self-update finished", "ok", res.OK, "from", res.From, "to", res.To, "step", res.Step,
			"error", res.Error, "rolled_back", res.RolledBack, "rollback_error", res.RollbackError)
	}
	return err
}

// healthURL 由监听地址推出本机健康检查地址；监听全部地址时走回环。
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:8080/api/v1/system/health"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/system/health"
}
