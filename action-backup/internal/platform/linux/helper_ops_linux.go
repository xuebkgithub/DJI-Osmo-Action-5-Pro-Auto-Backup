//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// RealOps 真实挂载操作：源卷走 mount(2)，CIFS 走 mount.cifs（它负责凭据文件与协议协商）。
type RealOps struct{}

// NewRealOps 返回真实实现。
func NewRealOps() (helperOps, error) { return RealOps{}, nil }

// MountSource 只读挂载，禁 suid / dev / exec；文件属主映射为服务用户，其它人不可读。
func (RealOps) MountSource(_ context.Context, p sourcePlan, uid, gid int) error {
	data := fmt.Sprintf("uid=%d,gid=%d,umask=0077,iocharset=utf8", uid, gid)
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	return unix.Mount(p.Source, p.Target, p.FSType, flags, data)
}

// MountTarget 调 mount.cifs。vers 为空时由内核协商（2.1 至 3.1.1，SMB1 已在规则层拒绝）。
func (RealOps) MountTarget(ctx context.Context, p targetPlan, uid, gid int) error {
	opts := []string{
		"credentials=" + p.CredentialsFile,
		fmt.Sprintf("uid=%d,gid=%d", uid, gid),
		"file_mode=0660,dir_mode=0770",
		"nosuid,nodev,noexec",
		"iocharset=utf8",
	}
	if p.Vers != "" {
		opts = append(opts, "vers="+p.Vers)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mount", "-t", "cifs", p.Share, p.MountPoint, "-o", strings.Join(opts, ","))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Unmount 先常规卸载，忙则延迟卸载（设备已拔出的残留挂载只能这样清）。
func (RealOps) Unmount(target string) error {
	err := unix.Unmount(target, 0)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EBUSY) || errors.Is(err, unix.EIO) || errors.Is(err, unix.ENXIO) {
		return unix.Unmount(target, unix.MNT_DETACH)
	}
	return err
}

// IsMounted 查当前挂载表。
func (RealOps) IsMounted(target string) (string, bool) {
	src, _, ok := mountEntry(target)
	return src, ok
}

// Healthy statfs 成功即认为挂载可用。
func (RealOps) Healthy(target string) bool {
	var st unix.Statfs_t
	return unix.Statfs(target, &st) == nil
}
