package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestAuth(password string) *auth {
	a := newAuth(password, newSessionStore(time.Hour))
	return a
}

func loginRequest(t *testing.T, password string) *http.Request {
	t.Helper()
	form := url.Values{"password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:1234"
	return req
}

func TestLoginCorrectPasswordIssuesSessionCookie(t *testing.T) {
	a := newTestAuth("secret")
	rec := httptest.NewRecorder()

	a.handleLogin(rec, loginRequest(t, "secret"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 See Other", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("redirect Location = %q, want %q", loc, "/")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].Value == "" {
		t.Fatalf("expected one %s cookie with value, got %+v", sessionCookie, cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
}

func TestLoginWrongPasswordRejected(t *testing.T) {
	a := newTestAuth("secret")
	rec := httptest.NewRecorder()

	a.handleLogin(rec, loginRequest(t, "wrong"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if c := rec.Result().Cookies(); len(c) != 0 {
		t.Errorf("failed login must not set cookies, got %+v", c)
	}
}

func TestLoginMissingFormFieldRejected(t *testing.T) {
	a := newTestAuth("secret")
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("notpassword=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.2:1234"
	a.handleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireSessionOpenModeAllowsAll(t *testing.T) {
	a := newTestAuth("") // no password
	called := false
	h := a.requireSession(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Error("open mode must call the wrapped handler without a session")
	}
}

func TestRequireSessionProtectedRedirectsAnonymous(t *testing.T) {
	a := newTestAuth("secret")
	called := false
	h := a.requireSession(func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if called {
		t.Error("protected mode must not call handler without a session")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func loginAndGetCookie(t *testing.T, a *auth) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	a.handleLogin(rec, loginRequest(t, a.password))
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected session cookie, got %+v", cookies)
	}
	return cookies[0]
}

func TestRequireSessionProtectedAllowsValidCookie(t *testing.T) {
	a := newTestAuth("secret")
	cookie := loginAndGetCookie(t, a)

	called := false
	h := a.requireSession(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Errorf("valid session must grant access, called=%v code=%d", called, rec.Code)
	}
}

func TestRequireSessionProtectedRejectsForgedCookie(t *testing.T) {
	a := newTestAuth("secret")
	called := false
	h := a.requireSession(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "deadbeefdeadbeef"})
	rec := httptest.NewRecorder()
	h(rec, req)

	if called {
		t.Error("forged cookie must not grant access")
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("forged cookie should redirect to /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestBruteForceLockout(t *testing.T) {
	a := newTestAuth("secret")

	// injectable clock
	base := time.Now()
	a.now = func() time.Time { return base }

	// 5 wrong attempts reach the threshold
	for i := 0; i < maxFailures; i++ {
		rec := httptest.NewRecorder()
		a.handleLogin(rec, loginRequest(t, "nope"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}

	// locked out now — even the CORRECT password must be refused while locked
	rec := httptest.NewRecorder()
	a.handleLogin(rec, loginRequest(t, "secret"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login: status = %d, want 429", rec.Code)
	}

	// after the lockout window passes, login works again
	base = base.Add(lockoutWindow + time.Second)
	rec = httptest.NewRecorder()
	a.handleLogin(rec, loginRequest(t, "secret"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("after lockout expires: status = %d, want 303", rec.Code)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	a := newTestAuth("secret")
	cookie := loginAndGetCookie(t, a)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(cookie)
	a.handleLogout(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout: status=%d location=%q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}

	// old cookie must no longer grant access
	called := false
	h := a.requireSession(func(w http.ResponseWriter, r *http.Request) { called = true })
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	h(httptest.NewRecorder(), req2)
	if called {
		t.Error("session must be invalid after logout")
	}
}
