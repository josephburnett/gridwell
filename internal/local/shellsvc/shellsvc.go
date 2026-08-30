// Package shellsvc owns the live shell PTY mechanics — the tmux-backed session
// lifecycle that used to live in the server. It now belongs to the plugin that
// owns the shell tiles (localdb), so the live bytes cross the Gridwell gRPC
// interface (OpenShell) like everything else: the server is a pure bridge, and
// a shell in a remote plugin streams over the same path.
//
// A gridwell-private tmux server backs every shell tile; sessions named
// `gridwell-<tileID>` survive ascents and restarts (bash + scrollback live in
// tmux). The tile id here is the plugin-LOCAL id — each plugin has its own tmux.
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

// Sizing defaults / clamps for the PTY. Exported so the OpenShell binder and
// resize path agree on one set of bounds.
const (
	MinCols     = 20
	MinRows     = 5
	DefaultCols = 80
	DefaultRows = 24
)

// ErrSessionGone is returned by Acquire when the tile has been snapshotted but
// its tmux session is no longer alive — recovery is impossible (bash is gone,
// only the JPEG remains), so the caller should signal the client to hide the
// refresh button rather than fabricate a fresh session behind the snapshot.
var ErrSessionGone = errors.New("shell session no longer alive")

// Session is the per-tile PTY handle. Output is a channel rather than a blocking
// read so a takeover/detach can return immediately without orphaning a goroutine
// on a PTY syscall.
type Session interface {
	Output() <-chan []byte
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Done() <-chan struct{}
	Close() error
}

// Streamer is the tmux/PTY backend. Stubbed in tests so the manager can run
// without spawning a real tmux/PTY pair.
type Streamer interface {
	OpenSession(tileID string, mode tmux.Mode, cols, rows uint16) (Session, error)
	HasSession(tileID string) (bool, error)
	Kill(tileID string) error
	ListLiveTileIDs() ([]string, error)
	PaneCommand(tileID string) (string, error)
}

// NewLive is the production Streamer: it composes tmux argv via the controller
// and execs that argv through shelldriver, so callers get a PTY-backed tmux
// client that survives detach via the underlying tmux server.
func NewLive(ctrl *tmux.Controller) Streamer { return &liveStreamer{ctrl: ctrl} }

type liveStreamer struct{ ctrl *tmux.Controller }

func (l *liveStreamer) OpenSession(tileID string, mode tmux.Mode, cols, rows uint16) (Session, error) {
	argv := l.ctrl.Args(tileID, mode, cols, rows, "")
	if len(argv) == 0 {
		return nil, fmt.Errorf("shellsvc: empty tmux argv for tile %s mode %v", tileID, mode)
	}
	// ctrl.Env carries the shadow-launcher PATH (issue #166): panes inherit
	// PATH from the tmux SERVER process, which this client may lazy-start.
	return shelldriver.Start(shelldriver.Config{Cols: cols, Rows: rows, BashPath: argv[0], Args: argv[1:], Env: l.ctrl.Env()})
}

func (l *liveStreamer) HasSession(tileID string) (bool, error)    { return l.ctrl.HasSession(tileID) }
func (l *liveStreamer) Kill(tileID string) error                  { return l.ctrl.KillSession(tileID) }
func (l *liveStreamer) ListLiveTileIDs() ([]string, error)        { return l.ctrl.ListSessions() }
func (l *liveStreamer) PaneCommand(tileID string) (string, error) { return l.ctrl.PaneCommand(tileID) }

// Manager owns the single live PTY per tile and the takeover semantics: a
// refresh from another pane evicts the previous holder but reuses the SAME PTY
// (the tmux session is alive by construction) and forces a repaint, because
// the new holder's terminal is empty and tmux has no way to know a viewer
// changed. It is the plugin-side half of OpenShell.
type Manager struct {
	streamer Streamer
	mu       sync.Mutex
	active   map[string]*entry
}

type entry struct {
	session Session
	stopOld chan struct{} // closed when a takeover evicts the current holder
}

// NewManager wraps a Streamer. A nil Streamer is not allowed; callers that have
// no shell backend should leave the Manager itself nil.
func NewManager(s Streamer) *Manager {
	return &Manager{streamer: s, active: map[string]*entry{}}
}

