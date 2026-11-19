package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"action-backup/internal/update"
)

// updateStatus 返回新版本检查的当前状态（不触发网络请求）。
func (h *handlers) updateStatus(w http.ResponseWriter, _ *http.Request) {
	if h.d.Update == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "update checker not available", false)
		return
	}
	writeData(w, h.d.Update.Status())
}

// updateCheck 立即检查一次。失败返回 502，同时附当前状态，前端可直接刷新「软件更新」一栏。
func (h *handlers) updateCheck(w http.ResponseWriter, r *http.Request) {
	if h.d.Update == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "update checker not available", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	st, err := h.d.Update.Check(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, struct {
			Data  any       `json:"data"`
			Error errorBody `json:"error"`
		}{Data: st, Error: errorBody{Code: "UPDATE_CHECK_FAILED", Message: err.Error(), Retryable: true}})
		return
	}
	writeData(w, st)
}

// updateApply 开始安装最新版本：下载、验签、等备份空闲后提交安装请求，立即返回 202。
// 之后 root 定时任务（每分钟一次）接手，期间本服务会被重启；进度看 GET /system/update 的 install 段。
func (h *handlers) updateApply(w http.ResponseWriter, _ *http.Request) {
	if h.d.Update == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "update checker not available", false)
		return
	}
	st, err := h.d.Update.Apply()
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, envelope{Data: st})
	case errors.Is(err, update.ErrInstallUnsupported):
		writeError(w, http.StatusNotImplemented, "INSTALL_NOT_SUPPORTED", err.Error(), false)
	case errors.Is(err, update.ErrNoUpdate):
		writeError(w, http.StatusConflict, "NO_UPDATE", err.Error(), false)
	case errors.Is(err, update.ErrNotInstallable):
		writeError(w, http.StatusConflict, "NOT_INSTALLABLE", err.Error(), false)
	case errors.Is(err, update.ErrInstallInProgress):
		writeError(w, http.StatusConflict, "INSTALL_IN_PROGRESS", err.Error(), false)
	default:
		writeError(w, http.StatusInternalServerError, "INSTALL_FAILED", err.Error(), true)
	}
}
