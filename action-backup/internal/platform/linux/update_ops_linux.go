//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// UpdateOps 自更新安装流程（update.Installer）在 Linux 上的系统操作，以 root 运行。
type UpdateOps struct{}

// NewUpdateOps 返回真实实现。
func NewUpdateOps() (*UpdateOps, error) { return &UpdateOps{}, nil }

// CopyNoFollow 从不可信目录复制一个文件：O_NOFOLLOW 拒绝符号链接，fstat 确认是普通文件且不超过 limit。
// O_NONBLOCK 防止对方放一个 FIFO 把进程挂住。
func (*UpdateOps) CopyNoFollow(src, dst string, limit int64) error {
	fd, err := unix.Open(src, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	in := os.NewFile(uintptr(fd), src)
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() > limit {
		return fmt.Errorf("%s is %d bytes, limit %d", src, info.Size(), limit)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("%s grew beyond limit %d while copying", src, limit)
	}
	if err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func run(ctx context.Context, timeout time.Duration, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Systemctl 执行 systemctl。
func (*UpdateOps) Systemctl(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("systemctl: no arguments")
	}
	_, err := run(ctx, 2*time.Minute, "", "systemctl", args...)
	return err
}

// IsActive 单元是否 active。
func (*UpdateOps) IsActive(ctx context.Context, unit string) bool {
	_, err := run(ctx, 15*time.Second, "", "systemctl", "is-active", "--quiet", unit)
	return err == nil
}

// RunInstaller 在解包目录里执行 install.sh。
func (*UpdateOps) RunInstaller(ctx context.Context, dir string) (string, error) {
	return run(ctx, 5*time.Minute, dir, "bash", "install.sh")
}

// BinaryVersion 执行 <path> --version。
func (*UpdateOps) BinaryVersion(ctx context.Context, path string) (string, error) {
	return run(ctx, 15*time.Second, "", path, "--version")
}
