package main

// T-layer (translate layer) data-root initialization for docker-vps v1.3.0+.
//
// The translate layer (webshell/translate/preload.c -> /usr/lib/libdvps-t.so,
// loaded globally via /etc/ld.so.preload) rewrites whitelisted absolute paths
// (/usr /var /etc /root /home /opt /srv /bin /sbin /lib /lib64 /libx32) to
// <DATA_ROOT>/rootfs/... at the libc call level, so all reads and writes land
// on the /data volume while the container root stays a real, symlink-free
// filesystem (image layer, read-only in practice).
//
// This file's job: make <DATA_ROOT>/rootfs contain a complete OS tree before
// the first dynamic process starts.
//
//   - fresh volume: seed it by copying the image-layer whitelist trees
//     (one-time, ~600MB on first boot; idempotent via .dvps-seeded marker)
//   - v1.2.x volume upgrade: the volume wins — everything already on the
//     volume is kept as-is, and only image files the volume lacks are
//     backfilled (v1.2.x only ever put *written* /etc entries and the
//     symlinked big dirs on the volume, so most image /etc files were never
//     there; backfill closes those gaps without clobbering user data)
//   - /bin /sbin /lib /lib64 /libx32: replicated as whatever the image root
//     has (usr-merge symlinks on bookworm), inside the volume.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const seedMarker = ".dvps-seeded"

// seedTopDirs are whitelisted top-level directories merged from the image
// layer into the volume.
var seedTopDirs = []string{"usr", "var", "etc", "root", "home", "opt", "srv"}

// seedTopLinks are top-level entries that are usr-merge symlinks on
// bookworm. We replicate the image's actual link targets instead of
// hardcoding them; if the image has a real directory there instead, we merge
// it like a regular tree.
var seedTopLinks = []string{"bin", "sbin", "lib", "lib64", "libx32"}

// initDataRoot seeds <dataRoot>/rootfs from the image layer. Idempotent:
// returns immediately when the .dvps-seeded marker exists.
// accountFiles are kept as REAL files in the container /etc and synced both
// ways (newest mtime wins) with the volume on every boot: nss_files and the
// dynamic loader read these via libc-internal opens that bypass the
// translate layer, so they must exist on the container side as well
// (v1.2.1 lesson, revived for the T-layer).
var accountFiles = []string{
	"passwd", "passwd-", "shadow", "shadow-",
	"group", "group-", "gshadow", "gshadow-",
	"subuid", "subuid-", "subgid", "subgid-",
	".pwd.lock",
}

func initDataRoot(dataRoot string) error {
	vol := filepath.Join(dataRoot, "rootfs")
	// Account-file sync runs on EVERY boot (independent of the seed marker):
	// dpkg/useradd write the container side, nss reads the container side,
	// and this sync keeps the volume copy durable across container rebuilds.
	syncAccountFiles(vol)
	marker := filepath.Join(vol, seedMarker)
	if _, err := os.Lstat(marker); err == nil {
		return nil // already seeded — fast path, also covers v1.3.0+ reboots
	}
	if err := os.MkdirAll(vol, 0o755); err != nil {
		return fmt.Errorf("dvps: mkdir %s: %w", vol, err)
	}
	for _, d := range seedTopDirs {
		if err := seedMerge(filepath.Join("/", d), filepath.Join(vol, d)); err != nil {
			return err
		}
	}
	for _, l := range seedTopLinks {
		if err := seedTopLink(filepath.Join("/", l), filepath.Join(vol, l)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(marker, []byte("docker-vps translate-layer seed\n"), 0o644); err != nil {
		return fmt.Errorf("dvps: write seed marker: %w", err)
	}
	return nil
}

// seedTopLink replicates a usr-merge top-level symlink (bin->usr/bin ...) on
// the volume. If the image has a real directory there instead, falls back to
// a regular merge.
func seedTopLink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		// not a symlink in the image: merge it as a normal tree (if present)
		return seedMerge(src, dst)
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil // volume already has an entry — volume wins
	}
	if err := os.Symlink(target, dst); err != nil {
		return fmt.Errorf("dvps: symlink %s -> %s: %w", dst, target, err)
	}
	return nil
}

