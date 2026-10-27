package linux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"action-backup/internal/config"
)

// helperOps 真正执行挂载的操作，Linux 上为 unix.Mount / mount.cifs；测试用假实现。
type helperOps interface {
	MountSource(ctx context.Context, p sourcePlan, uid, gid int) error
	MountTarget(ctx context.Context, p targetPlan, uid, gid int) error
	Unmount(target string) error
	// IsMounted 返回挂载来源；未挂载时 ok 为 false。
	IsMounted(target string) (source string, ok bool)
	// Healthy 对已挂载的目录做 statfs，设备拔出后的残留挂载会失败。
	Healthy(target string) bool
}

// Server 挂载守护：以 root 运行，监听 unix socket，只按配置文件和白名单规则挂载。
type Server struct {
	ConfigPath string
	Socket     string
	// Owner 挂载后文件的属主（服务用户 actionbackup 的 uid / gid）。
	OwnerUID, OwnerGID int
	// SocketGID socket 的组，默认与 OwnerGID 相同。
	SocketGID int
	Sysfs     Sysfs
	Ops       helperOps
	Log       *slog.Logger

	mu sync.Mutex // 串行化挂载操作
}

func (s *Server) logger() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Serve 监听直到 ctx 取消。
func (s *Server) Serve(ctx context.Context) error {
	if s.Sysfs.Root == "" {
		s.Sysfs = DefaultSysfsTree()
	}
	if s.Ops == nil {
		return errors.New("mount helper: no ops")
	}
	if _, err := s.loadConfig(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0o750); err != nil {
		return err
	}
	_ = os.Remove(s.Socket)
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.Socket, err)
	}
	defer ln.Close()
	gid := s.SocketGID
	if gid == 0 {
		gid = s.OwnerGID
	}
	if err := os.Chown(s.Socket, 0, gid); err != nil && !errors.Is(err, os.ErrPermission) {
		s.logger().Warn("chown socket failed", "err", err)
	}
	if err := os.Chmod(s.Socket, 0o660); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(s.Socket), 0o750); err == nil {
		_ = os.Chown(filepath.Dir(s.Socket), 0, gid)
	}
	s.logger().Info("mount helper listening", "socket", s.Socket, "owner_uid", s.OwnerUID, "owner_gid", s.OwnerGID, "config", s.ConfigPath)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) loadConfig() (*config.Config, error) {
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("mount helper: %w", err)
	}
	return cfg, nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	var req HelperRequest
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(HelperResponse{Error: "bad request: " + err.Error()})
		return
	}
	resp := s.Handle(ctx, req)
	_ = json.NewEncoder(conn).Encode(resp)
}

// Handle 处理一个请求；可直接调用，便于测试。
func (s *Server) Handle(ctx context.Context, req HelperRequest) HelperResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.dispatch(ctx, req)
	if err != nil {
		s.logger().Warn("mount helper request failed", "op", req.Op, "device", req.Device, "err", err)
		return HelperResponse{Error: err.Error()}
	}
	return HelperResponse{OK: true, Path: path}
}

func (s *Server) dispatch(ctx context.Context, req HelperRequest) (string, error) {
	if req.Op == OpPing {
		return "", nil
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return "", err
	}
	switch req.Op {
	case OpMountSource:
		p, err := planSource(s.Sysfs, cfg, req.Device)
		if err != nil {
			return "", err
		}
		if src, ok := s.Ops.IsMounted(p.Target); ok {
			if src == p.Source && s.Ops.Healthy(p.Target) {
				return p.Target, nil
			}
			s.logger().Info("replacing stale mount", "target", p.Target, "old_source", src)
			if err := s.Ops.Unmount(p.Target); err != nil {
				return "", fmt.Errorf("unmount stale %s: %w", p.Target, err)
			}
		}
		if err := os.MkdirAll(p.Target, 0o755); err != nil {
			return "", err
		}
		if err := s.Ops.MountSource(ctx, p, s.OwnerUID, s.OwnerGID); err != nil {
			return "", fmt.Errorf("mount %s on %s (%s, ro): %w", p.Source, p.Target, p.FSType, err)
		}
		s.logger().Info("source mounted read-only", "source", p.Source, "target", p.Target, "fstype", p.FSType)
		return p.Target, nil
	case OpUnmountSource:
		target, err := sourceTarget(cfg, req.Device)
		if err != nil {
			return "", err
		}
		if _, ok := s.Ops.IsMounted(target); ok {
			if err := s.Ops.Unmount(target); err != nil {
				return "", fmt.Errorf("unmount %s: %w", target, err)
			}
			s.logger().Info("source unmounted", "target", target)
		}
		_ = os.Remove(target)
		return target, nil
	case OpMountTarget:
		p, err := planTarget(cfg)
		if err != nil {
			return "", err
		}
		if src, ok := s.Ops.IsMounted(p.MountPoint); ok {
			if s.Ops.Healthy(p.MountPoint) {
				return p.MountPoint, nil
			}
			s.logger().Info("replacing stale target mount", "mount_point", p.MountPoint, "old_source", src)
			if err := s.Ops.Unmount(p.MountPoint); err != nil {
				return "", fmt.Errorf("unmount stale %s: %w", p.MountPoint, err)
			}
		}
		if err := os.MkdirAll(p.MountPoint, 0o755); err != nil {
			return "", err
		}
		if err := s.Ops.MountTarget(ctx, p, s.OwnerUID, s.OwnerGID); err != nil {
			return "", fmt.Errorf("mount cifs %s on %s: %w", p.Share, p.MountPoint, err)
		}
		s.logger().Info("target mounted", "share", p.Share, "mount_point", p.MountPoint, "vers", p.Vers)
		return p.MountPoint, nil
	case OpUnmountTarget:
		p, err := planTarget(cfg)
		if err != nil {
			return "", err
		}
		if _, ok := s.Ops.IsMounted(p.MountPoint); ok {
			if err := s.Ops.Unmount(p.MountPoint); err != nil {
				return "", fmt.Errorf("unmount %s: %w", p.MountPoint, err)
			}
			s.logger().Info("target unmounted", "mount_point", p.MountPoint)
		}
		return p.MountPoint, nil
	}
	return "", fmt.Errorf("unknown op %q", req.Op)
}
