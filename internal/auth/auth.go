// Package auth holds password hashing, sessions, CSRF tokens and the login rate limit.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the bcrypt work factor.
var BcryptCost = 12

// HashPassword returns a bcrypt hash.
func HashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", errors.New("password must have at least 8 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	return string(b), err
}

// CheckPassword compares a password with its hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash is used so that unknown users take as long as known ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.MinCost)

// BurnTime spends bcrypt time for an unknown user.
func BurnTime() { _ = bcrypt.CompareHashAndPassword(dummyHash, []byte("x")) }

// RandomToken returns n random bytes as URL-safe base64.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// Session is one login session.
type Session struct {
	TokenHash string
	UserRef   string // "main:<id>" or "site:<id>"
	Scope     string // "main" or a site key
	CSRF      string
	ExpiresAt int64
}

// UserKind returns "main" or "site"; UserID the number part of UserRef.
func (s *Session) UserKind() string { k, _, _ := strings.Cut(s.UserRef, ":"); return k }

// UserID returns the id part of UserRef.
func (s *Session) UserID() int64 {
	_, v, _ := strings.Cut(s.UserRef, ":")
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Manager stores sessions in main.db.
type Manager struct {
	DB     *sql.DB
	Secure bool // Secure flag on cookies
	TTL    time.Duration
	secret []byte
}

// NewManager creates a session manager. secretFile holds a random key for
// stateless CSRF tokens of public module forms (created when missing).
func NewManager(db *sql.DB, secure bool, secretFile string) (*Manager, error) {
	m := &Manager{DB: db, Secure: secure, TTL: 30 * 24 * time.Hour}
	b, err := os.ReadFile(secretFile)
	if err != nil || len(b) < 32 {
		b = []byte(RandomToken(32))
		if err := os.WriteFile(secretFile, b, 0o600); err != nil {
			return nil, err
		}
	}
	m.secret = b
	return m, nil
}

// CookieName gives the session cookie name for a scope.
func CookieName(scope string) string {
	if scope == "main" {
		return "hk_main"
	}
	return "hk_site_" + scope
}

// Create stores a new session and sets its cookie on path.
func (m *Manager) Create(w http.ResponseWriter, scope, userRef, cookiePath string) (*Session, error) {
	token := RandomToken(32)
	s := &Session{TokenHash: hashToken(token), UserRef: userRef, Scope: scope, CSRF: RandomToken(24),
		ExpiresAt: time.Now().Add(m.TTL).Unix()}
	if _, err := m.DB.Exec("INSERT INTO sessions (token_hash, user_ref, scope, csrf, expires_at) VALUES (?, ?, ?, ?, ?)",
		s.TokenHash, s.UserRef, s.Scope, s.CSRF, s.ExpiresAt); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName(scope), Value: token, Path: cookiePath, HttpOnly: true,
		Secure: m.Secure, SameSite: http.SameSiteLaxMode, Expires: time.Unix(s.ExpiresAt, 0)})
	return s, nil
}

// Get returns the session of the request for scope, or nil. A session made for
// another scope (another site) is never returned.
func (m *Manager) Get(r *http.Request, scope string) *Session {
	c, err := r.Cookie(CookieName(scope))
	if err != nil || c.Value == "" {
		return nil
	}
	return m.ByToken(c.Value, scope)
}

// ByToken looks up a raw token for scope.
func (m *Manager) ByToken(token, scope string) *Session {
	s := &Session{}
	err := m.DB.QueryRow("SELECT token_hash, user_ref, scope, csrf, expires_at FROM sessions WHERE token_hash = ?", hashToken(token)).
		Scan(&s.TokenHash, &s.UserRef, &s.Scope, &s.CSRF, &s.ExpiresAt)
	if err != nil || s.Scope != scope || s.ExpiresAt < time.Now().Unix() {
		return nil
	}
	return s
}

// Destroy removes the session of the request and clears the cookie.
func (m *Manager) Destroy(w http.ResponseWriter, r *http.Request, scope, cookiePath string) {
	if c, err := r.Cookie(CookieName(scope)); err == nil {
		_, _ = m.DB.Exec("DELETE FROM sessions WHERE token_hash = ?", hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName(scope), Value: "", Path: cookiePath, MaxAge: -1, HttpOnly: true,
		Secure: m.Secure, SameSite: http.SameSiteLaxMode})
}

// DestroyUser removes all sessions of a user in a scope (after a password change or delete).
func (m *Manager) DestroyUser(scope, userRef string) {
	_, _ = m.DB.Exec("DELETE FROM sessions WHERE scope = ? AND user_ref = ?", scope, userRef)
}

// HandOver creates a one-time token (60 s) that lets a main admin open a site admin
// on another domain. The site admin consumes it with TakeHandOver.
func (m *Manager) HandOver(siteKey, userRef string) (string, error) {
	token := RandomToken(32)
	_, err := m.DB.Exec("INSERT INTO sessions (token_hash, user_ref, scope, csrf, expires_at) VALUES (?, ?, ?, '', ?)",
		hashToken(token), userRef, "open:"+siteKey, time.Now().Add(60*time.Second).Unix())
	return token, err
}

// TakeHandOver returns the user_ref of a valid hand-over token and deletes it.
func (m *Manager) TakeHandOver(siteKey, token string) (string, bool) {
	s := m.ByToken(token, "open:"+siteKey)
	if s == nil {
		return "", false
	}
	_, _ = m.DB.Exec("DELETE FROM sessions WHERE token_hash = ?", s.TokenHash)
	return s.UserRef, true
}

// CheckCSRF compares the form field "_csrf" (or header X-CSRF-Token) with the session token.
func CheckCSRF(r *http.Request, s *Session) bool {
	if s == nil || s.CSRF == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.FormValue("_csrf")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRF)) == 1
}

// PublicCSRF makes a stateless token for public module forms of a site:
// "<unix time>.<hmac>". Visitors have no session, so the token is signed.
func (m *Manager) PublicCSRF(siteKey string) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return ts + "." + m.sign(siteKey, ts)
}

// CheckPublicCSRF validates a PublicCSRF token (max age 30 days).
func (m *Manager) CheckPublicCSRF(siteKey, token string) bool {
	ts, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)) > 30*24*time.Hour {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(m.sign(siteKey, ts)))
}

func (m *Manager) sign(siteKey, ts string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(siteKey + "|" + ts))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}

// ---------- rate limit ----------

// Limiter counts failures per key (IP) in a sliding window. Kept in memory.
type Limiter struct {
	Max    int
	Window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

// NewLimiter makes a limiter: max failures per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{Max: max, Window: window, hits: map[string][]time.Time{}}
}

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	list := l.hits[key]
	i := 0
	for i < len(list) && now.Sub(list[i]) > l.Window {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = list
	}
	return list
}

// Allow reports whether key is below the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, time.Now())) < l.Max
}

// Add records one failure (or one use) for key.
func (l *Limiter) Add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.hits[key] = append(l.prune(key, now), now)
	if len(l.hits) > 10000 { // keep memory bounded
		for k := range l.hits {
			l.prune(k, now)
		}
	}
}

// Reset clears key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// ClientIP returns the visitor IP. When the direct peer is a loopback proxy
// (Nginx/Apache on the same box), the last X-Forwarded-For entry is used: that
// entry is written by our own proxy, so a client cannot fake it.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
		if x := r.Header.Get("X-Real-IP"); x != "" {
			return x
		}
	}
	return host
}
