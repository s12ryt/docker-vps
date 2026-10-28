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

	// /data persistence layer: explicit DATA_ROOT wins, otherwise auto-detect
	// a mounted /data (common on docker cloud hosts). Disabled when neither.
	cfg.DataRoot = resolveDataRoot(os.Getenv, func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	})
	if cfg.DataRoot != "" {
		if err := initDataRoot(cfg.DataRoot); err != nil {
			// fatal on purpose: silently degrading would make users believe
			// their files persist while everything lands in the container layer
			log.Fatalf("cannot initialize translate-layer data root under %s: %v", cfg.DataRoot, err)
		}
		log.Printf("translate layer active: libc path rewriting sends /usr /var /etc /root /home /opt /srv (and usr-merge links) to %s/rootfs — container root stays real, all writes land on the volume", cfg.DataRoot)
	}

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
