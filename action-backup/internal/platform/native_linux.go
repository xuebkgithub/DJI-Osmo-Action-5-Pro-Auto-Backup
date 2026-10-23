//go:build linux

package platform

import (
	"log/slog"

	"action-backup/internal/config"
	"action-backup/internal/platform/linux"
)

func nativeDetector(*config.Config, *slog.Logger) (DeviceDetector, error) {
	return NotImplementedDetector{OS: "linux"}, nil
}

func nativeMounter() VolumeMounter { return NotImplementedMounter{OS: "linux"} }
func nativeProbe() StorageProbe    { return linux.Probe{} }
