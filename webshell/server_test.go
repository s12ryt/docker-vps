package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, password string) (*server, http.Handler) {
	t.Helper()
	cfg := loadConfig(func(k string) string {
		if k == "PW" && password != "" {
			return password
		}
		return ""
	})
	s := newServer(cfg)
	return s, s.routes()
}

func TestHealthzAlwaysOpen(t *testing.T) {
	for _, pw := range []string{"", "secret"} {
		_, h := newTestServer(t, pw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("pw=%q: /healthz status = %d, want 200", pw, rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Errorf("pw=%q: /healthz body = %q, want %q", pw, rec.Body.String(), "ok")
		}
	}
}

func TestIndexOpenModeServed(t *testing.T) {
	_, h := newTestServer(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-page="terminal"`) {
		t.Error("index page must contain data-page=\"terminal\" marker")
	}
}

func TestIndexProtectedRedirectsToLogin(t *testing.T) {
	_, h := newTestServer(t, "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous / in protected mode: %d %q, want 303 /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

func TestIndexProtectedServedWithSession(t *testing.T) {
	_, h := newTestServer(t, "secret")

	// log in first
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.1.1.1:1"
	h.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `data-page="terminal"`) {
		t.Fatalf("with session: status=%d, want 200 terminal page", rec2.Code)
	}
}

func TestLoginPageRoute(t *testing.T) {
	// open mode: /login bounces to /
	_, h := newTestServer(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("open mode /login: %d %q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}

	// protected mode: /login serves the form
	_, h2 := newTestServer(t, "secret")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("protected /login status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "<form") {
		t.Error("protected /login must contain a <form>")
	}
	if !strings.Contains(rec2.Body.String(), `data-page="login"`) {
		t.Error("login page must contain data-page=\"login\" marker")
	}
}

func TestStaticAssetsServedWithoutSession(t *testing.T) {
	// xterm assets must be reachable without a session (the login page styles depend on them)
	_, h := newTestServer(t, "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/xterm.min.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/static/xterm.min.js status = %d, want 200 (no session required)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("content-type = %q, want javascript", ct)
	}

	recCss := httptest.NewRecorder()
		h.ServeHTTP(recCss, httptest.NewRequest(http.MethodGet, "/static/xterm.min.css", nil))
	if recCss.Code != http.StatusOK {
		t.Errorf("css status = %d, want 200", recCss.Code)
	}
}

func TestWSRouteRequiresSession(t *testing.T) {
	// anonymous websocket upgrade attempt in protected mode must bounce to /login,
	// not attempt the upgrade (which would yield 400)
	_, h := newTestServer(t, "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous /ws: %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestHealthzBodySmall(t *testing.T) {
	// keep-alive friendly tiny payload
	_, h := newTestServer(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	b, _ := io.ReadAll(rec.Result().Body)
	if len(b) > 64 {
		t.Errorf("healthz payload too large: %d bytes", len(b))
	}
}

var _ = time.Hour // keep time import when tests are trimmed
