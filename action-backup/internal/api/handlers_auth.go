package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"action-backup/internal/api/auth"
	"action-backup/internal/config"
	"action-backup/internal/domain"
	"action-backup/internal/notify"
)

// ConfigStore 运行时配置：读取脱敏副本、按白名单修改并持久化。
type ConfigStore interface {
	Current() config.Config
	Path() string
	Patch(ctx context.Context, p config.Patch) (config.Config, error)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	AuthEnabled   bool   `json:"auth_enabled"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	CSRFToken     string `json:"csrf_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

func (h *handlers) authEnabled() bool { return h.d.Auth != nil && h.d.Auth.Enabled() }

func (h *handlers) authLogin(w http.ResponseWriter, r *http.Request) {
	if !h.authEnabled() {
		writeData(w, sessionResponse{AuthEnabled: false, Authenticated: true})
		return
	}
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", "expected {username, password}", false)
		return
	}
	s, err := h.d.Auth.Login(r.Context(), req.Username, req.Password, auth.RemoteIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", err.Error(), true)
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", err.Error(), false)
		default:
			writeError(w, http.StatusInternalServerError, "LOGIN_FAILED", err.Error(), true)
		}
		return
	}
	h.d.Auth.SetCookie(w, s)
	writeData(w, sessionResponse{AuthEnabled: true, Authenticated: true, Username: s.Username, CSRFToken: s.CSRFToken, ExpiresAt: s.ExpiresAt.UTC().Format(time.RFC3339)})
}

func (h *handlers) authLogout(w http.ResponseWriter, r *http.Request) {
	if h.authEnabled() {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			h.d.Auth.Logout(r.Context(), c.Value)
		}
		auth.ClearCookie(w)
	}
	writeData(w, map[string]any{"ok": true})
}

func (h *handlers) authMe(w http.ResponseWriter, r *http.Request) {
	if !h.authEnabled() {
		writeData(w, sessionResponse{AuthEnabled: false, Authenticated: true})
		return
	}
	s, ok := auth.FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "login required", false)
		return
	}
	writeData(w, sessionResponse{AuthEnabled: true, Authenticated: true, Username: s.Username, CSRFToken: s.CSRFToken, ExpiresAt: s.ExpiresAt.UTC().Format(time.RFC3339)})
}

type configResponse struct {
	Path      string              `json:"path"`
	Config    config.Config       `json:"config"`
	Patchable []config.PatchField `json:"patchable"`
}

func (h *handlers) configGet(w http.ResponseWriter, _ *http.Request) {
	if h.d.Config == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "config store not available", false)
		return
	}
	writeData(w, configResponse{Path: h.d.Config.Path(), Config: h.d.Config.Current(), Patchable: config.PatchableFields()})
}

func (h *handlers) configPatch(w http.ResponseWriter, r *http.Request) {
	if h.d.Config == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "config store not available", false)
		return
	}
	var raw map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error(), false)
		return
	}
	p, err := config.NormalizePatch(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PATCH", err.Error(), false)
		return
	}
	cfg, err := h.d.Config.Patch(r.Context(), p)
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIG", err.Error(), false)
			return
		}
		writeError(w, http.StatusInternalServerError, "CONFIG_WRITE_FAILED", err.Error(), true)
		return
	}
	writeData(w, configResponse{Path: h.d.Config.Path(), Config: cfg, Patchable: config.PatchableFields()})
}

type storageTestResponse struct {
	OK           bool   `json:"ok"`
	Kind         string `json:"kind"`
	MountPoint   string `json:"mount_point"`
	Root         string `json:"root"`
	FSType       string `json:"fs_type,omitempty"`
	Source       string `json:"source,omitempty"`
	FreeBytes    uint64 `json:"free_bytes"`
	RootExists   bool   `json:"root_exists"`
	WriteAllowed bool   `json:"write_allowed"`
	LatencyMS    int64  `json:"latency_ms"`
	Error        string `json:"error,omitempty"`
	CheckedAt    string `json:"checked_at"`
}

// storageTest 只读探测目标存储：挂载类型、来源、剩余空间、根目录是否存在。不写入任何文件。
func (h *handlers) storageTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	start := time.Now()
	hl := h.d.Target.Health(ctx)
	resp := storageTestResponse{
		OK: hl.OK, Kind: hl.Kind, MountPoint: hl.MountPoint, Root: hl.Root, FSType: hl.FSType, Source: hl.Source,
		FreeBytes: hl.FreeBytes, WriteAllowed: h.d.Guard.AllowDestinationWrite(), Error: hl.Error, CheckedAt: hl.CheckedAt,
	}
	resp.RootExists = h.d.Target.RootExists()
	resp.LatencyMS = time.Since(start).Milliseconds()
	writeData(w, resp)
}

func (h *handlers) deviceVolumes(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	for _, d := range h.d.Devices.Attached() {
		if (id > 0 && d.ID == id) || d.Key == idStr {
			writeData(w, toDeviceDTO(d).Volumes)
			return
		}
	}
	writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", domain.ErrDeviceNotFound.Error(), false)
}

type notifyTestRequest struct {
	Channel string `json:"channel"`
}

// notifyTest 用当前运行配置向指定渠道（meow | telegram）发一条测试消息。
func (h *handlers) notifyTest(w http.ResponseWriter, r *http.Request) {
	if h.d.Notify == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "notifier not available", false)
		return
	}
	var req notifyTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error(), false)
		return
	}
	switch req.Channel {
	case notify.ChannelMeoW, notify.ChannelTelegram:
	default:
		writeError(w, http.StatusBadRequest, "BAD_CHANNEL", "channel must be meow or telegram", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeData(w, h.d.Notify.Test(ctx, req.Channel))
}