// seedMerge recursively merges the image tree at src into the volume at dst.
// Merge rule: if the volume already has an entry at dst, the volume wins and
// the image entry is skipped (for directories we still recurse to backfill
// missing children). If the volume has nothing there, the image entry is
// copied (file, symlink or directory).
func seedMerge(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // image layer has no such tree — nothing to seed
		}
		return fmt.Errorf("dvps: lstat %s: %w", src, err)
	}
	switch fi.Mode() & os.ModeType {
	case os.ModeDir:
		if err := os.MkdirAll(dst, fi.Mode().Perm()); err != nil {
			return fmt.Errorf("dvps: mkdir %s: %w", dst, err)
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return fmt.Errorf("dvps: readdir %s: %w", src, err)
		}
		for _, e := range entries {
			if err := seedMerge(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	case os.ModeSymlink:
		if _, err := os.Lstat(dst); err == nil {
			return nil // volume wins
		}
		target, err := os.Readlink(src)
		if err != nil {
			return fmt.Errorf("dvps: readlink %s: %w", src, err)
		}
		if err := os.Symlink(target, dst); err != nil {
			return fmt.Errorf("dvps: symlink %s: %w", dst, err)
		}
	default: // regular file (ModeType == 0 lands here via default)
		if fi.Mode().IsRegular() {
			if _, err := os.Lstat(dst); err == nil {
				return nil // volume wins
			}
			if err := copySeedFile(src, dst, fi.Mode().Perm()); err != nil {
				return err
			}
		}
		// fifo/socket/device: skip — not expected in whitelisted image trees
	}
	return nil
}

// copySeedFile copies a regular file, creating dst exclusively so a racing
// writer can never be clobbered by the seed.
func copySeedFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("dvps: open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("dvps: create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("dvps: copy %s -> %s: %w", src, dst, err)
	}
	return out.Close()
}

// syncAccountFiles mirrors the 13 account files between the container /etc
// and <vol>/etc on every boot. See the accountFiles doc comment above for
// why these files must live on BOTH sides. Newest mtime wins; a file that
// exists on only one side is copied to the other (first boot seeds the
// volume, a rebuilt container restores from the volume). Best-effort:
// errors are swallowed — account sync must never block startup.
func syncAccountFiles(vol string) {
	etcVol := filepath.Join(vol, "etc")
	for _, name := range accountFiles {
		c := filepath.Join("/etc", name)
		v := filepath.Join(etcVol, name)
		cs, cerr := os.Lstat(c)
		vs, verr := os.Lstat(v)
		switch {
		case cerr != nil && verr != nil:
			// neither side has it — nothing to do
		case cerr != nil:
			// volume-only (rebuilt container over a v1.2.x volume) — restore
			if vs.Mode().IsRegular() {
				_ = copyFileOverwrite(v, c, vs.Mode().Perm())
			}
		case verr != nil:
			// container-only (first boot) — persist into the volume
			if cs.Mode().IsRegular() {
				_ = copyFileOverwrite(c, v, cs.Mode().Perm())
			}
		default:
			if !cs.Mode().IsRegular() || !vs.Mode().IsRegular() {
				continue
			}
			if vs.ModTime().After(cs.ModTime()) {
				_ = copyFileOverwrite(v, c, vs.Mode().Perm())
			} else if cs.ModTime().After(vs.ModTime()) {
				_ = copyFileOverwrite(c, v, cs.Mode().Perm())
			}
		}
	}
}

// copyFileOverwrite copies src onto dst even when dst already exists.
// Account files are tiny, so write-to-temp + rename is atomic enough.
func copyFileOverwrite(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".dvps-sync"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
