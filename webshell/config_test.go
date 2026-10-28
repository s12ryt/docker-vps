package main

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg := loadConfig(func(string) string { return "" })

	if cfg.Port != 7681 {
		t.Errorf("default port = %d, want 7681", cfg.Port)
	}
	if cfg.Password != "" {
		t.Errorf("default password = %q, want empty", cfg.Password)
	}
	if cfg.Protected() {
		t.Error("no PW env means open mode, Protected() = true, want false")
	}
	if cfg.ShellPath != "/bin/bash" {
		t.Errorf("default shell = %q, want /bin/bash", cfg.ShellPath)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Errorf("default session TTL = %v, want 12h", cfg.SessionTTL)
	}
}

func TestLoadConfigCustomValues(t *testing.T) {
	env := map[string]string{
		"PORT":  "9000",
		"PW":    "secret",
		"SHELL": "/usr/bin/zsh",
	}
	cfg := loadConfig(func(k string) string { return env[k] })

	if cfg.Port != 9000 {
		t.Errorf("port = %d, want 9000", cfg.Port)
	}
	if cfg.Password != "secret" {
		t.Errorf("password = %q, want %q", cfg.Password, "secret")
	}
	if !cfg.Protected() {
		t.Error("PW set means protected mode, Protected() = false, want true")
	}
	if cfg.ShellPath != "/usr/bin/zsh" {
		t.Errorf("shell = %q, want /usr/bin/zsh", cfg.ShellPath)
	}
}

func TestLoadConfigInvalidPortFallsBack(t *testing.T) {
	cases := []string{"not-a-number", "0", "-1", "65536", "99999"}
	for _, c := range cases {
		cfg := loadConfig(func(k string) string {
			if k == "PORT" {
				return c
			}
			return ""
		})
		if cfg.Port != 7681 {
			t.Errorf("invalid port %q parsed to %d, want fallback 7681", c, cfg.Port)
		}
	}
}

func TestLoadConfigBlankPWMeansOpenMode(t *testing.T) {
	// PW 存在但為空白字串 → 仍視為未設置（open mode）
	cfg := loadConfig(func(k string) string {
		if k == "PW" {
			return "   "
		}
		return ""
	})
	if cfg.Password != "" || cfg.Protected() {
		t.Error("blank PW should be treated as unset (open mode)")
	}
}

func TestResolveDataRootExplicit(t *testing.T) {
	cases := map[string]string{
		"/volume1":       "/volume1",
		"  /mnt/pv  ":    "/mnt/pv",
		"/data":          "/data",
		"relative":       "relative",
		"   ":            "", // blank explicit value is treated as unset
	}
	for raw, want := range cases {
		got := resolveDataRoot(func(k string) string {
			if k == "DATA_ROOT" {
				return raw
			}
			return ""
		}, func(string) bool { return false })
		if got != want {
			t.Errorf("resolveDataRoot(DATA_ROOT=%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResolveDataRootAutoDetect(t *testing.T) {
	// not set + /data exists -> /data
	got := resolveDataRoot(func(string) string { return "" }, func(p string) bool { return p == "/data" })
	if got != "/data" {
		t.Errorf("auto-detect with /data present = %q, want /data", got)
	}
	// not set + /data absent -> disabled
	got = resolveDataRoot(func(string) string { return "" }, func(string) bool { return false })
	if got != "" {
		t.Errorf("auto-detect without /data = %q, want empty (disabled)", got)
	}
}

func TestPersistRootPath(t *testing.T) {
	cfg := config{DataRoot: "/data"}
	if got := cfg.PersistRoot(); got != "/data/rootfs" {
		t.Errorf("PersistRoot() = %q, want /data/rootfs", got)
	}
	cfg = config{DataRoot: ""}
	if got := cfg.PersistRoot(); got != "" {
		t.Errorf("PersistRoot() with empty DataRoot = %q, want empty", got)
	}
}
