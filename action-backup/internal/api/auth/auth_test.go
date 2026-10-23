package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"action-backup/internal/events"
)

type sink struct{ types []string }

func (s *sink) Publish(_ context.Context, e events.Event) { s.types = append(s.types, e.Type) }

func newManager(t *testing.T) (*Manager, *sink) {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	rec := &sink{}
	m := New(Config{Enabled: true, Username: "admin", PasswordHash: string(h), SessionTTL: time.Hour, LoginRateLimitPerMin: 3}, rec, nil)
	return m, rec
}

func TestLoginLogout(t *testing.T) {
	m, rec := newManager(t)
	ctx := context.Background()
	if _, err := m.Login(ctx, "admin", "wrong", "10.0.0.1"); err != ErrInvalidCredentials {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := m.Login(ctx, "nobody", "secret", "10.0.0.1"); err != ErrInvalidCredentials {
		t.Fatalf("wrong user: %v", err)
	}
	s, err := m.Login(ctx, "admin", "secret", "10.0.0.1")
	if err != nil || s.ID == "" || s.CSRFToken == "" || s.Username != "admin" {
		t.Fatalf("login: %v %+v", err, s)
	}
	if got, ok := m.Lookup(s.ID); !ok || got.CSRFToken != s.CSRFToken {
		t.Fatal("lookup should find session")
	}
	if m.ActiveSessions() != 1 {
		t.Fatalf("active sessions = %d", m.ActiveSessions())
	}
	m.Logout(ctx, s.ID)
	if _, ok := m.Lookup(s.ID); ok {
		t.Fatal("session should be gone after logout")
	}
	want := []string{"auth.login_failed", "auth.login_failed", "auth.login", "auth.logout"}
	if len(rec.types) != len(want) {
		t.Fatalf("events: %v", rec.types)
	}
	for i := range want {
		if rec.types[i] != want[i] {
			t.Fatalf("events: %v", rec.types)
		}
	}
}

func TestRateLimit(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := m.Login(ctx, "admin", "wrong", "10.0.0.2"); err != ErrInvalidCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := m.Login(ctx, "admin", "secret", "10.0.0.2"); err != ErrRateLimited {
		t.Fatalf("4th attempt should be rate limited, got %v", err)
	}
	if _, err := m.Login(ctx, "admin", "secret", "10.0.0.3"); err != nil {
		t.Fatalf("other ip should pass: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, err := m.Login(ctx, "admin", "secret", "10.0.0.2"); err != nil {
		t.Fatalf("after window should pass: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	m, _ := newManager(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	s, err := m.Login(context.Background(), "admin", "secret", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Second)
	if _, ok := m.Lookup(s.ID); ok {
		t.Fatal("expired session must not be found")
	}
	if m.ActiveSessions() != 0 {
		t.Fatal("expired sessions must be swept")
	}
}

func TestHashPassword(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must be rejected")
	}
	h, err := HashPassword("pw")
	if err != nil || len(h) != 60 || h[:4] != "$2a$" {
		t.Fatalf("hash: %v %q", err, h)
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte("pw")) != nil {
		t.Fatal("hash must verify")
	}
}

func TestMiddleware(t *testing.T) {
	m, _ := newManager(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := m.Middleware(DefaultPolicy())(ok)
	do := func(method, path string, cookie, csrf string) int {
		req := httptest.NewRequest(method, path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
		}
		if csrf != "" {
			req.Header.Set(CSRFHeader, csrf)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("GET", "/api/v1/system/health", "", ""); c != 204 {
		t.Fatalf("health must be public: %d", c)
	}
	if c := do("POST", "/api/v1/auth/login", "", ""); c != 204 {
		t.Fatalf("login must be public: %d", c)
	}
	if c := do("GET", "/", "", ""); c != 204 {
		t.Fatalf("static must be public: %d", c)
	}
	if c := do("GET", "/api/v1/devices", "", ""); c != 401 {
		t.Fatalf("no cookie: %d", c)
	}
	if c := do("GET", "/api/v1/devices", "bogus", ""); c != 401 {
		t.Fatalf("bad cookie: %d", c)
	}
	s, _ := m.Login(context.Background(), "admin", "secret", "1.1.1.1")
	if c := do("GET", "/api/v1/devices", s.ID, ""); c != 204 {
		t.Fatalf("valid cookie GET: %d", c)
	}
	if c := do("POST", "/api/v1/devices/rescan", s.ID, ""); c != 403 {
		t.Fatalf("POST without csrf: %d", c)
	}
	if c := do("POST", "/api/v1/devices/rescan", s.ID, "wrong"); c != 403 {
		t.Fatalf("POST wrong csrf: %d", c)
	}
	if c := do("DELETE", "/api/v1/test/faults/x", s.ID, s.CSRFToken); c != 204 {
		t.Fatalf("DELETE with csrf: %d", c)
	}

	// 未启用时全部放行
	off := New(Config{Enabled: false}, nil, nil).Middleware(DefaultPolicy())(ok)
	req := httptest.NewRequest("POST", "/api/v1/backups/start", nil)
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("disabled auth must pass through: %d", rec.Code)
	}
}

func TestCookie(t *testing.T) {
	m, _ := newManager(t)
	s, _ := m.Login(context.Background(), "admin", "secret", "1.1.1.1")
	rec := httptest.NewRecorder()
	m.SetCookie(rec, s)
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != s.ID || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Fatalf("cookie attrs: %+v", c)
	}
	rec = httptest.NewRecorder()
	ClearCookie(rec)
	if c := rec.Result().Cookies()[0]; c.MaxAge != -1 || c.Value != "" {
		t.Fatalf("clear cookie: %+v", c)
	}
	if RemoteIP(&http.Request{RemoteAddr: "192.168.1.5:5123"}) != "192.168.1.5" {
		t.Fatal("remote ip")
	}
}
