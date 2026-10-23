package platform

import (
	"context"
	"fmt"

	"action-backup/internal/domain"
)

// NotImplementedDetector 占位实现，真实 udev / diskutil 后端在后续版本提供。
type NotImplementedDetector struct{ OS string }

// Discover 返回未实现。
func (n NotImplementedDetector) Discover(context.Context) ([]RawDevice, error) {
	return nil, fmt.Errorf("%s device detector: %w", n.OS, domain.ErrNotImplemented)
}

// Watch 返回未实现。
func (n NotImplementedDetector) Watch(context.Context) (<-chan DeviceEvent, error) {
	return nil, fmt.Errorf("%s device watcher: %w", n.OS, domain.ErrNotImplemented)
}

// NotImplementedMounter 占位实现。
type NotImplementedMounter struct{ OS string }

// MountReadOnly 返回未实现。
func (n NotImplementedMounter) MountReadOnly(context.Context, RawVolume) (string, error) {
	return "", fmt.Errorf("%s mounter: %w", n.OS, domain.ErrNotImplemented)
}

// Unmount 返回未实现。
func (n NotImplementedMounter) Unmount(context.Context, RawVolume) error {
	return fmt.Errorf("%s unmount: %w", n.OS, domain.ErrNotImplemented)
}
