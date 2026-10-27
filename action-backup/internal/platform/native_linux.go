//go:build linux

package platform

import (
	"log/slog"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/platform/linux"
)

func nativeDetector(cfg *config.Config, log *slog.Logger) (DeviceDetector, error) {
	interval := 2 * time.Second
	if cfg.Simulation != nil && cfg.Simulation.Volumes.PollIntervalSec > 0 {
		interval = time.Duration(cfg.Simulation.Volumes.PollIntervalSec) * time.Second
	}
	return linux.NewDetector(cfg.Device.VendorID, cfg.Device.ProductID, cfg.Device.MountRoot, interval, log), nil
}

func nativeMounter() VolumeMounter         { return linux.Mounter{Client: linux.NewClient()} }
func nativeProbe() StorageProbe            { return linux.Probe{} }
func nativeStorageMounter() StorageMounter { return linux.StorageMounter{Client: linux.NewClient()} }
