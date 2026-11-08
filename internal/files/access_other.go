//go:build !unix

package files

import (
	iofs "io/fs"
	"os"
)

// writeAccess reads the one write bit Go maps here, the read-only attribute.
func writeAccess(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() && fi.Mode().Perm()&0o200 == 0 {
		return iofs.ErrPermission
	}
	return nil
}
