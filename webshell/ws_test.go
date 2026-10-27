//go:build !windows

package main

import (
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialWS(t *testing.T, httpURL, path string, cookies []*http.Cookie) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u, _ := url.Parse(httpURL) // http(s) scheme — cookie jars only speak http(s)
	u.Path = path

	jar, _ := cookiejar.New(nil)
	if len(cookies) > 0 {
		jar.SetCookies(u, cookies)
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		Jar:              jar,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}

	// gorilla's DialContext internally rewrites ws->http before consulting the jar
	dialURL := *u
	switch dialURL.Scheme {
	case "https":
		dialURL.Scheme = "wss"
	default:
		dialURL.Scheme = "ws"
	}
	return dialer.Dial(dialURL.String(), nil)
}

func readUntil(t *testing.T, ws *websocket.Conn, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var acc strings.Builder

	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read error while waiting for %q: %v (got so far: %q)", needle, err, acc.String())
		}
		acc.Write(data)
		if strings.Contains(acc.String(), needle) {
			return acc.String()
		}
	}
	t.Fatalf("timed out waiting for %q, got: %q", needle, acc.String())
	return ""
}

// TestWSSpawnShellEcho proves the full pipeline in open mode:
// upgrade -> PTY spawn -> interactive echo round-trip.
func TestWSSpawnShellEcho(t *testing.T) {
	cfg := loadConfig(func(string) string { return "" }) // open mode, /bin/bash
	s := newServer(cfg)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	ws, _, err := dialWS(t, ts.URL, "/ws", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer ws.Close()

	// wait for a shell prompt (bash prints something on start)
	_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	marker := "WS_TDD_OK_7f3a"
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("echo "+marker+"\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	out := readUntil(t, ws, marker, 8*time.Second)
	if !strings.Contains(out, marker) {
		t.Fatalf("echo marker missing in output: %q", out)
	}
}

// TestWSResizeControlMessageAccepted sends a resize control frame;
// it must be consumed silently without corrupting the terminal stream.
func TestWSResizeControlMessageAccepted(t *testing.T) {
	cfg := loadConfig(func(string) string { return "" })
	s := newServer(cfg)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	ws, _, err := dialWS(t, ts.URL, "/ws", nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer ws.Close()

	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"action":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("send resize failed: %v", err)
	}

	marker := "WS_RESIZE_OK_91c"
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("echo "+marker+"\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	readUntil(t, ws, marker, 8*time.Second) // still functional after resize
}

// TestWSProtectedRequiresSession: in protected mode a cookie-less websocket
// dial must be bounced to /login (HTTP redirect), and a logged-in cookie works.
func TestWSProtectedRequiresSession(t *testing.T) {
	cfg := loadConfig(func(k string) string {
		if k == "PW" {
			return "secret"
		}
		return ""
	})
	s := newServer(cfg)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	// anonymous dial -> expect redirect, not an upgrade
	_, resp, err := dialWS(t, ts.URL, "/ws", nil)
	if err == nil {
		t.Fatal("anonymous websocket dial must fail")
	}
	if resp == nil || (resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound) {
		t.Fatalf("expected 302/303 redirect to login, got %v", resp)
	}
	if resp.Header.Get("Location") != "/login" {
		t.Errorf("Location = %q, want /login", resp.Header.Get("Location"))
	}

	// log in and dial again with the session cookie
	httpURL := ts.URL
	u, _ := url.Parse(httpURL)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		// stop at the first response so we can assert the 303 itself
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	form := strings.NewReader("password=secret")
	loginURL := httpURL + "/login"
	req, _ := http.NewRequest(http.MethodPost, loginURL, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	respLogin, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer respLogin.Body.Close()
	if respLogin.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", respLogin.StatusCode)
	}

	ws, _, err := dialWS(t, httpURL, "/ws", jar.Cookies(u))
	if err != nil {
		t.Fatalf("dial with session cookie failed: %v", err)
	}
	defer ws.Close()

	marker := "WS_AUTH_OK_4b2"
	_ = ws.WriteMessage(websocket.BinaryMessage, []byte("echo "+marker+"\n"))
	readUntil(t, ws, marker, 8*time.Second)
}
