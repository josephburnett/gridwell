//go:build unix

package files

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameAt moves name in dir to newName in to, both directories already
// opened through their roots, so no path is walked again.
func renameAt(dir *os.File, name string, to *os.File, newName string) error {
	return unix.Renameat(int(dir.Fd()), name, int(to.Fd()), newName)
}
