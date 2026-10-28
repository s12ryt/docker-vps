package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// persistDirs are migrated wholesale into the persistent layer on first
// boot and replaced by absolute symlinks at the container root, so that
// every write lands on the volume. /usr covers usr-merge layouts: /bin,
// /lib, ... are relative symlinks into /usr and keep resolving through
// the /usr symlink.
var persistDirs = []string{"usr", "var", "root", "home", "opt"}

// etcBindMounts are the files Docker bind-mounts over /etc at container
// start. They must stay real files: replacing them with symlinks fails
// with "Device or resource busy" and would break the bind mount.
var etcBindMounts = map[string]bool{
	"resolv.conf": true,
	"hostname":    true,
	"hosts":       true,
}

// initPersistLayer turns the container root into a symlink farm backed
// by the persistent layer (the mounted volume). The shell then runs
// natively — no path translation, no ptrace, apt & friends just work.
//
// Semantics:
//   - first boot: image content is seeded into the persist layer,
//     then replaced by symlinks
//   - later boots: whatever the volume already has wins over the image
//     layer (nothing is overwritten — "the VPS disk" is authoritative)
//   - /etc keeps its real directory and is migrated per entry, because
//     Docker bind-mounts resolv.conf/hostname/hosts over it
//
// Idempotent: entries that are already symlinks are skipped, so a
// restart on the same (mutated) root is a no-op.
func initPersistLayer(srcRoot, persist string) error {
	if err := os.MkdirAll(persist, 0o755); err != nil {
		return fmt.Errorf("create persist layer: %w", err)
	}
	for _, d := range persistDirs {
		if err := migrateDir(filepath.Join(srcRoot, d), filepath.Join(persist, d)); err != nil {
			return fmt.Errorf("migrate %s: %w", d, err)
		}
	}
	return initEtcHybrid(filepath.Join(srcRoot, "etc"), filepath.Join(persist, "etc"))
}

// migrateDir moves the real directory src onto dst (seeding dst from it
// if needed) and leaves an absolute symlink behind. If src is already a
// symlink the call is a no-op; if dst already exists its content wins.
func migrateDir(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // not present in this image — fine
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil // already migrated
	}
	if _, err := os.Lstat(dst); os.IsNotExist(err) {
		if err := copyPath(src, dst); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove original: %w", err)
	}
	return os.Symlink(dst, src)
}

// initEtcHybrid migrates /etc entry by entry: bind-mounted files and
// pre-existing symlinks are left in place, everything else is moved
// into the persist layer and symlinked. Volume entries that already
// exist are kept (image content only backfills what is missing).
func initEtcHybrid(srcEtc, dstEtc string) error {
	fi, err := os.Lstat(srcEtc)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil // already migrated
	}
	if _, err := os.Lstat(dstEtc); os.IsNotExist(err) {
		if err := copyPath(srcEtc, dstEtc); err != nil {
			return fmt.Errorf("seed etc: %w", err)
		}
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(srcEtc)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if etcBindMounts[e.Name()] {
			continue // Docker bind mount — must stay a real file
		}
		if e.Type()&fs.ModeSymlink != 0 {
			continue // leave pre-existing symlinks alone
		}
		src := filepath.Join(srcEtc, e.Name())
		dst := filepath.Join(dstEtc, e.Name())
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			if err := copyPath(src, dst); err != nil {
				return fmt.Errorf("backfill %s: %w", e.Name(), err)
			}
		} else if err != nil {
			return err
		}
		if err := os.RemoveAll(src); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
		if err := os.Symlink(dst, src); err != nil {
			return fmt.Errorf("symlink %s: %w", e.Name(), err)
		}
	}
	return nil
}

// copyPath copies src to dst, preserving symlinks, directory structure,
// file modes and contents. Special files (sockets, devices) are skipped.
func copyPath(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)

	case fi.IsDir():
		return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			info, err := e.Info()
			if err != nil {
				return err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				link, err := os.Readlink(p)
				if err != nil {
					return err
				}
				return os.Symlink(link, target)
			case e.IsDir():
				if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
					return err
				}
				return nil
			case info.Mode().IsRegular():
				return copyFile(p, target, info.Mode().Perm())
			default:
				return nil // skip sockets/devices/fifos
			}
		})

	case fi.Mode().IsRegular():
		return copyFile(src, dst, fi.Mode().Perm())

	default:
		return nil // skip special files at top level too
	}
}

// copyFile copies one regular file, preserving mode.
func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
