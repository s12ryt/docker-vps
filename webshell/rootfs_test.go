//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mkSrcTree builds a fake "container root" for testing initRootfs.
func mkSrcTree(t *testing.T, src string) {
	t.Helper()
	// bin -> usr/bin symlink (like Debian merged /usr)
	if err := os.MkdirAll(filepath.Join(src, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/bin", filepath.Join(src, "bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/lib", filepath.Join(src, "lib")); err != nil {
		t.Fatal(err)
	}
	// a regular executable file with non-default mode
	sh := filepath.Join(src, "usr", "bin", "bash")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// etc with a secret file mode 600
	if err := os.MkdirAll(filepath.Join(src, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "etc", "shadow"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// nested home
	if err := os.MkdirAll(filepath.Join(src, "home", "alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "home", "alice", "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a dir that is NOT in the whitelist: must not be copied
	if err := os.MkdirAll(filepath.Join(src, "run", "docker.sock.d"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInitRootfsCopiesWhitelist(t *testing.T) {
	src := t.TempDir()
	rootfs := filepath.Join(t.TempDir(), "rootfs")
	mkSrcTree(t, src)

	if err := initRootfs(src, rootfs); err != nil {
		t.Fatalf("initRootfs: %v", err)
	}

	// symlink preserved
	for _, link := range []string{"bin", "lib"} {
		target, err := os.Readlink(filepath.Join(rootfs, link))
		if err != nil {
			t.Fatalf("symlink %s not preserved: %v", link, err)
		}
		if target != "usr/"+filepath.Base(link) {
			t.Errorf("symlink %s -> %q, want usr/%s", link, target, link)
		}
	}

	// file contents + mode preserved
	data, err := os.ReadFile(filepath.Join(rootfs, "etc", "shadow"))
	if err != nil || string(data) != "secret" {
		t.Errorf("etc/shadow content = %q, %v; want \"secret\"", data, err)
	}
	fi, _ := os.Lstat(filepath.Join(rootfs, "etc", "shadow"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("etc/shadow mode = %o, want 600", fi.Mode().Perm())
	}
	fi, _ = os.Lstat(filepath.Join(rootfs, "usr", "bin", "bash"))
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("usr/bin/bash mode = %o, want 755", fi.Mode().Perm())
	}

	// nested dirs copied
	if _, err := os.Stat(filepath.Join(rootfs, "home", "alice", "note.txt")); err != nil {
		t.Errorf("home/alice/note.txt not copied: %v", err)
	}

	// non-whitelisted dir skipped
	if _, err := os.Lstat(filepath.Join(rootfs, "run")); !os.IsNotExist(err) {
		t.Errorf("run/ should not be copied, stat err = %v", err)
	}

	// pseudo dirs created
	for _, d := range []string{"proc", "sys", "dev", "tmp"} {
		if fi, err := os.Lstat(filepath.Join(rootfs, d)); err != nil || !fi.IsDir() {
			t.Errorf("pseudo dir %s missing: %v", d, err)
		}
	}
	// tmp is world-writable sticky
	fi, _ = os.Lstat(filepath.Join(rootfs, "tmp"))
	if fi.Mode().Perm() != 0o777 || fi.Mode()&os.ModeSticky == 0 {
		t.Errorf("tmp mode = %o sticky=%v, want 1777 sticky", fi.Mode().Perm(), fi.Mode()&os.ModeSticky != 0)
	}
}

func TestInitRootfsIdempotent(t *testing.T) {
	src := t.TempDir()
	rootfs := filepath.Join(t.TempDir(), "rootfs")
	mkSrcTree(t, src)

	if err := initRootfs(src, rootfs); err != nil {
		t.Fatalf("first initRootfs: %v", err)
	}

	// user mutation after first init: a new file + a modified file
	userFile := filepath.Join(rootfs, "root", "user-data.txt")
	if err := os.MkdirAll(filepath.Join(rootfs, "root"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	origSrc := filepath.Join(src, "etc", "shadow")
	if err := os.WriteFile(origSrc, []byte("changed-in-source"), 0o600); err != nil {
		t.Fatal(err)
	}

	// second init must skip: user file kept, source change NOT propagated
	if err := initRootfs(src, rootfs); err != nil {
		t.Fatalf("second initRootfs: %v", err)
	}
	if data, err := os.ReadFile(userFile); err != nil || string(data) != "keep me" {
		t.Errorf("user file lost after re-init: %q, %v", data, err)
	}
	if data, _ := os.ReadFile(filepath.Join(rootfs, "etc", "shadow")); string(data) != "secret" {
		t.Errorf("re-init overwrote existing rootfs content: %q", data)
	}
}
