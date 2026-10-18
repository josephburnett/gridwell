//go:build unix

package cli

// The flock half of the serve lock; servelock.go holds the contract.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// serveLock is the held exclusive lock; the zero value is never valid.
type serveLock struct {
	f *os.File
}

// acquireServeLock takes the exclusive per-directory lock, or returns
// *errServeLockHeld carrying the running holder's banner.
func acquireServeLock(dir string) (*serveLock, error) {
	path := filepath.Join(dir, "serve.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("serve lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		banner, _ := os.ReadFile(path)
		f.Close()
		return nil, &errServeLockHeld{banner: strings.TrimSpace(string(banner))}
	}
	// Any content is a crashed holder's leftover, since a clean Release
	// removes the file. Empty it until our banner is known.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("serve lock: %w", err)
	}
	return &serveLock{f: f}, nil
}

// WriteBanner records the line a conflicting serve re-emits.
func (l *serveLock) WriteBanner(banner string) {
	_, _ = l.f.WriteAt([]byte(banner+"\n"), 0)
	_ = l.f.Sync()
}

// probeServeLock answers "is anyone serving this node directory?" with a shared
// non-blocking flock. Taking the exclusive lock would let a read-only
// question beat a starting serve to it and manufacture a failure.
func probeServeLock(dir string) (banner string, held bool, err error) {
	path := filepath.Join(dir, "serve.lock")
	f, oerr := os.Open(path)
	if oerr != nil {
		if os.IsNotExist(oerr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("serve lock: %w", oerr)
	}
	defer f.Close()
	if flerr := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); flerr != nil {
		// Exclusively held: a serve is running, or mid-start.
		b, _ := os.ReadFile(path)
		return strings.TrimSpace(string(b)), true, nil
	}
	// Closing drops the shared lock; the file stays as the crash breadcrumb.
	return "", false, nil
}

// Release drops the lock and removes the file, so a leftover serve.lock
// means the holder crashed. Informational only: the flock is what gates.
func (l *serveLock) Release() {
	_ = os.Remove(l.f.Name())
	_ = l.f.Close() // closing drops the flock
}
