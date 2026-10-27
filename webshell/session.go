package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessionStore keeps in-memory login sessions with a fixed TTL.
// Sessions die with the process, which is the desired security property
// for an ephemeral container: a restart logs everyone out.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	ttl      time.Duration
	now      func() time.Time // injectable clock for tests
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
		now:      time.Now,
	}
}

// create mints a new random session token and returns it.
func (s *sessionStore) create() string {
	buf := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is unrecoverable; an empty token never validates.
		return ""
	}
	tok := hex.EncodeToString(buf)

	s.mu.Lock()
	s.sessions[tok] = s.now().Add(s.ttl)
	s.mu.Unlock()
	return tok
}

// valid reports whether the token maps to a live session.
// Expired entries are purged along the way.
func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	expiry, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if s.now().After(expiry) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

// revoke removes a session immediately (logout).
func (s *sessionStore) revoke(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

// count returns the number of live sessions (used by /healthz diagnostics).
func (s *sessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	now := s.now()
	for tok, expiry := range s.sessions {
		if now.After(expiry) {
			delete(s.sessions, tok)
			continue
		}
		n++
	}
	return n
}
