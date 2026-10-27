package main

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCreateAndValidate(t *testing.T) {
	s := newSessionStore(time.Hour)
	tok := s.create()

	if tok == "" {
		t.Fatal("create() returned empty token")
	}
	if !s.valid(tok) {
		t.Error("freshly created token should be valid")
	}
}

func TestSessionRejectsUnknownToken(t *testing.T) {
	s := newSessionStore(time.Hour)
	if s.valid("bogus-token") {
		t.Error("unknown token must not validate")
	}
	if s.valid("") {
		t.Error("empty token must not validate")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newSessionStore(40 * time.Millisecond)
	tok := s.create()

	time.Sleep(70 * time.Millisecond)

	if s.valid(tok) {
		t.Error("expired session must not validate")
	}
}

func TestSessionRevoke(t *testing.T) {
	s := newSessionStore(time.Hour)
	tok := s.create()

	s.revoke(tok)

	if s.valid(tok) {
		t.Error("revoked session must not validate")
	}
}

func TestSessionTokensUniqueAndLong(t *testing.T) {
	s := newSessionStore(time.Hour)
	seen := make(map[string]bool)

	for i := 0; i < 500; i++ {
		tok := s.create()
		if len(tok) < 32 {
			t.Fatalf("token too short to be secure: %q (len %d)", tok, len(tok))
		}
		if !isHex(tok) {
			t.Fatalf("token %q is not hex-encoded", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = true
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return len(s) > 0
}
