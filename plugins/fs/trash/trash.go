// Package trash moves host files into the freedesktop.org "home trash"
// instead of unlinking them, so discarding a file (or a directory) through
// Gridwell is recoverable rather than an irreversible rm -rf. It implements
// the spec's home trash ($XDG_DATA_HOME/Trash, default ~/.local/share/Trash)
// directly — no trash CLI is assumed present — so a desktop file manager can
// list and restore what Gridwell discarded.
package trash

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Trash moves path into the freedesktop home trash, writing the matching
// .trashinfo record. path may be a file, directory, or symlink.
func Trash(path string) error {
	dir, err := homeDir()
	if err != nil {
		return err
	}
	return into(dir, path)
}

// homeDir resolves the freedesktop home-trash directory. Honors
// XDG_DATA_HOME, falling back to ~/.local/share.
func homeDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "Trash"), nil
}

// into moves src into the trash directory trashDir, writing the matching
// .trashinfo record (original path + deletion time) per the spec. src may be a
// file, directory, or symlink. The info record is reserved atomically (O_EXCL)
// so two deletes of same-named files never collide.
func into(trashDir, src string) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return err
	}

	filesDir := filepath.Join(trashDir, "files")
	infoDir := filepath.Join(trashDir, "info")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}

	record := []byte(fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		encodePath(abs), time.Now().Format("2006-01-02T15:04:05")))

	base := filepath.Base(abs)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		infoPath := filepath.Join(infoDir, name+".trashinfo")
		f, err := os.OpenFile(infoPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // the name is taken: try name.2, name.3, …
		}
		if err != nil {
			return err
		}
		_, werr := f.Write(record)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(infoPath)
			return werr
		}
		if err := moveOrCopy(abs, filepath.Join(filesDir, name)); err != nil {
			_ = os.Remove(infoPath) // do not leave an orphaned record
			return err
		}
		return nil
	}
}

// moveOrCopy renames src to dst, falling back to a recursive copy-then-remove
// when the two are on different filesystems, where os.Rename returns EXDEV.
func moveOrCopy(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if cerr := copyTree(src, dst); cerr != nil {
		_ = os.RemoveAll(dst)
		return cerr
	}
	return os.RemoveAll(src)
}

// copyTree recursively copies src to dst (files, dirs, symlinks), preserving
// permission bits. Used only for the cross-device trash fallback.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
}

// encodePath percent-encodes an absolute path for a .trashinfo Path= field
// per the spec, preserving the '/' separators.
func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
