package main

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildShellCmdPlain(t *testing.T) {
	cfg := config{ShellPath: "/bin/bash"} // no DataRoot -> plain mode
	got := buildShellCmd(cfg)
	want := []string{"/bin/bash"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildShellCmd plain = %v, want %v", got, want)
	}
}

// wantProotArgv mirrors buildShellCmd's proot wrapping (kept in sync by hand).
// No "--" separator: proot 5.1.0 does not support it.
func wantProotArgv(rootfs, shell string) []string {
	return []string{
		"proot", "-r", rootfs,
		"-b", "/proc",
		"-b", "/dev",
		"-b", "/sys",
		"-b", "/etc/resolv.conf",
		"-b", "/etc/hosts",
		"-b", "/etc/host.conf",
		"-b", "/etc/nsswitch.conf",
		"-w", "/root",
		shell, "-l",
	}
}

func TestBuildShellCmdProot(t *testing.T) {
	cfg := config{
		ShellPath:  "/bin/bash",
		DataRoot:   "/data",
		SessionTTL: time.Hour,
	}
	got := buildShellCmd(cfg)
	want := wantProotArgv("/data/rootfs", "/bin/bash")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildShellCmd proot = %v, want %v", got, want)
	}
}

func TestBuildShellCmdProotCustomShell(t *testing.T) {
	cfg := config{ShellPath: "/usr/bin/zsh", DataRoot: "/volume1"}
	got := buildShellCmd(cfg)
	want := wantProotArgv("/volume1/rootfs", "/usr/bin/zsh")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildShellCmd proot custom = %v, want %v", got, want)
	}
}
