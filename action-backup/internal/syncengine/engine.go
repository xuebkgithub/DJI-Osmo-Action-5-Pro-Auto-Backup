package syncengine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/platform"
	"action-backup/internal/scanner"
	"action-backup/internal/storage"
)

// DeviceSource 当前已插入设备。
type DeviceSource interface {
	Attached() []domain.Device
}

// Engine 扫描 + 对比 + 计划。
type Engine struct {
	cfg     config.BackupConfig
	mode    string
	scanner *scanner.Scanner
	mounter platform.VolumeMounter
	devices DeviceSource
	devRepo repository.Devices
	files   repository.SourceFiles
	tasks   repository.Tasks
	target  *storage.Target
	bus     *events.Broadcaster
	log     *slog.Logger

	mu      sync.Mutex
	running bool
	last    *domain.Plan
}

// New 创建引擎。
func New(cfg config.BackupConfig, mode string, mounter platform.VolumeMounter, devices DeviceSource,
	devRepo repository.Devices, files repository.SourceFiles, tasks repository.Tasks,
	target *storage.Target, bus *events.Broadcaster, log *slog.Logger) *Engine {
	return &Engine{
		cfg: cfg, mode: mode,
		scanner: scanner.New(scanner.Options{SourceDirectory: cfg.SourceDirectory, Extensions: cfg.FileExtensions, IgnorePatterns: cfg.IgnorePatterns}),
		mounter: mounter, devices: devices, devRepo: devRepo, files: files, tasks: tasks, target: target, bus: bus, log: log,
	}
}

// ErrPlanRunning 已有计划在生成中。
var ErrPlanRunning = fmt.Errorf("a plan is already being generated")

// Last 最近一次计划；没有返回 nil。
func (e *Engine) Last() *domain.Plan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// Plan 扫描所有已插入设备并生成计划。不写数据库，不写目标。
func (e *Engine) Plan(ctx context.Context) (*domain.Plan, error) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil, ErrPlanRunning
	}
	e.running = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
	}()

	start := time.Now()
	plan := &domain.Plan{GeneratedAt: start.UTC(), Mode: e.mode, Items: []domain.PlanItem{}, Warnings: []string{}}

	targetChecked := true
	if _, err := e.target.Probe(ctx); err != nil {
		targetChecked = false
		plan.Warnings = append(plan.Warnings, "target storage unavailable, skip decisions not verified: "+err.Error())
	}
	plan.TargetChecked = targetChecked
	cmp := Comparator{Files: e.files, Tasks: e.tasks, Target: e.target, TargetChecked: targetChecked}
	layout := e.target.Layout()

	devices := e.devices.Attached()
	plan.Devices = len(devices)
	for _, dev := range devices {
		for _, vol := range dev.Volumes {
			if !e.included(vol.Type) {
				continue
			}
			plan.Volumes++
			mountPath, err := e.mounter.MountReadOnly(ctx, platform.RawVolume{Key: vol.Key, LUN: vol.LUN, Label: vol.Label, Filesystem: vol.Filesystem, SizeBytes: vol.SizeBytes, MountPath: vol.MountPath})
			if err != nil {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("volume %s (%s): mount failed: %v", vol.Label, vol.Type, err))
				continue
			}
			volumeID := vol.ID
			if volumeID == 0 {
				volumeID, _ = e.devRepo.VolumeID(ctx, dev.Key, vol.Key)
			}
			res, err := e.scanner.Scan(ctx, dev, vol, mountPath)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("volume %s (%s): scan failed: %v", vol.Label, vol.Type, err))
				continue
			}
			if !res.SourceExists {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("volume %s (%s): no %s directory", vol.Label, vol.Type, e.cfg.SourceDirectory))
			}
			plan.Scanned += len(res.Files)
			for _, f := range res.Files {
				item := domain.PlanItem{VolumeType: vol.Type, RelativePath: f.RelativePath, Size: f.Size, File: f, VolumeID: volumeID}
				dest, err := layout.Render(layoutVars(dev, vol, f))
				if err != nil {
					item.Action, item.Reason = domain.PlanConflict, ReasonDestinationRenderErr+": "+err.Error()
					plan.Add(item)
					continue
				}
				item.DestinationPath = dest
				action, reason, err := cmp.Decide(ctx, volumeID, f, dest)
				if err != nil {
					return nil, err
				}
				item.Action, item.Reason = action, reason
				plan.Add(item)
			}
		}
	}
	plan.DurationMs = time.Since(start).Milliseconds()

	e.mu.Lock()
	e.last = plan
	e.mu.Unlock()
	e.bus.Publish(ctx, events.Event{Type: "plan.generated",
		Message: fmt.Sprintf("plan: %d upload (%d bytes), %d skip, %d conflict across %d volume(s)", plan.Upload.Files, plan.Upload.Bytes, plan.Skip.Files, plan.Conflict.Files, plan.Volumes),
		Data: map[string]any{
			"upload_files": plan.Upload.Files, "upload_bytes": plan.Upload.Bytes,
			"skip_files": plan.Skip.Files, "conflict_files": plan.Conflict.Files,
			"target_checked": plan.TargetChecked, "warnings": len(plan.Warnings), "duration_ms": plan.DurationMs,
		}})
	return plan, nil
}

func (e *Engine) included(t domain.VolumeType) bool {
	switch t {
	case domain.VolumeInternal:
		return e.cfg.IncludesInternal()
	case domain.VolumeSDCard:
		return e.cfg.IncludesSDCard()
	}
	return false
}

func layoutVars(dev domain.Device, vol domain.Volume, f domain.SourceFile) storageVars {
	return storageVars{
		RelativePath: f.RelativePath, VolumeType: string(vol.Type), VolumeLabel: vol.Label,
		Serial: dev.SerialNumber, DeviceName: dev.Name, MTime: f.ModTime,
	}
}
