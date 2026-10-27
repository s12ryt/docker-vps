package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// rootfsDirs is the whitelist of top-level directories copied from the
// container root into the persistent rootfs on first start.
// /proc /sys /dev are bound by proot itself; /run holds host runtime state
// (sockets, pids) that must not leak into the guest; /tmp is recreated fresh.
var rootfsDirs = []string{
	"bin", "etc", "home", "lib", "lib64", "media",
	"mnt", "opt", "root", "sbin", "srv", "usr", "var",
}

// initRootfs makes sure rootfs holds a usable guest root filesystem.
// srcRoot is the (real) root to copy from, rootfs the guest location.
// It is idempotent: once usr/ exists inside rootfs, nothing is copied again,
// so user data and apt-installed packages survive container restarts.
func initRootfs(srcRoot, rootfs string) error {
	if _, err := os.Stat(filepath.Join(rootfs, "usr")); err == nil {
		return nil // already initialized — never overwrite the "VPS disk"
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return fmt.Errorf("create rootfs dir: %w", err)
	}
	for _, d := range rootfsDirs {
		src := filepath.Join(srcRoot, d)
		if _, err := os.Lstat(src); err != nil {
			continue // not present in this image — fine
		}
		if err := copyPath(src, filepath.Join(rootfs, d)); err != nil {
			return fmt.Errorf("copy %s: %w", d, err)
		}
	}
	// pseudo mount points; proot binds the real ones over these
	for _, d := range []string{"proc", "sys", "dev", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := os.Chmod(filepath.Join(rootfs, "tmp"), 0o777|os.ModeSticky); err != nil {
		return fmt.Errorf("chmod tmp: %w", err)
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
