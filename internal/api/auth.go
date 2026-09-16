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
	s.setSessionCookie(w, r, s.sessions.create(), int(sessionLifetime.Seconds()))
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(c.Value)
	}
	s.setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// setSessionCookie writes (maxAge > 0) or clears (maxAge < 0) the session
// cookie. Login and logout share it so both carry the same attributes.
func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// secureCookie decides the cookie's Secure attribute per Config.SecureCookies.
// In auto mode a cookie becomes Secure when the request itself came in over
// TLS or a reverse proxy in front of the controller reports the client
// connection as https via X-Forwarded-Proto. Trusting that header is safe
// here: a client forging it can only make its own cookie stricter.
func (s *server) secureCookie(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(s.cfg.SecureCookies)) {
	case "on", "true", "yes", "1":
		return true
	case "off", "false", "no", "0":
		return false
	}
	if r.TLS != nil {
		return true
	}
	// Proxies chain the header as "https, http"; the first hop is the client's.
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
