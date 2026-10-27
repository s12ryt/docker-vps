package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	cfg := loadConfig(os.Getenv)
	srv := newServer(cfg)

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", addr, err)
	}

	mode := "open (no password)"
	if cfg.Protected() {
		mode = "password-protected"
	}
	log.Printf("webshell listening on %s — mode: %s, shell: %s, session TTL: %s",
		addr, mode, cfg.ShellPath, cfg.SessionTTL)

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(httpSrv.Serve(ln))
}
