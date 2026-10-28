//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// T-layer seed tests. These run inside the golang:1.26 container, whose
// image root provides a real OS tree (/usr/bin/go, /etc/passwd, usr-merge
// /bin symlink) to seed from.

func TestSeedFirstBoot(t *testing.T) {
	vol := t.TempDir()
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("initDataRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vol, "rootfs/usr/local/go/bin/go")); err != nil {
		t.Fatalf("seeded /usr/local/go/bin/go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vol, "rootfs/etc/passwd")); err != nil {
		t.Fatalf("seeded /etc/passwd missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vol, "rootfs", seedMarker)); err != nil {
		t.Fatalf("seed marker missing: %v", err)
	}
}

func TestSeedIdempotent(t *testing.T) {
	vol := t.TempDir()
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("first initDataRoot: %v", err)
	}
	p := filepath.Join(vol, "rootfs/usr/local/dvps-idem.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("second initDataRoot: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "keep" {
		t.Fatalf("rerun clobbered user file: %q %v", b, err)
	}
}

func TestSeedMergeKeepsExisting(t *testing.T) {
	vol := t.TempDir()
	// simulate a v1.2.x volume: user data already on the volume
	p := filepath.Join(vol, "rootfs/usr/local/custom.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("user-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("initDataRoot: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "user-data" {
		t.Fatalf("volume data lost: %q %v", b, err)
	}
	// and image files the volume lacks are backfilled
	if _, err := os.Stat(filepath.Join(vol, "rootfs/usr/local/go/bin/go")); err != nil {
		t.Fatalf("backfill of /usr/local/go/bin/go missing: %v", err)
	}
}

func TestSeedTopSymlinks(t *testing.T) {
	vol := t.TempDir()
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("initDataRoot: %v", err)
	}
	tgt, err := os.Readlink("/bin")
	if err != nil {
		t.Skip("image root /bin is not a symlink (non usr-merge image)")
	}
	got, err := os.Readlink(filepath.Join(vol, "rootfs/bin"))
	if err != nil {
		t.Fatalf("volume /bin not seeded as symlink: %v", err)
	}
	if got != tgt {
		t.Fatalf("volume /bin -> %q, want %q", got, tgt)
	}
}

func TestSeedEtcMerge(t *testing.T) {
	vol := t.TempDir()
	// simulate a v1.2.x volume: account data already on the volume
	p := filepath.Join(vol, "rootfs/etc/passwd")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("root:x:0:0:custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initDataRoot(vol); err != nil {
		t.Fatalf("initDataRoot: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "root:x:0:0:custom\n" {
		t.Fatalf("etc volume data lost: %q %v", b, err)
	}
	// image /etc files the volume never had are backfilled
	if _, err := os.Stat(filepath.Join(vol, "rootfs/etc/debian_version")); err != nil {
		t.Fatalf("etc backfill of /etc/debian_version missing: %v", err)
	}
}
