package files

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameAt moves name in dir to newName in to, both directories already
// opened through their roots, so no path is walked again, and replaces
// nothing: a name taken there is EEXIST. A filesystem without
// RENAME_NOREPLACE (EINVAL) is checked first instead, which a name taken in
// between can still lose to.
func renameAt(dir *os.File, name string, to *os.File, newName string) error {
	err := unix.Renameat2(int(dir.Fd()), name, int(to.Fd()), newName, unix.RENAME_NOREPLACE)
	if err != unix.EINVAL {
		return err
	}
	var st unix.Stat_t
	if unix.Fstatat(int(to.Fd()), newName, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return unix.EEXIST
	}
	return unix.Renameat(int(dir.Fd()), name, int(to.Fd()), newName)
}
