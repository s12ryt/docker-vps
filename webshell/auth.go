package main

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie = "webshell_session"
	maxFailures   = 5
	lockoutWindow = 30 * time.Second
)

// auth implements password login, session enforcement and a simple
// per-client brute-force lockout.
type auth struct {
	password string // empty = open mode (everything allowed)
	sessions *sessionStore

	mu          sync.Mutex
	failures    map[string]int       // client -> consecutive failures
	lockedUntil map[string]time.Time // client -> lock expiry
	now         func() time.Time     // injectable clock for tests
}

func newAuth(password string, sessions *sessionStore) *auth {
	return &auth{
		password:    password,
		sessions:    sessions,
		failures:    make(map[string]int),
		lockedUntil: make(map[string]time.Time),
		now:         time.Now,
	}
}

// handleLogin validates a form POST ("password" field) and, on success,
// issues a session cookie and redirects to "/".
func (a *auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := r.RemoteAddr

	if a.isLocked(key) {
		http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pw := r.FormValue("password")
	// constant-time comparison to avoid leaking the password byte-by-byte
	ok := len(pw) == len(a.password) &&
		subtle.ConstantTimeCompare([]byte(pw), []byte(a.password)) == 1

	if !ok {
		a.recordFailure(key)
		http.Error(w, "wrong password", http.StatusUnauthorized)
		return
	}

	a.resetFailures(key)

	tok := a.sessions.create()
	if tok == "" {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout revokes the caller's session.
func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.revoke(c.Value)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// requireSession wraps a handler so it is only reachable in open mode or
// with a valid session cookie. Otherwise the client is sent to /login.
func (a *auth) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.password == "" {
			next(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && a.sessions.valid(c.Value) {
			next(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func (a *auth) isLocked(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	until, ok := a.lockedUntil[key]
	if !ok {
		return false
	}
	if a.now().After(until) {
		// lock expired — clean up and let the caller try again
		delete(a.lockedUntil, key)
		delete(a.failures, key)
		return false
	}
	return true
}

func (a *auth) recordFailure(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failures[key]++
	if a.failures[key] >= maxFailures {
		a.lockedUntil[key] = a.now().Add(lockoutWindow)
		a.failures[key] = 0
	}
}

func (a *auth) resetFailures(key string) {
	a.mu.Lock()
	delete(a.failures, key)
	delete(a.lockedUntil, key)
	a.mu.Unlock()
}
