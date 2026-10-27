package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// server wires configuration, auth and routes together.
type server struct {
	cfg  config
	auth *auth

	staticFS fs.FS // /static assets (xterm etc.)
}

func newServer(cfg config) *server {
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic("embedded web/static missing: " + err.Error())
	}
	return &server{
		cfg:      cfg,
		auth:     newAuth(cfg.Password, newSessionStore(cfg.SessionTTL)),
		staticFS: staticFS,
	}
}

// routes builds the full http handler.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// public endpoints — never require a session
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

	// login page: in open mode it is pointless, bounce to /
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Protected() {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		serveEmbed(w, "web/login.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("POST /login", s.auth.handleLogin)
	mux.HandleFunc("GET /logout", s.auth.handleLogout)

	// protected endpoints
	mux.Handle("GET /ws", s.auth.requireSession(s.handleWS))
	mux.HandleFunc("GET /", s.auth.requireSession(func(w http.ResponseWriter, r *http.Request) {
		serveEmbed(w, "web/index.html", "text/html; charset=utf-8")
	}))

	return mux
}

func serveEmbed(w http.ResponseWriter, name, contentType string) {
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
