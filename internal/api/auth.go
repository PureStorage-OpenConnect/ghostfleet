package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie   = "ghostfleet_session"
	sessionLifetime = 24 * time.Hour
)

// sessionStore holds in-memory session tokens. Sessions don't survive a
// controller restart, which is acceptable for a single-operator lab tool.
type sessionStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time // token -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: make(map[string]time.Time)}
}

func (ss *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for t, exp := range ss.tokens { // opportunistic cleanup
		if time.Now().After(exp) {
			delete(ss.tokens, t)
		}
	}
	ss.tokens[token] = time.Now().Add(sessionLifetime)
	return token
}

func (ss *sessionStore) valid(token string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	exp, ok := ss.tokens[token]
	return ok && time.Now().Before(exp)
}

func (ss *sessionStore) revoke(token string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.tokens, token)
}

// requireAuth gates the API when a password and/or API keys are configured
// (UI-1). With neither, access is open by design. A request passes with a
// valid session cookie (UI login) or a valid static API key (automation).
func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authRequired() || s.authenticated(r) || s.hasValidAPIKey(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "authentication required")
	})
}

// authRequired reports whether any auth is configured at all.
func (s *server) authRequired() bool {
	return s.cfg.Password != "" || len(s.cfg.APIKeys) > 0
}

func (s *server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.sessions.valid(c.Value)
}

// hasValidAPIKey checks for a static API key, looked for (in order) in the
// Authorization: Bearer header, the X-API-Key header, then the apikey query
// parameter. The query form is convenient but leaks into logs/history — the
// header forms are preferred.
func (s *server) hasValidAPIKey(r *http.Request) bool {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		if b := r.Header.Get("Authorization"); strings.HasPrefix(b, "Bearer ") {
			key = strings.TrimSpace(b[len("Bearer "):])
		}
	}
	if key == "" {
		key = r.URL.Query().Get("apikey")
	}
	if key == "" {
		return false
	}
	for _, k := range s.cfg.APIKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
			return true
		}
	}
	return false
}

// handleSessionStatus tells the UI whether a password is required and
// whether the caller is already authenticated.
func (s *server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"passwordRequired": s.cfg.Password != "",
		"authenticated":    !s.authRequired() || s.authenticated(r) || s.hasValidAPIKey(r),
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Password == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.Password)) != 1 {
		time.Sleep(500 * time.Millisecond) // slow down brute force
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessions.create(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}
