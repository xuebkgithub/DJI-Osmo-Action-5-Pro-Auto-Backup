package platform

import (
	"fmt"
	"log/slog"
	"runtime"

	"action-backup/internal/config"
	"action-backup/internal/platform/sim"
	"action-backup/internal/runmode"
)

// Select 按 GOOS 与运行模式选择后端。返回的 sim.Backend 仅在 simulation 模式非空，供测试接口控制。
func Select(cfg *config.Config, mode runmode.Mode, log *slog.Logger) (*Backend, *sim.Backend, error) {
	if log == nil {
		log = slog.Default()
	}
	switch mode {
	case runmode.Simulation:
		if cfg.Simulation == nil {
			return nil, nil, fmt.Errorf("simulation mode requires simulation config")
		}
		s, err := sim.New(cfg.Simulation.Fixtures, cfg.Device)
		if err != nil {
			return nil, nil, err
		}
		b := &Backend{Name: "sim", Detector: s, Mounter: s}
		switch cfg.Simulation.DeviceSource {
		case "volumes", "both":
			nd, err := nativeDetector(cfg, log)
			if err != nil {
				return nil, nil, err
			}
			if cfg.Simulation.DeviceSource == "both" {
				b.Detector = MultiDetector{s, nd}
				b.Name = "sim+" + runtime.GOOS
			} else {
				b.Detector = nd
				b.Name = "sim/" + runtime.GOOS
			}
			b.Mounter = MultiMounter{Fallback: nativeMounter()}
		}
		if cfg.Storage.Type == config.StorageLocal {
			b.Probe = sim.LocalProbe{}
		} else {
			b.Probe = nativeProbe()
		}
		return b, s, nil
	case runmode.DryRun, runmode.Production:
		if runtime.GOOS == "darwin" && mode == runmode.Production {
			return nil, nil, fmt.Errorf("production mode is not supported on darwin; use dry-run or simulation")
		}
		nd, err := nativeDetector(cfg, log)
		if err != nil {
			return nil, nil, err
		}
		b := &Backend{
			Name:     runtime.GOOS,
			Detector: nd,
			Mounter:  MultiMounter{Fallback: nativeMounter()},
			Probe:    nativeProbe(),
		}
		if cfg.Storage.Type == config.StorageCIFS {
			b.Storage = nativeStorageMounter()
		}
		return b, nil, nil
	}
	return nil, nil, fmt.Errorf("unknown mode %q", mode)
}
