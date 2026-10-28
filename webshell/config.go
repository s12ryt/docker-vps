package main

import (
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort  = 7681
	defaultShell = "/bin/bash"
	defaultTTL   = 12 * time.Hour
)

// config holds runtime configuration parsed from environment variables.
type config struct {
	Port        int           // TCP port the web shell listens on
	Password    string        // login password; empty means open (no password) mode
	ShellPath   string        // shell executable spawned inside the PTY
	SessionTTL  time.Duration // how long a login session stays valid
	DataRoot    string        // backing dir for the persistent rootfs layer; empty = disabled
}

// Protected reports whether the web shell requires a password to access.
func (c config) Protected() bool { return c.Password != "" }

// PersistRoot returns the on-disk location of the persistent layer
// (symlink farm backing /usr /var /root /home /opt /etc), or "" when the
// /data persistence layer is disabled.
// Uses path.Join (not filepath): the result is used as a POSIX symlink
// target and must never pick up the host OS separator.
func (c config) PersistRoot() string {
	if c.DataRoot == "" {
		return ""
	}
	return path.Join(c.DataRoot, "rootfs")
}

// resolveDataRoot decides where the persistent rootfs lives.
//   - DATA_ROOT set (non-blank) -> that exact path (explicit opt-in)
//   - DATA_ROOT unset/blank + /data exists -> /data (cloud-host auto-detect)
//   - DATA_ROOT unset/blank + no /data    -> "" (layer disabled, legacy behavior)
func resolveDataRoot(getenv func(string) string, dirExists func(string) bool) string {
	if v := strings.TrimSpace(getenv("DATA_ROOT")); v != "" {
		return v
	}
	if dirExists("/data") {
		return "/data"
	}
	return ""
}

// loadConfig builds a config from the given environment getter.
// It is tolerant: invalid values fall back to safe defaults.
func loadConfig(getenv func(string) string) config {
	cfg := config{
		Port:       defaultPort,
		ShellPath:  defaultShell,
		SessionTTL: defaultTTL,
	}

	if raw := strings.TrimSpace(getenv("PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p >= 1 && p <= 65535 {
			cfg.Port = p
		}
	}

	// Password is taken as-is (may contain inner spaces), but an all-blank
	// value is treated as "not set" so users cannot accidentally lock
	// themselves behind an invisible password.
	if pw := getenv("PW"); strings.TrimSpace(pw) != "" {
		cfg.Password = pw
	}

	if sh := strings.TrimSpace(getenv("SHELL")); sh != "" {
		cfg.ShellPath = sh
	}

	if ttl := strings.TrimSpace(getenv("SESSION_TTL")); ttl != "" {
		if d, err := time.ParseDuration(ttl); err == nil && d > 0 {
			cfg.SessionTTL = d
		}
	}

	return cfg
}
