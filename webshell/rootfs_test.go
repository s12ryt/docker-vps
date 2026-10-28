//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mkSrcTree builds a fake container root filesystem to migrate from.
func mkSrcTree(t *testing.T, src string) {
	t.Helper()
	mkdir := func(parts ...string) string {
		p := filepath.Join(append([]string{src}, parts...)...)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(parts []string, content string, mode os.FileMode) {
		p := filepath.Join(append([]string{src}, parts...)...)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	mkdir("usr", "bin")
	write([]string{"usr", "bin", "bash"}, "#!/bin/sh\n", 0755)
	write([]string{"usr", "bin", "webshell"}, "fake-binary", 0755)

	mkdir("var", "lib", "apt", "lists")
	mkdir("var", "lib", "dpkg")
	write([]string{"var", "lib", "dpkg", "status"}, "Package: core\n", 0644)

	mkdir("root")
	write([]string{"root", "rootfile.txt"}, "root-data", 0644)

	mkdir("home", "alice")
	write([]string{"home", "alice", "note.txt"}, "hi", 0644)

	mkdir("opt", "tool")

	mkdir("etc", "apt")
	write([]string{"etc", "apt", "sources.list"}, "deb http://deb.debian.org/debian bookworm main\n", 0644)
	write([]string{"etc", "shadow"}, "secret", 0600)
	write([]string{"etc", "hostname"}, "abc123\n", 0644)
	write([]string{"etc", "hosts"}, "127.0.0.1 localhost\n", 0644)
	write([]string{"etc", "resolv.conf"}, "nameserver 1.1.1.1\n", 0644)
	if err := os.Symlink("motd", filepath.Join(src, "etc", "motd.link")); err != nil {
		t.Fatalf("symlink etc/motd.link: %v", err)
	}

	// usr-merge symlinks (not in whitelist, must stay untouched)
	if err := os.Symlink("usr/bin", filepath.Join(src, "bin")); err != nil {
		t.Fatalf("symlink bin: %v", err)
	}
	if err := os.Symlink("usr/lib", filepath.Join(src, "lib")); err != nil {
		t.Fatalf("symlink lib: %v", err)
	}

	// non-whitelist dirs stay untouched
	mkdir("run", "docker.sock.d")
	mkdir("srv")
	mkdir("media")
}

func assertSymlink(t *testing.T, path, wantTarget string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("%s: not a symlink: %v", path, err)
	}
	if got != wantTarget {
		t.Fatalf("%s: symlink target = %q, want %q", path, got, wantTarget)
	}
}

// assertFile checks a file following symlinks (os.Stat), with content and mode.
func assertFile(t *testing.T, path, wantContent string, wantMode os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("%s: not a regular file (mode %v)", path, fi.Mode())
	}
	if wantContent != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read: %v", path, err)
		}
		if string(b) != wantContent {
			t.Fatalf("%s: content = %q, want %q", path, b, wantContent)
		}
	}
	if wantMode != 0 && fi.Mode().Perm() != wantMode {
		t.Fatalf("%s: mode = %v, want %v", path, fi.Mode().Perm(), wantMode)
	}
}

func assertRealDir(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s: not a real directory (mode %v)", path, fi.Mode())
	}
}

