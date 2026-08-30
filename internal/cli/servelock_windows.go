//go:build windows

package cli

// Windows stub: no flock, so a windows build serves without the per-home
// single-server guard. LockFileEx is the faithful port; servelock.go holds
// the contract. The status probe refuses honestly instead of answering "not
// serving" to a question it cannot ask, because a false negative would let
// the desktop app start a second server over the same database.

import "errors"

type serveLock struct{}

func acquireServeLock(home string) (*serveLock, error) { return &serveLock{}, nil }
func (l *serveLock) WriteBanner(banner string)         {}
func (l *serveLock) Release()                          {}

func probeServeLock(home string) (string, bool, error) {
	return "", false, errors.New("serve-lock probe is not supported on this platform")
}
