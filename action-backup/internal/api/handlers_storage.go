package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"action-backup/internal/domain"
	"action-backup/internal/storage"
)

// storageControl 把挂载 / 卸载 / 重新挂载统一成一个处理流程：不支持 → 501，失败 → 503 并附当前健康。
func (h *handlers) storageControl(w http.ResponseWriter, r *http.Request, name string, fn func(context.Context) (storage.Health, error)) {
	if h.d.Storage == nil || !h.d.Storage.Supported() {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "storage mount control is not available on this platform", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	hl, err := fn(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotImplemented) {
			writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", err.Error(), false)
			return
		}
		if errors.Is(err, domain.ErrStorageBusy) {
			writeError(w, http.StatusConflict, "STORAGE_BUSY", err.Error(), true)
			return
		}
		// 失败时同时返回当前健康，前端可直接刷新目标存储一栏
		writeJSON(w, http.StatusServiceUnavailable, struct {
			Data  any       `json:"data"`
			Error errorBody `json:"error"`
		}{Data: map[string]any{"action": name, "health": hl}, Error: errorBody{Code: "STORAGE_" + name + "_FAILED", Message: err.Error(), Retryable: true}})
		return
	}
	writeData(w, map[string]any{"action": name, "health": hl})
}

// storageMount 按配置挂载目标（已挂且健康则直接返回）。
func (h *handlers) storageMount(w http.ResponseWriter, r *http.Request) {
	h.storageControl(w, r, "MOUNT", h.d.Storage.Mount)
}

// storageRemount 卸载后重新挂载：用于 NAS 掉线残留、只读、凭据或共享改动后的恢复。
func (h *handlers) storageRemount(w http.ResponseWriter, r *http.Request) {
	h.storageControl(w, r, "REMOUNT", h.d.Storage.Remount)
}

// storageUnmount 卸载目标；有任务正在传输时拒绝（409）。
func (h *handlers) storageUnmount(w http.ResponseWriter, r *http.Request) {
	h.storageControl(w, r, "UNMOUNT", func(ctx context.Context) (storage.Health, error) {
		if h.d.Queue != nil {
			if st := h.d.Queue.State(); st.Current != nil {
				return h.d.Target.Health(ctx), domain.ErrStorageBusy
			}
		}
		return h.d.Storage.Unmount(ctx)
	})
}

type retryFailedResponse struct {
	Retried int     `json:"retried"`
	Skipped int     `json:"skipped"`
	IDs     []int64 `json:"task_ids"`
}

// backupRetryFailed 把全部 FAILED / INTERRUPTED 任务重新入队，包括已用尽自动重试次数的；
// 单个任务失败不影响其它任务。
func (h *handlers) backupRetryFailed(w http.ResponseWriter, r *http.Request) {
	if !h.requireWrite(w) {
		return
	}
	ctx := r.Context()
	resp := retryFailedResponse{IDs: []int64{}}
	for _, st := range []domain.TaskStatus{domain.TaskInterrupted, domain.TaskFailed} {
		items, _, err := h.d.Tasks.List(ctx, string(st), 500, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error(), true)
			return
		}
		for i := len(items) - 1; i >= 0; i-- { // 旧任务优先
			if err := h.d.Queue.Retry(ctx, items[i].ID); err != nil {
				resp.Skipped++
				continue
			}
			resp.Retried++
			resp.IDs = append(resp.IDs, items[i].ID)
		}
	}
	writeData(w, resp)
}