// Acquire returns the live session for tileID. Three outcomes:
//   - an active holder exists  → takeover: signal it to exit, reuse the PTY,
//     and force a repaint for the new holder (see below).
//   - no holder, session alive → open a fresh PTY in attach mode.
//   - no holder, no session    → open in create mode iff allowCreate, else
//     ErrSessionGone.
//
// allowCreate is the caller's intent: true for a fresh tile (no snapshot yet, a
// new bash is expected), false for a snapshotted tile (don't fabricate state
// behind the JPEG). The returned channel is closed when a later Acquire takes
// over; pump until the session's Done, that channel, or the request ends.
func (m *Manager) Acquire(tileID string, allowCreate bool, cols, rows uint16) (Session, chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.active[tileID]; ok {
		close(e.stopOld)
		e.stopOld = make(chan struct{})
		// The PTY is reused, so tmux cannot see that the VIEWER changed:
		// nothing repaints, and the new pane's terminal — a fresh, empty
		// one — stays blank until something else happens to resize it.
		// Bounce the winsize (one row taller, then back): the kernel
		// raises SIGWINCH only on a REAL change, and that is what makes
		// tmux repaint the whole screen for whoever is watching now.
		// Height-only, so nothing rewraps on the way through.
		_ = e.session.Resize(cols, rows+1)
		_ = e.session.Resize(cols, rows)
		return e.session, e.stopOld, nil
	}

	alive, err := m.streamer.HasSession(tileID)
	if err != nil {
		return nil, nil, fmt.Errorf("shellsvc: probe tile %s: %w", tileID, err)
	}
	mode := tmux.ModeAttach
	if !alive {
		if !allowCreate {
			return nil, nil, ErrSessionGone
		}
		mode = tmux.ModeCreate
	}
	sess, err := m.streamer.OpenSession(tileID, mode, cols, rows)
	if err != nil {
		return nil, nil, err
	}
	m.active[tileID] = &entry{session: sess, stopOld: make(chan struct{})}
	return sess, m.active[tileID].stopOld, nil
}

// Release is the inverse of Acquire. If this holder still owns the entry (no
// takeover happened mid-flight) the PTY-side session is closed — which kills the
// gridwell-spawned tmux CLIENT but leaves the tmux SERVER + bash running, so the
// next refresh re-attaches to the same state — and onDetach (best-effort title
// capture) fires. On takeover this is a no-op: the new holder keeps the session.
func (m *Manager) Release(tileID string, mySession Session, myStopOld chan struct{}, onDetach func()) {
	m.mu.Lock()
	e, ok := m.active[tileID]
	matches := ok && e.session == mySession && e.stopOld == myStopOld
	if matches {
		delete(m.active, tileID)
	}
	m.mu.Unlock()
	if matches {
		log.Printf("[shellsvc] detach tile=%s", tileID)
		_ = mySession.Close()
		if onDetach != nil {
			onDetach()
		}
	}
}

// HasSession reports whether the tile's tmux session exists right now.
func (m *Manager) HasSession(tileID string) (bool, error) { return m.streamer.HasSession(tileID) }

// Kill removes the tile's tmux session. Idempotent.
func (m *Manager) Kill(tileID string) error { return m.streamer.Kill(tileID) }

// PaneCommand returns the foreground command of the tile's session (e.g.
// "claude", "vim"), or "" if gone. Used to label a frozen shell on detach.
func (m *Manager) PaneCommand(tileID string) (string, error) { return m.streamer.PaneCommand(tileID) }

// CleanupOrphans kills tmux sessions whose tile id no longer exists. exists is
// queried per live session; a tile that is GONE had its row deleted while the
// session leaked (delete raced a crash). Returns the count killed and the first
// error (best-effort: a per-session failure doesn't abort the pass).
func (m *Manager) CleanupOrphans(_ context.Context, exists func(tileID string) (bool, error)) (int, error) {
	live, err := m.streamer.ListLiveTileIDs()
	if err != nil {
		return 0, fmt.Errorf("list live sessions: %w", err)
	}
	killed := 0
	var firstErr error
	for _, id := range live {
		ok, err := exists(id)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("exists tile %s: %w", id, err)
			}
			continue
		}
		if ok {
			continue
		}
		if err := m.streamer.Kill(id); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("kill orphan tile %s: %w", id, err)
			}
			continue
		}
		killed++
	}
	return killed, firstErr
}

// ClampSize clamps a requested cols/rows to the PTY minimums, substituting the
// shell defaults for zero/too-small values.
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
