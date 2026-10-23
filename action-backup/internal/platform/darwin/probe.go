//go:build darwin

// Package darwin 提供 macOS 平台实现。
package darwin

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"action-backup/internal/platform/fsinfo"
)

// Probe 用 statfs 确认路径的文件系统类型与来源。
type Probe struct{}

// Probe 实现 platform.StorageProbe。
func (Probe) Probe(_ context.Context, path string, expect fsinfo.Expectation) (fsinfo.Info, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fsinfo.Info{Path: path}, fmt.Errorf("statfs %s: %w", path, err)
	}
	info := fsinfo.Info{
		Path:      path,
		FSType:    unix.ByteSliceToString(st.Fstypename[:]),
		Source:    unix.ByteSliceToString(st.Mntfromname[:]),
		FreeBytes: st.Bavail * uint64(st.Bsize),
	}
	mounted := unix.ByteSliceToString(st.Mntonname[:])
	info.Matched = fsinfo.Matches(expect, info.FSType, info.Source, mounted == path)
	if !info.Matched {
		return info, fmt.Errorf("%s is %s mounted from %q, expected %s %q", path, info.FSType, info.Source, expect.Kind, expect.Share)
	}
	return info, nil
}
