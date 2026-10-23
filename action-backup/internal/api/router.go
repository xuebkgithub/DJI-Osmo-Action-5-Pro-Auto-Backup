package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"action-backup/internal/backup"
	"action-backup/internal/database"
	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/faultinject"
	"action-backup/internal/runmode"
	"action-backup/internal/scheduler"
	"action-backup/internal/storage"
)

// DeviceSource 当前已插入设备。
type DeviceSource interface {
	Attached() []domain.Device
	Rescan(ctx context.Context) error
}

// SimControl simulation 模式下的设备控制；非 simulation 为 nil。
type SimControl interface {
	Keys() []string
	Attach(key string) error
	Detach(key string) error
	Attached(key string) (bool, error)
}

// Planner 生成与查询备份计划。
type Planner interface {
	Plan(ctx context.Context) (*domain.Plan, error)
	Last() *domain.Plan
}

// Backups 备份控制。
type Backups interface {
	Start(ctx context.Context) (*backup.StartResult, error)
}

// Queue 调度控制。
type Queue interface {
	Pause()
	Resume()
	Cancel(ctx context.Context, id int64) error
	Retry(ctx context.Context, id int64) error
	State() scheduler.State
}

// Deps 处理器依赖。
type Deps struct {
	Planner      Planner
	Backups      Backups
	Queue        Queue
	Guard        *runmode.Guard
	PlatformName string
	Version      string
	StartedAt    time.Time
	DB           *database.DB
	Target       *storage.Target
	Devices      DeviceSource
	Events       repository.Events
	Tasks        repository.Tasks
	Sim          SimControl
	Faults       faultinject.Controller
	Static       fs.FS
	AuthEnabled  bool
	Log          *slog.Logger
}

// NewRouter 组装路由。
func NewRouter(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	h := &handlers{d: d}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/system/health", h.health)
	mux.HandleFunc("GET /api/v1/system/status", h.status)
	mux.HandleFunc("GET /api/v1/system/mode", h.mode)
	mux.HandleFunc("GET /api/v1/devices", h.devices)
	mux.HandleFunc("POST /api/v1/devices/rescan", h.rescan)
	mux.HandleFunc("GET /api/v1/events", h.events)
	mux.HandleFunc("POST /api/v1/backups/plan", h.planRun)
	mux.HandleFunc("GET /api/v1/backups/plan", h.planLast)
	mux.HandleFunc("POST /api/v1/backups/start", h.backupStart)
	mux.HandleFunc("POST /api/v1/backups/pause", h.backupPause)
	mux.HandleFunc("POST /api/v1/backups/resume", h.backupResume)
	mux.HandleFunc("GET /api/v1/backups/history", h.backupHistory)
	mux.HandleFunc("GET /api/v1/tasks", h.tasksList)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.taskGet)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", h.taskCancel)
	mux.HandleFunc("POST /api/v1/tasks/{id}/retry", h.taskRetry)

	if d.Guard.TestEndpointsEnabled() && d.Sim != nil {
		mux.HandleFunc("GET /api/v1/test/sim/devices", h.simDevices)
		mux.HandleFunc("POST /api/v1/test/sim/devices/{key}/attach", h.simAttach)
		mux.HandleFunc("POST /api/v1/test/sim/devices/{key}/detach", h.simDetach)
	}
	if d.Guard.TestEndpointsEnabled() && d.Faults != nil {
		mux.HandleFunc("GET /api/v1/test/faults", h.faultsList)
		mux.HandleFunc("POST /api/v1/test/faults", h.faultsAdd)
		mux.HandleFunc("DELETE /api/v1/test/faults/{id}", h.faultsRemove)
		mux.HandleFunc("POST /api/v1/test/faults/reset", h.faultsReset)
	}

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint", false)
	})
	if d.Static != nil {
		mux.Handle("/", http.FileServerFS(d.Static))
	}

	var handler http.Handler = mux
	handler = http.TimeoutHandler(handler, 30*time.Second, `{"error":{"code":"TIMEOUT","message":"request timed out","retryable":true}}`)
	handler = securityHeaders(handler)
	handler = logging(d.Log)(handler)
	handler = recovery(d.Log)(handler)
	return handler
}
