//go:build windows

package cli

// Windows stub: no flock. A windows build serves without the per-home
// single-server guard (LockFileEx is the faithful port when a windows
// distribution becomes real; see servelock.go for the contract). The
// STATUS probe refuses honestly instead of answering "not serving" for a
// question it cannot ask — a false negative would let the desktop app
// start a second server over the same DBs.

import "errors"

type serveLock struct{}

func acquireServeLock(home string) (*serveLock, error) { return &serveLock{}, nil }
func (l *serveLock) WriteBanner(banner string)         {}
func (l *serveLock) Release()                          {}

func probeServeLock(home string) (string, bool, error) {
	return "", false, errors.New("serve-lock probe is not supported on this platform")
}
