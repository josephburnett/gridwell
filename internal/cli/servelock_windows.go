//go:build windows

package cli

// Windows stub: no flock. A windows build serves without the per-home
// single-server guard (LockFileEx is the faithful port when a windows
// distribution becomes real; see servelock.go for the contract).

type serveLock struct{}

func acquireServeLock(home string) (*serveLock, error) { return &serveLock{}, nil }
func (l *serveLock) WriteBanner(banner string)         {}
func (l *serveLock) Release()                          {}
