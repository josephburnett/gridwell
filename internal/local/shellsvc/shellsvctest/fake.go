// Package shellsvctest provides an in-memory shellsvc.Streamer, so the shell
// manager, the home's OpenShell, and the server's WebSocket bridge can be
// exercised without a real tmux or PTY. Tmuxes echo their input to
// their output, which is enough to verify a round trip through every hop.
package shellsvctest

import (
	"io"
	"sync"

	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// FakeStreamer is a programmable shellsvc.Streamer. Has is driven per
// tmux so a test can pick the create, attach, or reject path, and every
// Open is recorded with its mode and size.
type FakeStreamer struct {
	mu      sync.Mutex
	alive   map[string]bool
	tmuxes  []*FakeAttachment
	killed  []string
	PaneCmd string // canned PaneCommand answer
	PaneErr error  // when set, PaneCommand fails with it: tmux unreachable
	// OpenErr, when set, makes every Open fail with it and open no
	// tmux, which is how the real streamer behaves when the PTY layer
	// refuses: no tmux server, a failed exec, or a platform with no PTY.
	OpenErr error
	// KillErr, when set, makes every Kill fail with it and leave the tmux.
	KillErr error
}

func New() *FakeStreamer { return &FakeStreamer{alive: map[string]bool{}} }

// SetAlive programs Has's answer for key.
func (f *FakeStreamer) SetAlive(key string, alive bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[key] = alive
}

func (f *FakeStreamer) Open(key string, mode tmux.Mode, cols, rows uint16) (shellsvc.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.OpenErr != nil {
		return nil, f.OpenErr
	}
	s := &FakeAttachment{
		Key: key, OpenMode: mode, InitialCols: cols, InitialRows: rows,
		outCh: make(chan []byte, 64), done: make(chan struct{}),
	}
	f.tmuxes = append(f.tmuxes, s)
	f.alive[key] = true // a successful open leaves the tmux alive
	return s, nil
}

func (f *FakeStreamer) Has(key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[key], nil
}

func (f *FakeStreamer) Kill(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.KillErr != nil {
		return f.KillErr
	}
	f.killed = append(f.killed, key)
	delete(f.alive, key)
	return nil
}

func (f *FakeStreamer) ListLive() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, alive := range f.alive {
		if alive {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *FakeStreamer) PaneCommand(string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PaneErr != nil {
		return "", f.PaneErr
	}
	return f.PaneCmd, nil
}

// LastAttachment returns the most recently opened tmux, or nil.
func (f *FakeStreamer) LastAttachment() *FakeAttachment {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tmuxes) == 0 {
		return nil
	}
	return f.tmuxes[len(f.tmuxes)-1]
}

// AttachmentCount is how many Open calls succeeded.
func (f *FakeStreamer) AttachmentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tmuxes)
}

// Killed returns the tmux keys passed to Kill, in order.
func (f *FakeStreamer) Killed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// FakeAttachment is an echoing PTY. Bytes written are pushed to Output, so a
// round trip through the manager, the namespace, and the bridge returns them.
type FakeAttachment struct {
	Key         string
	OpenMode    tmux.Mode
	InitialCols uint16
	InitialRows uint16

	mu      sync.Mutex
	inputs  [][]byte
	resizes [][2]uint16
	closed  bool

	outCh chan []byte
	done  chan struct{}
}

func (s *FakeAttachment) Output() <-chan []byte { return s.outCh }

func (s *FakeAttachment) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	dup := append([]byte(nil), p...)
	s.inputs = append(s.inputs, dup)
	s.mu.Unlock()
	select {
	case s.outCh <- dup: // echo
	default:
	}
	return len(p), nil
}

func (s *FakeAttachment) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.resizes = append(s.resizes, [2]uint16{cols, rows})
	return nil
}

func (s *FakeAttachment) Done() <-chan struct{} { return s.done }

func (s *FakeAttachment) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	close(s.outCh)
	return nil
}

func (s *FakeAttachment) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Inputs returns a copy of every byte slice written to the tmux.
func (s *FakeAttachment) Inputs() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.inputs...)
}

// Resizes returns a copy of every (cols,rows) resize applied.
func (s *FakeAttachment) Resizes() [][2]uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]uint16(nil), s.resizes...)
}