func TestInitPersistLayerFirstBoot(t *testing.T) {
	src := t.TempDir()
	persist := t.TempDir()
	mkSrcTree(t, src)

	if err := initPersistLayer(src, persist); err != nil {
		t.Fatalf("initPersistLayer: %v", err)
	}

	// whitelist dirs become symlinks into the persist layer
	for _, d := range []string{"usr", "var", "root", "home", "opt"} {
		assertSymlink(t, filepath.Join(src, d), filepath.Join(persist, d))
	}

	// content reachable through the symlinks with permissions intact
	assertFile(t, filepath.Join(src, "usr", "bin", "bash"), "#!/bin/sh\n", 0755)
	assertFile(t, filepath.Join(src, "var", "lib", "dpkg", "status"), "Package: core\n", 0644)
	assertFile(t, filepath.Join(src, "root", "rootfile.txt"), "root-data", 0644)
	assertFile(t, filepath.Join(src, "home", "alice", "note.txt"), "hi", 0644)

	// /etc stays a real directory...
	assertRealDir(t, filepath.Join(src, "etc"))
	// ...with per-entry symlinks
	assertSymlink(t, filepath.Join(src, "etc", "apt"), filepath.Join(persist, "etc", "apt"))
	assertSymlink(t, filepath.Join(src, "etc", "shadow"), filepath.Join(persist, "etc", "shadow"))
	assertFile(t, filepath.Join(src, "etc", "apt", "sources.list"), "deb http://deb.debian.org/debian bookworm main\n", 0644)
	assertFile(t, filepath.Join(src, "etc", "shadow"), "secret", 0600)

	// Docker bind-mounted files must remain real files
	for _, f := range []string{"resolv.conf", "hostname", "hosts"} {
		fi, err := os.Lstat(filepath.Join(src, "etc", f))
		if err != nil {
			t.Fatalf("etc/%s: %v", f, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("etc/%s must stay a real file (bind mount)", f)
		}
	}

	// pre-existing symlink inside etc is left alone
	fi, err := os.Lstat(filepath.Join(src, "etc", "motd.link"))
	if err != nil {
		t.Fatalf("etc/motd.link should be untouched: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("etc/motd.link should still be a symlink")
	}

	// usr-merge symlinks at root stay untouched (they resolve through /usr)
	assertSymlink(t, filepath.Join(src, "bin"), "usr/bin")
	assertSymlink(t, filepath.Join(src, "lib"), "usr/lib")

	// non-whitelist dirs stay untouched
	if _, err := os.Stat(filepath.Join(src, "run", "docker.sock.d")); err != nil {
		t.Fatalf("run/docker.sock.d should stay: %v", err)
	}
}

func TestInitPersistLayerIdempotent(t *testing.T) {
	persist := t.TempDir()

	// First container boot
	src1 := t.TempDir()
	mkSrcTree(t, src1)
	if err := initPersistLayer(src1, persist); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// user data written through the symlink farm lands in the persist layer
	if err := os.WriteFile(filepath.Join(src1, "root", "user-data.txt"), []byte("keep-me"), 0644); err != nil {
		t.Fatal(err)
	}

	// Second boot: fresh image layer (same content), same volume
	src2 := t.TempDir()
	mkSrcTree(t, src2)
	if err := initPersistLayer(src2, persist); err != nil {
		t.Fatalf("second init: %v", err)
	}

	// user marker survived
	assertFile(t, filepath.Join(src2, "root", "user-data.txt"), "keep-me", 0644)
}

func TestInitPersistLayerExistingVolumeWins(t *testing.T) {
	src := t.TempDir()
	persist := t.TempDir()
	mkSrcTree(t, src)

	// pre-populated volume (e.g. from an older release)
	if err := os.MkdirAll(filepath.Join(persist, "usr", "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persist, "usr", "bin", "bash"), []byte("OLD-BASH"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persist, "usr", "bin", "old-data.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := initPersistLayer(src, persist); err != nil {
		t.Fatalf("init: %v", err)
	}

	// image content must NOT overwrite what the volume already has
	assertFile(t, filepath.Join(src, "usr", "bin", "bash"), "OLD-BASH", 0755)
	// volume-only files are reachable
	assertFile(t, filepath.Join(src, "usr", "bin", "old-data.txt"), "old", 0644)
}

func TestInitPersistLayerEtcHybridBackfill(t *testing.T) {
	src := t.TempDir()
	persist := t.TempDir()
	mkSrcTree(t, src)

	// volume already has etc/apt from a previous boot (with different content)
	if err := os.MkdirAll(filepath.Join(persist, "etc", "apt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persist, "etc", "apt", "sources.list"), []byte("old-list"), 0644); err != nil {
		t.Fatal(err)
	}
	// image ships a new config file that the volume does not have yet
	if err := os.WriteFile(filepath.Join(src, "etc", "newthing.conf"), []byte("new-config"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := initPersistLayer(src, persist); err != nil {
		t.Fatalf("init: %v", err)
	}

	// existing volume entry wins (no overwrite), but still gets symlinked
	assertSymlink(t, filepath.Join(src, "etc", "apt"), filepath.Join(persist, "etc", "apt"))
	assertFile(t, filepath.Join(src, "etc", "apt", "sources.list"), "old-list", 0644)
	// missing entry is backfilled from the image and symlinked
	assertSymlink(t, filepath.Join(src, "etc", "newthing.conf"), filepath.Join(persist, "etc", "newthing.conf"))
	assertFile(t, filepath.Join(src, "etc", "newthing.conf"), "new-config", 0644)
}

func TestInitPersistLayerV110VolumeUpgrade(t *testing.T) {
	src := t.TempDir()
	persist := t.TempDir()
	mkSrcTree(t, src)

	// simulate a v1.1.0 volume: proot-style copy of the old whitelist
	// (usr var root home opt etc plus the extra dirs v1.1.0 also copied)
	for _, d := range []string{"usr", "var", "root", "home", "opt", "etc", "bin", "lib", "media", "mnt", "sbin", "srv"} {
		if err := os.MkdirAll(filepath.Join(persist, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(persist, "root", "v110-data.txt"), []byte("v110"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persist, "bin", "v110-tool"), []byte("tool"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := initPersistLayer(src, persist); err != nil {
		t.Fatalf("upgrade init: %v", err)
	}

	// whitelist dirs migrated to symlinks
	for _, d := range []string{"usr", "var", "root", "home", "opt"} {
		assertSymlink(t, filepath.Join(src, d), filepath.Join(persist, d))
	}
	// v1.1.0 user data reachable through the new symlinks
	assertFile(t, filepath.Join(src, "root", "v110-data.txt"), "v110", 0644)
	// v1.1.0 extra copies are left alone in the volume
	assertFile(t, filepath.Join(persist, "bin", "v110-tool"), "tool", 0755)
	// src root keeps its usr-merge symlink (not replaced by the v1.1.0 copy)
	assertSymlink(t, filepath.Join(src, "bin"), "usr/bin")
	// etc handled as hybrid again (per-entry symlinks)
	assertSymlink(t, filepath.Join(src, "etc", "shadow"), filepath.Join(persist, "etc", "shadow"))
}
