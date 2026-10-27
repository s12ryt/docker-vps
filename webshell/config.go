package main

import (
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
}

// Protected reports whether the web shell requires a password to access.
func (c config) Protected() bool { return c.Password != "" }

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
