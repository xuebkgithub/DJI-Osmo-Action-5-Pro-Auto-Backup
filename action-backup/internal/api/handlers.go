package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/faultinject"
	"action-backup/internal/scheduler"
	"action-backup/internal/storage"
	"action-backup/internal/syncengine"
)

type handlers struct{ d Deps }

type component struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type healthResponse struct {
	Status   string         `json:"status"`
	Database component      `json:"database"`
	Storage  storage.Health `json:"storage"`
	Devices  struct {
		OK       bool `json:"ok"`
		Attached int  `json:"attached"`
	} `json:"devices"`
	CheckedAt string `json:"checked_at"`
}

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	resp := healthResponse{Status: "ok", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	resp.Database.OK = true
	if err := h.d.DB.PingContext(ctx); err != nil {
		resp.Database = component{OK: false, Error: err.Error()}
		resp.Status = "degraded"
	}
	resp.Storage = h.d.Target.Health(ctx)
	if !resp.Storage.OK {
		resp.Status = "degraded"
	}
	resp.Devices.OK = true
	resp.Devices.Attached = len(h.d.Devices.Attached())
	status := http.StatusOK
	if resp.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, envelope{Data: resp})
}

type statusResponse struct {
	Version       string            `json:"version"`
	Mode          string            `json:"mode"`
	Platform      string            `json:"platform"`
	StartedAt     string            `json:"started_at"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	SchemaVersion int               `json:"schema_version"`
	Devices       int               `json:"devices_attached"`
	Tasks         map[string]int    `json:"tasks"`
	Storage       map[string]string `json:"storage"`
	Scheduler     *scheduler.State  `json:"scheduler,omitempty"`
	Auth          map[string]any    `json:"auth"`
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := statusResponse{
		Version:       h.d.Version,
		Mode:          string(h.d.Guard.Mode()),
		Platform:      h.d.PlatformName,
		StartedAt:     h.d.StartedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(h.d.StartedAt).Seconds()),
		Devices:       len(h.d.Devices.Attached()),
		Tasks:         map[string]int{},
		Storage:       map[string]string{"kind": h.d.Target.Kind(), "root": h.d.Target.Root()},
	}
	if v, err := h.d.DB.SchemaVersion(ctx); err == nil {
		resp.SchemaVersion = v
	}
	counts, err := h.d.Tasks.CountByStatus(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
		return
	}
	for s, n := range counts {
		resp.Tasks[string(s)] = n
	}
	if h.d.Queue != nil {
		st := h.d.Queue.State()
		resp.Scheduler = &st
	}
	resp.Auth = map[string]any{"enabled": h.authEnabled()}
	if h.authEnabled() {
		resp.Auth["active_sessions"] = h.d.Auth.ActiveSessions()
	}
	writeData(w, resp)
}

type modeResponse struct {
	Mode                    string `json:"mode"`
	Platform                string `json:"platform"`
	DeviceBackend           string `json:"device_backend"`
	StorageBackend          string `json:"storage_backend"`
	FaultInjectBuild        bool   `json:"faultinject_build"`
	DestinationWriteAllowed bool   `json:"destination_write_allowed"`
	TestEndpointsEnabled    bool   `json:"test_endpoints_enabled"`
	AuthEnabled             bool   `json:"auth_enabled"`
	// StorageMountControl 本平台是否支持网页手工挂载 / 卸载目标（Linux cifs）。
	StorageMountControl bool `json:"storage_mount_control"`
}

func (h *handlers) mode(w http.ResponseWriter, _ *http.Request) {
	writeData(w, modeResponse{
		Mode:                    string(h.d.Guard.Mode()),
		Platform:                h.d.PlatformName,
		DeviceBackend:           h.d.PlatformName,
		StorageBackend:          h.d.Target.Kind(),
		FaultInjectBuild:        faultinject.Enabled,
		DestinationWriteAllowed: h.d.Guard.AllowDestinationWrite(),
		TestEndpointsEnabled:    h.d.Guard.TestEndpointsEnabled(),
		AuthEnabled:             h.authEnabled(),
		StorageMountControl:     h.d.Storage != nil && h.d.Storage.Supported(),
	})
}

type volumeDTO struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	LUN        int    `json:"lun"`
	Filesystem string `json:"filesystem"`
	SizeBytes  int64  `json:"size_bytes"`
	MountPath  string `json:"mount_path"`
}

type deviceDTO struct {
	ID           int64       `json:"id"`
	Key          string      `json:"key"`
	Name         string      `json:"name"`
	VendorID     string      `json:"vendor_id"`
	ProductID    string      `json:"product_id"`
	Serial       string      `json:"serial"`
	WeakIdentity bool        `json:"weak_identity"`
	LastSeenAt   string      `json:"last_seen_at"`
	Volumes      []volumeDTO `json:"volumes"`
}

func toDeviceDTO(d domain.Device) deviceDTO {
	out := deviceDTO{
		ID: d.ID, Key: d.Key, Name: d.Name, VendorID: d.VendorID, ProductID: d.ProductID,
		Serial: d.SerialNumber, WeakIdentity: d.WeakIdentity, LastSeenAt: d.LastSeenAt.UTC().Format(time.RFC3339),
		Volumes: []volumeDTO{},
	}
	for _, v := range d.Volumes {
		out.Volumes = append(out.Volumes, volumeDTO{
			Key: v.Key, Type: string(v.Type), Label: v.Label, LUN: v.LUN,
			Filesystem: v.Filesystem, SizeBytes: v.SizeBytes, MountPath: v.MountPath,
		})
	}
	return out
}

func (h *handlers) devices(w http.ResponseWriter, _ *http.Request) {
	devs := h.d.Devices.Attached()
	out := make([]deviceDTO, 0, len(devs))
	for _, d := range devs {
		out = append(out, toDeviceDTO(d))
	}
	writeData(w, out)
}

func (h *handlers) rescan(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Devices.Rescan(r.Context()); err != nil {
		if errors.Is(err, domain.ErrNotImplemented) {
			writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", err.Error(), false)
			return
		}
		writeError(w, http.StatusInternalServerError, "RESCAN_FAILED", err.Error(), true)
		return
	}
	h.devices(w, r)
}

func (h *handlers) events(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "BAD_LIMIT", "limit must be 1..1000", false)
			return
		}
		limit = n
	}
	evs, err := h.d.Events.ListRecent(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
		return
	}
	if evs == nil {
		evs = []repository.EventRecord{}
	}
	writeData(w, evs)
}

type simDeviceDTO struct {
	Key      string `json:"key"`
	Attached bool   `json:"attached"`
}

func (h *handlers) simDevices(w http.ResponseWriter, _ *http.Request) {
	keys := h.d.Sim.Keys()
	out := make([]simDeviceDTO, 0, len(keys))
	for _, k := range keys {
		a, _ := h.d.Sim.Attached(k)
		out = append(out, simDeviceDTO{Key: k, Attached: a})
	}
	writeData(w, out)
}

func (h *handlers) simAttach(w http.ResponseWriter, r *http.Request) {
	h.simToggle(w, r, h.d.Sim.Attach)
}

func (h *handlers) simDetach(w http.ResponseWriter, r *http.Request) {
	h.simToggle(w, r, h.d.Sim.Detach)
}

func (h *handlers) simToggle(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	key := r.PathValue("key")
	if err := fn(key); err != nil {
		if errors.Is(err, domain.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", err.Error(), false)
			return
		}
		writeError(w, http.StatusInternalServerError, "SIM_ERROR", err.Error(), false)
		return
	}
	a, _ := h.d.Sim.Attached(key)
	writeData(w, simDeviceDTO{Key: key, Attached: a})
}

func (h *handlers) planRun(w http.ResponseWriter, r *http.Request) {
	if h.d.Planner == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "planner not available", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	plan, err := h.d.Planner.Plan(ctx)
	if err != nil {
		if errors.Is(err, syncengine.ErrPlanRunning) {
			writeError(w, http.StatusConflict, "PLAN_RUNNING", err.Error(), true)
			return
		}
		writeError(w, http.StatusInternalServerError, "PLAN_FAILED", err.Error(), true)
		return
	}
	writeData(w, plan)
}

func (h *handlers) planLast(w http.ResponseWriter, _ *http.Request) {
	if h.d.Planner == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "planner not available", false)
		return
	}
	plan := h.d.Planner.Last()
	if plan == nil {
		writeError(w, http.StatusNotFound, "NO_PLAN", "no plan generated yet; POST /api/v1/backups/plan", false)
		return
	}
	writeData(w, plan)
}

func (h *handlers) requireWrite(w http.ResponseWriter) bool {
	if !h.d.Guard.AllowDestinationWrite() {
		writeError(w, http.StatusConflict, "MODE_READ_ONLY", "destination writes are disabled in mode "+string(h.d.Guard.Mode()), false)
		return false
	}
	if h.d.Backups == nil || h.d.Queue == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "backup service not available", false)
		return false
	}
	return true
}

func (h *handlers) backupStart(w http.ResponseWriter, r *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	res, err := h.d.Backups.Start(ctx)
	if err != nil {
		if errors.Is(err, syncengine.ErrPlanRunning) {
			writeError(w, http.StatusConflict, "PLAN_RUNNING", err.Error(), true)
			return
		}
		writeError(w, http.StatusInternalServerError, "START_FAILED", err.Error(), true)
		return
	}
	writeData(w, res)
}

func (h *handlers) backupPause(w http.ResponseWriter, _ *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	h.d.Queue.Pause()
	writeData(w, h.d.Queue.State())
}

func (h *handlers) backupResume(w http.ResponseWriter, _ *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	h.d.Queue.Resume()
	writeData(w, h.d.Queue.State())
}

func (h *handlers) backupHistory(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := paging(w, r)
	if !ok {
		return
	}
	items, total, err := h.d.Tasks.List(r.Context(), string(domain.TaskCompleted), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
		return
	}
	writeData(w, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *handlers) tasksList(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := paging(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" {
		valid := false
		for _, s := range domain.AllTaskStatuses {
			if string(s) == strings.ToUpper(status) {
				valid = true
			}
		}
		if !valid {
			writeError(w, http.StatusBadRequest, "BAD_STATUS", "unknown status "+status, false)
			return
		}
	}
	items, total, err := h.d.Tasks.List(r.Context(), status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
		return
	}
	writeData(w, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *handlers) taskGet(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	t, err := h.d.Tasks.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "TASK_NOT_FOUND", "task not found", false)
		return
	}
	resp := map[string]any{"task": t}
	if h.d.Queue != nil {
		if st := h.d.Queue.State(); st.Current != nil && st.Current.TaskID == id {
			resp["progress"] = st.Current
		}
	}
	writeData(w, resp)
}

func (h *handlers) taskCancel(w http.ResponseWriter, r *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if err := h.d.Queue.Cancel(r.Context(), id); err != nil {
		h.taskControlError(w, err)
		return
	}
	h.taskGet(w, r)
}

func (h *handlers) taskRetry(w http.ResponseWriter, r *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if err := h.d.Queue.Retry(r.Context(), id); err != nil {
		h.taskControlError(w, err)
		return
	}
	h.taskGet(w, r)
}

func (h *handlers) taskControlError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidTransition) {
		writeError(w, http.StatusConflict, "INVALID_TRANSITION", err.Error(), false)
		return
	}
	if strings.Contains(err.Error(), "not found") {
		writeError(w, http.StatusNotFound, "TASK_NOT_FOUND", err.Error(), false)
		return
	}
	writeError(w, http.StatusInternalServerError, "TASK_ERROR", err.Error(), true)
}

func taskID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_ID", "task id must be a positive integer", false)
		return 0, false
	}
	return id, true
}

func paging(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, offset = 50, 0
	q := r.URL.Query()
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "BAD_LIMIT", "limit must be 1..500", false)
			return 0, 0, false
		}
		limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_OFFSET", "offset must be >= 0", false)
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func (h *handlers) faultsList(w http.ResponseWriter, _ *http.Request) {
	writeData(w, h.d.Faults.Rules())
}

func (h *handlers) faultsAdd(w http.ResponseWriter, r *http.Request) {
	var rule faultinject.Rule
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error(), false)
		return
	}
	if err := h.d.Faults.Add(rule); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_RULE", err.Error(), false)
		return
	}
	writeData(w, h.d.Faults.Rules())
}

func (h *handlers) faultsRemove(w http.ResponseWriter, r *http.Request) {
	if !h.d.Faults.Remove(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "RULE_NOT_FOUND", "no such fault rule", false)
		return
	}
	writeData(w, h.d.Faults.Rules())
}

func (h *handlers) faultsReset(w http.ResponseWriter, _ *http.Request) {
	h.d.Faults.Reset()
	writeData(w, h.d.Faults.Rules())
}
