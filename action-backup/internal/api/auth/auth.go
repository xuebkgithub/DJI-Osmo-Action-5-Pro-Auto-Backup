// Package auth 提供用户名密码登录、内存会话表、CSRF Token 与登录限速。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"action-backup/internal/events"
)

// CookieName 会话 Cookie 名。
const CookieName = "ab_session"

// CSRFHeader 写操作必须携带的请求头。
const CSRFHeader = "X-CSRF-Token"

// BcryptCost 口令哈希成本。
const BcryptCost = 12

// 错误。
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many login attempts, try again later")
	ErrNoSession          = errors.New("no valid session")
	ErrCSRF               = errors.New("missing or invalid CSRF token")
)

// Config 认证配置。
type Config struct {
	Enabled              bool
	Username             string
	PasswordHash         string
	SessionTTL           time.Duration
	LoginRateLimitPerMin int
}

// Session 一次登录会话。
type Session struct {
	ID        string    `json:"-"`
	Username  string    `json:"username"`
	CSRFToken string    `json:"csrf_token"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	RemoteIP  string    `json:"-"`
}

// Recorder 记录登录失败等安全事件；可为 nil。
type Recorder interface {
	Publish(ctx context.Context, e events.Event)
}

// Manager 会话管理器。
type Manager struct {
	cfg      Config
	rec      Recorder
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]*Session
	attempts map[string][]time.Time
}

// New 创建管理器。
func New(cfg Config, rec Recorder, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.LoginRateLimitPerMin <= 0 {
		cfg.LoginRateLimitPerMin = 5
	}
	return &Manager{cfg: cfg, rec: rec, log: log, now: time.Now, sessions: map[string]*Session{}, attempts: map[string][]time.Time{}}
}

// Enabled 认证是否启用。
func (m *Manager) Enabled() bool { return m.cfg.Enabled }

// Username 配置的用户名。
func (m *Manager) Username() string { return m.cfg.Username }

// HashPassword 生成 bcrypt 哈希。
func HashPassword(password string) (string, error) {
	if len(password) == 0 {
		return "", errors.New("password must not be empty")
	}
	if len(password) > 72 {
		return "", errors.New("password must be at most 72 bytes")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// Login 校验用户名口令并签发会话。ip 用于限速；失败记入事件。
func (m *Manager) Login(ctx context.Context, username, password, ip string) (*Session, error) {
	if !m.allowAttempt(ip) {
		m.record(ctx, "warn", "auth.login_rate_limited", "login rate limit exceeded", map[string]any{"ip": ip})
		return nil, ErrRateLimited
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(m.cfg.Username)) == 1
	// 用户名不匹配时仍执行一次 bcrypt 比较，避免时序差异泄露用户名。
	hash := m.cfg.PasswordHash
	if !userOK || hash == "" {
		hash = dummyHash
	}
	passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	if !userOK || !passOK {
		m.record(ctx, "warn", "auth.login_failed", "login failed", map[string]any{"ip": ip, "username": truncate(username, 64)})
		return nil, ErrInvalidCredentials
	}
	s, err := m.create(username, ip)
	if err != nil {
		return nil, err
	}
	m.record(ctx, "info", "auth.login", "login succeeded", map[string]any{"ip": ip, "username": username})
	return s, nil
}

// dummyHash 用于用户名不存在时的等时比较（口令为随机值，永不匹配）。
var dummyHash = func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("action-backup-dummy"), bcrypt.MinCost)
	return string(h)
}()

func (m *Manager) create(username, ip string) (*Session, error) {
	id, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := m.now()
	s := &Session{ID: id, Username: username, CSRFToken: csrf, CreatedAt: now, ExpiresAt: now.Add(m.cfg.SessionTTL), RemoteIP: ip}
	m.mu.Lock()
	m.sweepLocked(now)
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// Lookup 查找未过期的会话。
func (m *Manager) Lookup(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if !m.now().Before(s.ExpiresAt) {
		delete(m.sessions, id)
		return nil, false
	}
	cp := *s
	return &cp, true
}

// Logout 注销会话。
func (m *Manager) Logout(ctx context.Context, id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok {
		m.record(ctx, "info", "auth.logout", "logout", map[string]any{"username": s.Username})
	}
}

// ActiveSessions 当前有效会话数。
func (m *Manager) ActiveSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(m.now())
	return len(m.sessions)
}

func (m *Manager) sweepLocked(now time.Time) {
	for id, s := range m.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(m.sessions, id)
		}
	}
}

func (m *Manager) allowAttempt(ip string) bool {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	window := now.Add(-time.Minute)
	kept := m.attempts[ip][:0]
	for _, t := range m.attempts[ip] {
		if t.After(window) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= m.cfg.LoginRateLimitPerMin {
		m.attempts[ip] = kept
		return false
	}
	m.attempts[ip] = append(kept, now)
	// 避免 map 无限增长：清理空闲 IP
	for k, v := range m.attempts {
		if k != ip && (len(v) == 0 || !v[len(v)-1].After(window)) {
			delete(m.attempts, k)
		}
	}
	return true
}

func (m *Manager) record(ctx context.Context, level, typ, msg string, data map[string]any) {
	if m.rec == nil {
		return
	}
	m.rec.Publish(ctx, events.Event{Level: level, Type: typ, Message: msg, Data: data})
}

// SessionFromRequest 从 Cookie 取会话。
func (m *Manager) SessionFromRequest(r *http.Request) (*Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}
	return m.Lookup(c.Value)
}

// SetCookie 写会话 Cookie：HttpOnly、SameSite=Strict、Path=/。局域网 HTTP 部署不设 Secure。
func (m *Manager) SetCookie(w http.ResponseWriter, s *Session) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: s.ID, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: s.ExpiresAt, MaxAge: int(time.Until(s.ExpiresAt).Seconds()),
	})
}

// ClearCookie 清除会话 Cookie。
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// CheckCSRF 校验写请求的 CSRF Token。
func CheckCSRF(r *http.Request, s *Session) bool {
	tok := r.Header.Get(CSRFHeader)
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.CSRFToken)) == 1
}

// RemoteIP 取请求来源 IP（不信任代理头）。
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
