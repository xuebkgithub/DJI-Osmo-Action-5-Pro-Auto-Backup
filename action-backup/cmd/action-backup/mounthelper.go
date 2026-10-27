package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"action-backup/internal/platform/linux"
	"action-backup/internal/version"
)

// runMountHelper 实现 `action-backup mount-helper`：以 root 运行的挂载守护，供 action-backup-mounter.service 使用。
// 只按配置文件与白名单规则挂载相机卷（只读）和 CIFS 目标，客户端不能指定路径。
func runMountHelper(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("action-backup mount-helper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/action-backup/config.yaml", "配置文件路径")
	socket := fs.String("socket", linux.DefaultHelperSocket, "监听的 unix socket")
	owner := fs.String("owner", "actionbackup", "挂载后文件的属主用户（服务运行用户）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("mount-helper must run as root (euid %d)", os.Geteuid())
	}
	u, err := user.Lookup(*owner)
	if err != nil {
		return fmt.Errorf("lookup owner %q: %w", *owner, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	ops, err := linux.NewRealOps()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("mount-helper starting", "version", version.Version, "config", *cfgPath, "socket", *socket, "owner", *owner)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &linux.Server{ConfigPath: *cfgPath, Socket: *socket, OwnerUID: uid, OwnerGID: gid, Ops: ops, Log: log}
	return srv.Serve(ctx)
}
