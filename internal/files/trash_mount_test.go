//go:build linux

package files

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A path on another filesystem than the home trash goes to its own
// filesystem's trash, by a rename: nothing is copied, so a directory holding
// a FIFO trashes at once instead of waiting on it.
func TestAPathOnAnotherFilesystemGoesToItsOwnTrash(t *testing.T) {
	// The victim lives on /dev/shm, whose mount top a user can write a
	// .Trash-<uid> under; the home trash is on the test's temp filesystem,
	// whose top may be / on a CI runner.
	home := t.TempDir()
	src, err := os.MkdirTemp("/dev/shm", "gridwell-trash-")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	t.Cleanup(func() { os.RemoveAll(src) })
	var a, b syscall.Stat_t
	if syscall.Stat(home, &a) != nil || syscall.Stat(src, &b) != nil || a.Dev == b.Dev {
		t.Skip("the temp directory and /dev/shm are one filesystem")
	}
	t.Setenv("XDG_DATA_HOME", home)
	top, err := mountTop(src)
	if err != nil {
		t.Fatal(err)
	}
	place := filepath.Join(top, ".Trash-"+strconv.Itoa(os.Getuid()))
	_, existed := os.Lstat(place)
	name := "gone-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(place, "files", name))
		os.Remove(filepath.Join(place, "info", name+".trashinfo"))
		if existed != nil {
			os.Remove(filepath.Join(place, "files"))
			os.Remove(filepath.Join(place, "info"))
			os.Remove(place)
		}
	})
	victim := filepath.Join(src, name)
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(victim, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	d, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := within(t, "osTrash", func() error { return osTrash(d, name) }); err != nil {
		t.Fatalf("osTrash across filesystems: %v", err)
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Errorf("the trashed directory is still there: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(place, "files", name, "pipe")); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("its own filesystem's trash holds %v, %v; want the directory and its FIFO", fi, err)
	}
	if _, err := os.Stat(filepath.Join(place, "info", name+".trashinfo")); err != nil {
		t.Errorf("no trashinfo record beside it: %v", err)
	}
}
