//go:build !unix

package files

import (
	"os"
	"path/filepath"
)

// renameAt is the unix one by path: the OS here renames by no directory
// handle, so a directory swapped between the open and the rename is walked.
func renameAt(dir *os.File, name string, to *os.File, newName string) error {
	return os.Rename(filepath.Join(dir.Name(), name), filepath.Join(to.Name(), newName))
}
