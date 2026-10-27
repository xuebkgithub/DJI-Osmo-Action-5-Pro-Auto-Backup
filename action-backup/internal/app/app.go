// Package app 负责依赖装配与生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"action-backup/internal/api"
	"action-backup/internal/api/auth"
	"action-backup/internal/backup"
	"action-backup/internal/config"
	"action-backup/internal/database"
	"action-backup/internal/database/repository"
	"action-backup/internal/device"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/faultinject"
	"action-backup/internal/notify"
	"action-backup/internal/platform"
	"action-backup/internal/platform/sim"
	"action-backup/internal/runmode"
	"action-backup/internal/scheduler"
	"action-backup/internal/storage"
	"action-backup/internal/syncengine"
	"action-backup/internal/version"
	"action-backup/web"
)

// App 已装配的应用。
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	guard   *runmode.Guard
	backend *platform.Backend
	sim     *sim.Backend
	db      *database.DB
	bus     *events.Broadcaster
	target  *storage.Target
	devices *device.Manager
	engine  *syncengine.Engine
	sched   *scheduler.Scheduler
	service *backup.Service
	auth    *auth.Manager
	notify  *notify.Dispatcher
	handler http.Handler
	started time.Time

	autoBackup atomic.Bool
	logLevel   *slog.LevelVar

	cfgMu sync.Mutex // 保护 cfg 的运行时修改

	addrMu   sync.Mutex
	listener net.Listener
}

// Option 装配选项。
type Option func(*App)

// WithLogLevel 让 PATCH /config 的 logging.level 实时生效。
func WithLogLevel(v *slog.LevelVar) Option { return func(a *App) { a.logLevel = v } }

