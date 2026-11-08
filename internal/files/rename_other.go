//go:build !linux && !darwin

package files

import (
	"os"
	"path/filepath"
)

// renameAt is rename_linux.go's by path, after a check that the name is
// free: the OS here has no rename by directory handle that refuses to
// replace, so a directory swapped, or a name taken, between the check and
// the rename is walked or replaced.
func renameAt(dir *os.File, name string, to *os.File, newName string) error {
	dst := filepath.Join(to.Name(), newName)
	if _, err := os.Lstat(dst); err == nil {
		return &os.LinkError{Op: "rename", Old: name, New: dst, Err: os.ErrExist}
	}
	return os.Rename(filepath.Join(dir.Name(), name), dst)
}
