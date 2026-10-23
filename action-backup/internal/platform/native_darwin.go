//go:build darwin

package platform

import (
	"log/slog"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/platform/darwin"
)

func nativeDetector(cfg *config.Config, log *slog.Logger) (DeviceDetector, error) {
	interval := 2 * time.Second
	if cfg.Simulation != nil && cfg.Simulation.Volumes.PollIntervalSec > 0 {
		interval = time.Duration(cfg.Simulation.Volumes.PollIntervalSec) * time.Second
	}
	return darwin.NewDetector(cfg.Device.VendorID, cfg.Device.ProductID, interval, nil, log)
}

func nativeMounter() VolumeMounter { return darwin.Mounter{} }
func nativeProbe() StorageProbe    { return darwin.Probe{} }