// New 装配应用。配置必须已通过 Validate。
func New(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	mode, err := runmode.Parse(cfg.Mode)
	if err != nil {
		return nil, err
	}
	guard := runmode.NewGuard(mode, faultinject.Enabled)

	backend, simBackend, err := platform.Select(cfg, mode, log)
	if err != nil {
		return nil, fmt.Errorf("select platform backend: %w", err)
	}

	db, err := database.Open(ctx, cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	applied, err := db.Migrate(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	log.Info("database ready", "path", cfg.Database.Path, "migrations_applied", applied)

	bus := events.New(repository.Events{DB: db.DB}, log)

	target, err := storage.New(cfg.Storage, guard, backend.Probe)
	if err != nil {
		db.Close()
		return nil, err
	}

	devices := device.NewManager(backend.Detector, repository.Devices{DB: db.DB}, bus, log)
	engine := syncengine.New(cfg.Backup, string(mode), backend.Mounter, devices,
		repository.Devices{DB: db.DB}, repository.SourceFiles{DB: db.DB}, repository.Tasks{DB: db.DB}, target, bus, log)

	var injector faultinject.Injector = faultinject.Noop{}
	var faults faultinject.Controller
	if cfg.Faults != nil && cfg.Faults.Enabled {
		ctrl, err := faultinject.New(cfg.Faults.Rules)
		if err != nil {
			db.Close()
			return nil, err
		}
		if ctrl != nil {
			faults, injector = ctrl, ctrl
			ctrl.SetOnHit(func(h faultinject.HitRecord) {
				tid := h.Meta.TaskID
				var tp *int64
				if tid > 0 {
					tp = &tid
				}
				bus.Publish(context.Background(), events.Event{Level: "warn", TaskID: tp, Type: "fault.hit",
					Message: fmt.Sprintf("fault %s hit at %s (%s)", h.RuleID, h.Point, h.Action),
					Data:    map[string]any{"rule": h.RuleID, "point": string(h.Point), "action": h.Action, "bytes": h.Meta.Bytes, "path": h.Meta.RelativePath}})
			})
			if simBackend != nil {
				ctrl.SetDisconnect(func(context.Context) error {
					for _, k := range simBackend.Keys() {
						_ = simBackend.Detach(k)
					}
					return nil
				})
			}
			log.Warn("fault injection enabled", "rules", len(cfg.Faults.Rules))
		}
	}
	target.SetInjector(injector)
	worker := backup.New(backup.Options{
		BufferSize:         cfg.Backup.BufferSizeMB << 20,
		CheckpointInterval: time.Duration(cfg.Backup.CheckpointIntervalSec) * time.Second,
		CheckpointBytes:    int64(cfg.Backup.CheckpointBytesMB) << 20,
	}, backup.Deps{Target: target, Tasks: repository.Tasks{DB: db.DB}, Checkpoints: repository.Checkpoints{DB: db.DB}, Inject: injector, Log: log})
	sched := scheduler.New(repository.Tasks{DB: db.DB}, worker, devices, backend.Mounter, bus, log)
	sched.Configure(scheduler.Options{MaxRetries: cfg.Backup.RetryCount, ReconcileInterval: time.Duration(cfg.Backup.ReconcileIntervalSec) * time.Second}, target)
	service := backup.NewService(engine, repository.SourceFiles{DB: db.DB}, repository.Tasks{DB: db.DB}, sched, guard, domain.VerifyMode(cfg.Backup.VerifyMode), bus, log)
	service.SetConflictPolicy(cfg.Backup.OnConflict)

	authMgr := auth.New(auth.Config{
		Enabled: cfg.Server.Auth.Enabled, Username: cfg.Server.Auth.Username, PasswordHash: cfg.Server.Auth.PasswordHash,
		SessionTTL: time.Duration(cfg.Server.Auth.SessionTTLHours) * time.Hour, LoginRateLimitPerMin: cfg.Server.Auth.LoginRateLimitPerMin,
	}, bus, log)

	notifier := notify.New(bus, log, notify.Options{})
	notifier.Configure(cfg.Notifications)

	a := &App{
		cfg: cfg, log: log, guard: guard, backend: backend, sim: simBackend,
		db: db, bus: bus, target: target, devices: devices, engine: engine, sched: sched, service: service, auth: authMgr, notify: notifier, started: time.Now(),
	}
	for _, o := range opts {
		o(a)
	}
	a.autoBackup.Store(cfg.Device.AutoBackup)
	devices.OnAttached = a.onDeviceAttached
	devices.OnDetached = a.onDeviceDetached
	deps := api.Deps{
		Planner: engine,
		Backups: service,
		Queue:   sched,
		Guard:   guard, PlatformName: backend.Name, Version: version.Version, StartedAt: a.started,
		DB: db, Target: target, Storage: (*storageControl)(a), Devices: devices,
		Events: repository.Events{DB: db.DB}, Tasks: repository.Tasks{DB: db.DB},
		Static: web.Dist(), AuthEnabled: cfg.Server.Auth.Enabled, Auth: authMgr, Bus: bus, Config: (*configStore)(a), Notify: notifier, Log: log,
	}
	if simBackend != nil {
		deps.Sim = simBackend
	}
	if faults != nil {
		deps.Faults = faults
	}
	a.handler = api.NewRouter(deps)
	return a, nil
}

// onDeviceAttached auto_backup 为 true 时在设备就绪后自动规划并开始备份；dry-run 只规划。
func (a *App) onDeviceAttached(ctx context.Context, d domain.Device) {
	if !a.autoBackup.Load() {
		return
	}
	res, err := a.service.Start(ctx)
	if err != nil {
		if !errors.Is(err, syncengine.ErrPlanRunning) {
			a.log.Error("auto backup failed", "device", d.Key, "err", err)
		}
		return
	}
	if a.guard.Mode() == runmode.DryRun {
		a.log.Info("dry-run: plan generated, no transfer", "device", d.Key, "upload_files", res.Plan.Upload.Files, "upload_bytes", res.Plan.Upload.Bytes)
		return
	}
	a.log.Info("auto backup queued", "device", d.Key, "created", res.Created, "requeued", res.Requeued, "conflicts", res.Conflicts)
}

// onDeviceDetached 拔出后让挂载器清掉残留的只读挂载（Linux 上经挂载守护延迟卸载）。
func (a *App) onDeviceDetached(ctx context.Context, d domain.Device) {
	for _, v := range d.Volumes {
		if v.DevicePath == "" {
			continue
		}
		rv := platform.RawVolume{Key: v.Key, LUN: v.LUN, Label: v.Label, Filesystem: v.Filesystem, SizeBytes: v.SizeBytes, DevicePath: v.DevicePath}
		if err := a.backend.Mounter.Unmount(context.WithoutCancel(ctx), rv); err != nil {
			a.log.Warn("unmount detached volume failed", "device", d.Key, "volume", v.Key, "err", err)
		}
	}
}

// ensureTargetMounted cifs 目标由平台挂载器挂载；失败只告警，探测继续反映真实状态。
func (a *App) ensureTargetMounted(ctx context.Context) {
	if a.backend.Storage == nil {
		return
	}
	if h := a.target.Health(ctx); h.OK {
		return
	}
	if _, err := a.mountTarget(ctx); err != nil {
		a.log.Warn("mount storage target failed", "err", err)
	}
}

// mountTarget 经平台挂载器挂载目标并复查健康；成功发 storage.mounted，失败发 storage.mount_failed。
func (a *App) mountTarget(ctx context.Context) (storage.Health, error) {
	if err := a.backend.Storage.MountTarget(ctx); err != nil {
		a.bus.Publish(ctx, events.Event{Level: "warn", Type: "storage.mount_failed", Message: "storage target mount failed: " + err.Error(), Data: map[string]any{"error": err.Error()}})
		return a.target.Health(ctx), err
	}
	h := a.target.Health(ctx)
	if !h.OK {
		err := fmt.Errorf("mounted but probe failed: %s", h.Error)
		a.bus.Publish(ctx, events.Event{Level: "warn", Type: "storage.mount_failed", Message: "storage target mount failed: " + err.Error(), Data: map[string]any{"error": h.Error}})
		return h, err
	}
	a.bus.Publish(ctx, events.Event{Level: "info", Type: "storage.mounted", Message: "storage target mounted", Data: map[string]any{"root": h.Root, "fs": h.FSType}})
	return h, nil
}

// unmountTarget 经平台挂载器卸载目标，发 storage.unmounted。
func (a *App) unmountTarget(ctx context.Context) (storage.Health, error) {
	if err := a.backend.Storage.UnmountTarget(ctx); err != nil {
		return a.target.Health(ctx), err
	}
	h := a.target.Health(ctx)
	a.bus.Publish(ctx, events.Event{Level: "info", Type: "storage.unmounted", Message: "storage target unmounted", Data: map[string]any{"mount_point": h.MountPoint}})
	return h, nil
}

// storageControl 把 App 的目标挂载操作暴露给 API（api.StorageControl）；
// 平台无挂载器（mac、simulation、premounted / local）时 Supported 为 false，方法返回 ErrNotImplemented。
type storageControl App

func (c *storageControl) app() *App { return (*App)(c) }

// Supported 本平台是否有目标挂载器。
func (c *storageControl) Supported() bool { return c.app().backend.Storage != nil }

// Mount 挂载目标（已挂且健康时守护直接返回）。
func (c *storageControl) Mount(ctx context.Context) (storage.Health, error) {
	a := c.app()
	if a.backend.Storage == nil {
		return a.target.Health(ctx), domain.ErrNotImplemented
	}
	return a.mountTarget(ctx)
}

// Remount 先卸载再挂载；卸载失败（例如根本没挂）不阻止随后的挂载。
func (c *storageControl) Remount(ctx context.Context) (storage.Health, error) {
	a := c.app()
	if a.backend.Storage == nil {
		return a.target.Health(ctx), domain.ErrNotImplemented
	}
	if err := a.backend.Storage.UnmountTarget(ctx); err != nil {
		a.log.Warn("remount: unmount step failed, continuing", "err", err)
	}
	return a.mountTarget(ctx)
}

// Unmount 卸载目标。
func (c *storageControl) Unmount(ctx context.Context) (storage.Health, error) {
	a := c.app()
	if a.backend.Storage == nil {
		return a.target.Health(ctx), domain.ErrNotImplemented
	}
	return a.unmountTarget(ctx)
}

// superviseTarget 目标不可用时每 reconcile 周期重试挂载（NAS 开机晚于设备、掉线恢复）。
func (a *App) superviseTarget(ctx context.Context) {
	if a.backend.Storage == nil {
		return
	}
	interval := time.Duration(a.cfg.Backup.ReconcileIntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.ensureTargetMounted(ctx)
		}
	}
}

// Auth 返回会话管理器。
func (a *App) Auth() *auth.Manager { return a.auth }

// Notifier 返回通知分发器（测试用）。
func (a *App) Notifier() *notify.Dispatcher { return a.notify }

// Scheduler 返回调度器。
func (a *App) Scheduler() *scheduler.Scheduler { return a.sched }

// Service 返回备份服务。
func (a *App) Service() *backup.Service { return a.service }

// Engine 返回同步引擎。
func (a *App) Engine() *syncengine.Engine { return a.engine }

// Handler 返回 HTTP 处理器，供测试直接调用。
func (a *App) Handler() http.Handler { return a.handler }

// Guard 返回运行模式门禁。
func (a *App) Guard() *runmode.Guard { return a.guard }

// Sim 返回模拟后端；非 simulation 为 nil。
func (a *App) Sim() *sim.Backend { return a.sim }

// Addr 返回实际监听地址（Run 之后有效）。
func (a *App) Addr() string {
	a.addrMu.Lock()
	defer a.addrMu.Unlock()
	if a.listener == nil {
		return ""
	}
	return a.listener.Addr().String()
}

// Run 启动设备管理与 HTTP 服务，直到 ctx 取消。
func (a *App) Run(ctx context.Context) error {
	defer a.db.Close()

	if a.auth.Enabled() {
		a.log.Info("authentication enabled", "username", a.cfg.Server.Auth.Username, "session_ttl_hours", a.cfg.Server.Auth.SessionTTLHours)
	} else {
		a.log.Warn("authentication disabled; API is open to anyone who can reach the listen address")
	}
	a.notify.Start(ctx)
	a.bus.Publish(ctx, events.Event{Type: "system.started", Message: "service started",
		Data: map[string]any{"version": version.Version, "mode": string(a.guard.Mode()), "platform": a.backend.Name}})

	a.ensureTargetMounted(ctx)
	if h := a.target.Health(ctx); !h.OK {
		a.log.Warn("storage target not ready", "kind", h.Kind, "mount_point", h.MountPoint, "err", h.Error)
	} else {
		a.log.Info("storage target ready", "kind", h.Kind, "root", h.Root, "fs", h.FSType, "free_bytes", h.FreeBytes)
	}
	go a.superviseTarget(ctx)

	if n, err := (repository.Tasks{DB: a.db.DB}).MarkInterruptedOnStartup(ctx); err != nil {
		return fmt.Errorf("recover tasks: %w", err)
	} else if n > 0 {
		a.bus.Publish(ctx, events.Event{Level: "warn", Type: "recovery.interrupted", Message: fmt.Sprintf("%d task(s) were in progress at last shutdown, marked INTERRUPTED", n), Data: map[string]any{"count": n}})
	}
	if a.guard.AllowDestinationWrite() {
		a.sched.Start(ctx)
	}
	if a.cfg.Device.AutoDetect {
		if err := a.devices.Start(ctx); err != nil {
			return fmt.Errorf("start device manager: %w", err)
		}
	}

	ln, err := net.Listen("tcp", a.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", a.cfg.Server.Listen, err)
	}
	a.addrMu.Lock()
	a.listener = ln
	a.addrMu.Unlock()
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	a.log.Info("http listening", "addr", ln.Addr().String(), "mode", string(a.guard.Mode()), "platform", a.backend.Name)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.bus.Publish(shutdownCtx, events.Event{Type: "system.stopping", Message: "service stopping"})
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
