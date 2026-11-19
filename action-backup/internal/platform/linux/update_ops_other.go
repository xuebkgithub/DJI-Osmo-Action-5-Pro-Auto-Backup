//go:build !linux

package linux

import (
	"context"
	"errors"
)

var errUpdateOpsUnsupported = errors.New("self-update install only runs on linux")

// UpdateOps 非 Linux 上的占位：自更新安装只在设备上运行。
type UpdateOps struct{}

// NewUpdateOps 非 Linux 上返回错误。
func NewUpdateOps() (*UpdateOps, error) { return nil, errUpdateOpsUnsupported }

// CopyNoFollow 未实现。
func (*UpdateOps) CopyNoFollow(string, string, int64) error { return errUpdateOpsUnsupported }

// Systemctl 未实现。
func (*UpdateOps) Systemctl(context.Context, ...string) error { return errUpdateOpsUnsupported }

// IsActive 未实现。
func (*UpdateOps) IsActive(context.Context, string) bool { return false }

// RunInstaller 未实现。
func (*UpdateOps) RunInstaller(context.Context, string) (string, error) {
	return "", errUpdateOpsUnsupported
}

// BinaryVersion 未实现。
func (*UpdateOps) BinaryVersion(context.Context, string) (string, error) {
	return "", errUpdateOpsUnsupported
}
