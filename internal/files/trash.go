package files

// The OS trash, so a file deleted through Gridwell is recoverable: Finder's
// ~/.Trash on macOS, and elsewhere the freedesktop.org home trash
// ($XDG_DATA_HOME/Trash, default ~/.local/share/Trash), written directly so a
// file manager can list and restore what was discarded.

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// osTrash moves name in dir, a file, directory or symlink, into the user's
// trash.
func osTrash(dir *os.File, name string) error {
	place, finder, err := trashPlace(runtime.GOOS)
	switch {
	case err != nil:
		return err
	case finder:
		return intoFinder(place, dir, name)
	}
	return intoTrash(place, dir, name)
}

// trashPlace is the trash directory for goos, and whether it is Finder's.
func trashPlace(goos string) (dir string, finder bool, err error) {
	if goos == "darwin" {
		home, err := os.UserHomeDir()
		return filepath.Join(home, ".Trash"), true, err
	}
	dir, err = trashHome()
	return dir, false, err
}

// finderMu makes intoFinder's check for a free name and its move one step
// within this process; a rename onto a taken name would replace what is
// there.
var finderMu sync.Mutex

// intoFinder moves base in dir into Finder's trash under its own name, a
// clash taking the next free "name N.ext" as Finder names one. The trash
// keeps no record beside the file: Finder's own "Put Back" record is private
// to Finder.
func intoFinder(trashDir string, dir *os.File, base string) error {
	to, err := openTrashDir(trashDir)
	if err != nil {
		return err
	}
	defer to.Close()
	finderMu.Lock()
	defer finderMu.Unlock()
	ext := filepath.Ext(base)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s %d%s", strings.TrimSuffix(base, ext), i, ext)
		}
		dst := filepath.Join(trashDir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return moveOrCopy(dir, base, to, name)
	}
}

// openTrashDir is a trash directory, made if absent.
func openTrashDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.Open(dir)
}

// trashHome resolves the freedesktop home-trash directory. Honors
// XDG_DATA_HOME, falling back to ~/.local/share.
func trashHome() (string, error) {
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

// intoTrash moves base in dir, a file, directory or symlink, into the trash
// directory trashDir, writing the matching .trashinfo record (original path
// and deletion time) per the spec. The info record is reserved atomically
// (O_EXCL) so two deletes of same-named files never collide.
func intoTrash(trashDir string, dir *os.File, base string) error {
	infoDir := filepath.Join(trashDir, "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}
	files, err := openTrashDir(filepath.Join(trashDir, "files"))
	if err != nil {
		return err
	}
	defer files.Close()

	record := []byte(fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		encodePath(filepath.Join(dir.Name(), base)), time.Now().Format("2006-01-02T15:04:05")))

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
		_, err = f.Write(record)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = moveOrCopy(dir, base, files, name)
		}
		if err != nil {
			_ = os.Remove(infoPath) // do not leave an orphaned record
		}
		return err
	}
}

// moveOrCopy renames name in dir to dstName in to, falling back to a
// recursive copy-then-remove when the two are on different filesystems,
// where a rename answers EXDEV.
func moveOrCopy(dir *os.File, name string, to *os.File, dstName string) error {
	err := renameAt(dir, name, to, dstName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	src, dst := filepath.Join(dir.Name(), name), filepath.Join(to.Name(), dstName)
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
