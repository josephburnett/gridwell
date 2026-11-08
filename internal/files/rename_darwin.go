package files

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameAt is rename_linux.go's, by renameatx_np's RENAME_EXCL.
func renameAt(dir *os.File, name string, to *os.File, newName string) error {
	return unix.RenameatxNp(int(dir.Fd()), name, int(to.Fd()), newName, unix.RENAME_EXCL)
}
