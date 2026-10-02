// Package shellsvc owns the tmux-backed session lifecycle. It belongs to the
// namespace that owns the shell tiles, so live bytes cross the namespace
// interface through OpenShell and a shell in a remote namespace streams over
// the same path. Everything here is keyed by session, never by tile: a key is
// the namespace-local id of the tile that started the session, which any
// number of tiles may name (rpc.ShellSession), and tmux.SessionName maps it to
// a session name.
package shellsvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/josephburnett/gridwell/internal/local/shelldriver"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// Sizing defaults and clamps, exported so the OpenShell binder and the resize
// path agree on one set of bounds.
const (
	MinCols     = 20
	MinRows     = 5
	DefaultCols = 80
	DefaultRows = 24
)

// ErrSessionGone is a session that was started and is no longer alive. Only
// the faces remain, so the caller hides the refresh button rather than
// fabricate a fresh session behind them.
var ErrSessionGone = errors.New("shell session no longer alive")

// Session's Output is a channel so a takeover or detach returns at once,
// leaving no goroutine orphaned on a PTY syscall.
type Session interface {
	Output() <-chan []byte
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Done() <-chan struct{}
	Close() error
}

// Streamer is the tmux and PTY backend, stubbed in tests.
type Streamer interface {
	OpenSession(key string, mode tmux.Mode, cols, rows uint16) (Session, error)
	HasSession(key string) (bool, error)
	Kill(key string) error
	ListLiveSessions() ([]string, error)
	PaneCommand(key string) (string, error)
}

// NewLive composes the tmux argv through the controller and execs it through
// shelldriver, so a detach kills only the client and leaves the tmux server
// running.
func NewLive(ctrl *tmux.Controller) Streamer { return &liveStreamer{ctrl: ctrl} }

type liveStreamer struct{ ctrl *tmux.Controller }

func (l *liveStreamer) OpenSession(key string, mode tmux.Mode, cols, rows uint16) (Session, error) {
	argv := l.ctrl.Args(key, mode, cols, rows, "")
	if len(argv) == 0 {
		return nil, fmt.Errorf("shellsvc: empty tmux argv for session %s mode %v", key, mode)
	}
	// ctrl.Env carries the shadow-launcher PATH; see tmux.Controller.Env.
	return shelldriver.Start(shelldriver.Config{Cols: cols, Rows: rows, BashPath: argv[0], Args: argv[1:], Env: l.ctrl.Env()})
}

func (l *liveStreamer) HasSession(key string) (bool, error)    { return l.ctrl.HasSession(key) }
func (l *liveStreamer) Kill(key string) error                  { return l.ctrl.KillSession(key) }
func (l *liveStreamer) ListLiveSessions() ([]string, error)    { return l.ctrl.ListSessions() }
func (l *liveStreamer) PaneCommand(key string) (string, error) { return l.ctrl.PaneCommand(key) }

// Manager owns the single live PTY per session and the takeover semantics, the
// namespace-side half of OpenShell: one attached tmux client per session,
// whichever tile, pane, window or device asks.
type Manager struct {
	streamer Streamer
	mu       sync.Mutex
	active   map[string]*entry
}

type entry struct {
	session Session
	stopOld chan struct{} // closed when a takeover evicts the current holder
}

// NewManager requires a non-nil Streamer; a caller with no shell backend
// leaves the Manager itself nil.
func NewManager(s Streamer) *Manager {
	return &Manager{streamer: s, active: map[string]*entry{}}
}

// Acquire takes over an active holder: signalled to exit, its PTY reused, its
// screen repainted. With no holder a live tmux session is attached and a dead
// one created if allowCreate, which is false for a session that was started,
// where fabricating state behind its faces would be wrong. The returned channel closes
// when a later Acquire takes over.
func (m *Manager) Acquire(key string, allowCreate bool, cols, rows uint16) (Session, chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.active[key]; ok {
		close(e.stopOld)
		e.stopOld = make(chan struct{})
		// The PTY is reused, so tmux cannot see the viewer changed and the
		// new pane's terminal would stay blank until something resized it.
		// Bounce the winsize one row taller and back, the kernel raising
		// SIGWINCH only on a real change. Height only, so nothing rewraps.
		_ = e.session.Resize(cols, rows+1)
		_ = e.session.Resize(cols, rows)
		return e.session, e.stopOld, nil
	}

	alive, err := m.streamer.HasSession(key)
	if err != nil {
		return nil, nil, fmt.Errorf("shellsvc: probe session %s: %w", key, err)
	}
	mode := tmux.ModeAttach
	if !alive {
		if !allowCreate {
			return nil, nil, ErrSessionGone
		}
		mode = tmux.ModeCreate
	}
	sess, err := m.streamer.OpenSession(key, mode, cols, rows)
	if err != nil {
		return nil, nil, err
	}
	m.active[key] = &entry{session: sess, stopOld: make(chan struct{})}
	return sess, m.active[key].stopOld, nil
}

// Release closes the PTY-side session if this holder still owns the entry,
// killing the gridwell-spawned tmux client but leaving the tmux server and the
// shell running for the next refresh, then fires onDetach. After a takeover it
// is a no-op: the new holder keeps the session.
func (m *Manager) Release(key string, mySession Session, myStopOld chan struct{}, onDetach func()) {
	m.mu.Lock()
	e, ok := m.active[key]
	matches := ok && e.session == mySession && e.stopOld == myStopOld
	if matches {
		delete(m.active, key)
	}
	m.mu.Unlock()
	if matches {
		log.Printf("[shellsvc] detach session=%s", key)
		_ = mySession.Close()
		if onDetach != nil {
			onDetach()
		}
	}
}

func (m *Manager) HasSession(key string) (bool, error) { return m.streamer.HasSession(key) }

// Kill is idempotent.
func (m *Manager) Kill(key string) error { return m.streamer.Kill(key) }

// PaneCommand labels a frozen shell on detach, "" when the session is gone.
func (m *Manager) PaneCommand(key string) (string, error) { return m.streamer.PaneCommand(key) }

// CleanupOrphans kills tmux sessions nothing names any more, the
// bounded leak left by a delete that raced a crash. A per-session failure does
// not abort the pass, and the count killed comes back with the first error.
func (m *Manager) CleanupOrphans(_ context.Context, exists func(key string) (bool, error)) (int, error) {
	live, err := m.streamer.ListLiveSessions()
	if err != nil {
		return 0, fmt.Errorf("list live sessions: %w", err)
	}
	killed := 0
	var firstErr error
	for _, id := range live {
		ok, err := exists(id)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("exists session %s: %w", id, err)
			}
			continue
		}
		if ok {
			continue
		}
		if err := m.streamer.Kill(id); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("kill orphan session %s: %w", id, err)
			}
			continue
		}
		killed++
	}
	return killed, firstErr
}

// ClampSize substitutes the shell defaults for zero or too-small values.
func ClampSize(cols, rows uint16) (uint16, uint16) {
	if cols == 0 {
		cols = DefaultCols
	} else if cols < MinCols {
		cols = MinCols
	}
	if rows == 0 {
		rows = DefaultRows
	} else if rows < MinRows {
		rows = MinRows
	}
	return cols, rows
}
