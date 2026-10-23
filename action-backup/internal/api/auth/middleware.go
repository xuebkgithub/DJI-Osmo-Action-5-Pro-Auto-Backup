package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type ctxKey struct{}

// FromContext 取中间件放入的会话。
func FromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(*Session)
	return s, ok
}

// Policy 决定哪些路径免认证。
type Policy struct {
	// APIPrefix 需要认证的前缀；其它路径（静态资源）放行。
	APIPrefix string
	// Public 免认证的精确路径。
	Public []string
}

// DefaultPolicy 默认：/api/ 下除登录与健康检查外全部要求会话。
func DefaultPolicy() Policy {
	return Policy{APIPrefix: "/api/", Public: []string{"/api/v1/auth/login", "/api/v1/system/health"}}
}

// Middleware 认证门禁：未登录 401，写操作缺少 CSRF 403。认证未启用时直接放行。
func (m *Manager) Middleware(p Policy) func(http.Handler) http.Handler {
	public := map[string]bool{}
	for _, path := range p.Public {
		public[path] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !m.cfg.Enabled || !strings.HasPrefix(r.URL.Path, p.APIPrefix) || public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			s, ok := m.SessionFromRequest(r)
			if !ok {
				if _, err := r.Cookie(CookieName); err == nil {
					ClearCookie(w)
				}
				writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "login required")
				return
			}
			if isWrite(r.Method) && !CheckCSRF(r, s) {
				writeError(w, http.StatusForbidden, "CSRF_INVALID", ErrCSRF.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
		})
	}
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// writeError 与 api 包相同的错误信封；auth 不能 import api（避免循环）。
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "retryable": false}})
}
