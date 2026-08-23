// Package shellsvctest provides an in-memory shellsvc.Streamer for tests, so
// the shell manager, the localdb plugin's OpenShell, and the server's
// WebSocket↔OpenShell bridge can all be exercised without a real tmux/PTY.
// Sessions echo their input to their output, so a byte written in comes back
// out — enough to verify an end-to-end round trip through every hop.
package shellsvctest

import (
	"io"
	"sync"

	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// FakeStreamer is a programmable shellsvc.Streamer. HasSession is driven per
// tile so a test can pick the create / attach / reject path; every OpenSession
// is recorded with its mode and size.
type FakeStreamer struct {
	mu       sync.Mutex
	alive    map[string]bool
	sessions []*FakeSession
	killed   []string
	PaneCmd  string // canned PaneCommand answer
}

func New() *FakeStreamer { return &FakeStreamer{alive: map[string]bool{}} }

// SetAlive programs HasSession's answer for tileID.
func (f *FakeStreamer) SetAlive(tileID string, alive bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[tileID] = alive
}

func (f *FakeStreamer) OpenSession(tid string, mode tmux.Mode, cols, rows uint16) (shellsvc.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &FakeSession{
		TileID: tid, OpenMode: mode, InitialCols: cols, InitialRows: rows,
		outCh: make(chan []byte, 64), done: make(chan struct{}),
	}
	f.sessions = append(f.sessions, s)
	f.alive[tid] = true // a successful open leaves the session alive
	return s, nil
}

func (f *FakeStreamer) HasSession(tileID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[tileID], nil
}

func (f *FakeStreamer) Kill(tileID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, tileID)
	delete(f.alive, tileID)
	return nil
}

func (f *FakeStreamer) ListLiveTileIDs() ([]string, error) {
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
	return f.PaneCmd, nil
}

// LastSession returns the most recently opened session, or nil.
func (f *FakeStreamer) LastSession() *FakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

// SessionCount is how many OpenSession calls succeeded.
func (f *FakeStreamer) SessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// Killed returns the tile ids passed to Kill, in order.
func (f *FakeStreamer) Killed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// FakeSession is an echoing PTY: bytes written are pushed to Output, so a
// round trip through the manager/plugin/bridge returns them.
type FakeSession struct {
	TileID      string
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

func (s *FakeSession) Output() <-chan []byte { return s.outCh }

func (s *FakeSession) Write(p []byte) (int, error) {
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

func (s *FakeSession) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.resizes = append(s.resizes, [2]uint16{cols, rows})
	return nil
}

func (s *FakeSession) Done() <-chan struct{} { return s.done }

func (s *FakeSession) Close() error {
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

func (s *FakeSession) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Inputs returns a copy of every byte slice written to the session.
func (s *FakeSession) Inputs() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.inputs...)
}

// Resizes returns a copy of every (cols,rows) resize applied.
func (s *FakeSession) Resizes() [][2]uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]uint16(nil), s.resizes...)
}
