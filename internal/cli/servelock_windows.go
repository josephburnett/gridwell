//go:build windows

package cli

// Windows stub: no flock, so a windows build serves without the per-directory
// guard. The status probe refuses honestly rather than answering "not
// serving", because a false negative would let a second server start.

import "errors"

type serveLock struct{}

func acquireServeLock(dir string) (*serveLock, error) { return &serveLock{}, nil }
func (l *serveLock) WriteBanner(banner string)        {}
func (l *serveLock) Release()                         {}

func probeServeLock(dir string) (string, bool, error) {
	return "", false, errors.New("serve-lock probe is not supported on this platform")
}
