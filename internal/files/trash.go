package files

// The OS trash, so a file deleted through Gridwell is recoverable: Finder's
// ~/.Trash on macOS, and elsewhere the freedesktop.org home trash
// ($XDG_DATA_HOME/Trash, default ~/.local/share/Trash), written directly so a
// file manager can list and restore what was discarded. A path on another
// filesystem goes to that filesystem's own trash, at its top: .Trashes/<uid>
// on macOS, .Trash-<uid> elsewhere. Every move is a rename; nothing is
// copied.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// osTrash moves name in dir, a file, directory or symlink, into the user's
// trash, or its filesystem's when that is another than the home trash's.
func osTrash(dir *os.File, name string) error {
	place, finder, err := trashPlace(runtime.GOOS)
	if err != nil {
		return err
	}
	into := intoTrash
	if finder {
		into = intoFinder
	}
	if err = into(place, dir, name); !errors.Is(err, syscall.EXDEV) {
		return err
	}
	top, err := mountTop(dir.Name())
	if err != nil {
		return fmt.Errorf("%s is on another filesystem than the trash, whose own trash cannot be found: %w", dir.Name(), err)
	}
	uid := strconv.Itoa(os.Getuid())
	place = filepath.Join(top, ".Trash-"+uid)
	if finder {
		place = filepath.Join(top, ".Trashes", uid)
	}
	return into(place, dir, name)
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
	ext := filepath.Ext(base)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s %d%s", strings.TrimSuffix(base, ext), i, ext)
		}
		if err := renameAt(dir, base, to, name); !errors.Is(err, os.ErrExist) {
			return err
		}
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
			err = renameAt(dir, base, files, name)
		}
		if err != nil {
			_ = os.Remove(infoPath) // do not leave an orphaned record
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
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
