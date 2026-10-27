//go:build !darwin && !linux

package platform

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"action-backup/internal/config"
	"action-backup/internal/domain"
)

type unsupportedProbe struct{}

func (unsupportedProbe) Probe(context.Context, string, MountExpectation) (MountInfo, error) {
	return MountInfo{}, fmt.Errorf("%s storage probe: %w", runtime.GOOS, domain.ErrNotImplemented)
}

func nativeDetector(*config.Config, *slog.Logger) (DeviceDetector, error) {
	return NotImplementedDetector{OS: runtime.GOOS}, nil
}

func nativeMounter() VolumeMounter { return NotImplementedMounter{OS: runtime.GOOS} }
func nativeProbe() StorageProbe    { return unsupportedProbe{} }

// nativeStorageMounter 本平台没有目标挂载器。
func nativeStorageMounter() StorageMounter { return nil }
