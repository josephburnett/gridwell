//go:build unix

package files

import (
	"os"
	"path/filepath"
	"syscall"
)

// mountTop is the top directory of the filesystem holding path: the last
// directory up from it on the same device.
func mountTop(path string) (string, error) {
	dev := func(p string) (uint64, error) {
		var st syscall.Stat_t
		err := syscall.Stat(p, &st)
		return uint64(st.Dev), err
	}
	at := filepath.Clean(path)
	d, err := dev(at)
	if err != nil {
		return "", &os.PathError{Op: "stat", Path: at, Err: err}
	}
	for up := filepath.Dir(at); up != at; at, up = up, filepath.Dir(up) {
		if u, err := dev(up); err != nil || u != d {
			break
		}
	}
	return at, nil
}
