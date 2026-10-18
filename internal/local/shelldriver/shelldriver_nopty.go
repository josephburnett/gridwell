//go:build !unix

// The no-PTY half. Start refuses, so this build links no creack/pty and no
// syscall.Kill.
package shelldriver

import (
	"fmt"
	"runtime"
)

// PTY exists so both halves present the same type to shellsvc.Attachment.
// Start never returns one; its channels are already closed and its writes
// refused, so a caller that ignored Start's error terminates instead of
// hanging.
type PTY struct{}

var (
	closedOutput = func() chan []byte {
		c := make(chan []byte)
		close(c)
		return c
	}()
	closedDone = func() chan struct{} {
		c := make(chan struct{})
		close(c)
		return c
	}()
)

// Start refuses; see ErrShellsUnavailable.
func Start(_ Config) (*PTY, error) {
	return nil, fmt.Errorf("shelldriver: %w (%s)", ErrShellsUnavailable, runtime.GOOS)
}

func (s *PTY) Output() <-chan []byte       { return closedOutput }
func (s *PTY) Write(_ []byte) (int, error) { return 0, ErrShellsUnavailable }
func (s *PTY) Resize(_, _ uint16) error    { return ErrShellsUnavailable }
func (s *PTY) Done() <-chan struct{}       { return closedDone }
func (s *PTY) Close() error                { return nil }
