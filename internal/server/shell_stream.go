package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/shelldriver"
	"github.com/josephburnett/gridwell/internal/tmux"
)

// shellStreamer is the subset of the shell-backing layer this handler
// needs. Stubbed in tests so the WS handler can run without spawning
// a real tmux/PTY pair.
//
// OpenSession spawns a fresh PTY connected to the tile's tmux session
// in either ModeCreate (new session if missing, attach if present) or
// ModeAttach (attach-only; errors if the session doesn't exist). The
// handler chooses the mode per the design: ModeCreate for tiles with
// no snapshot yet, ModeAttach for tiles whose tmux session was
// previously launched and might or might not still be alive.
//
// HasSession reports whether the tile's tmux session exists right now.
// Used by the ShellSessionAlive RPC to gate the wasm's refresh button.
//
// Kill removes the tile's tmux session. Idempotent. Called from
// DeleteTile and from the startup orphan cleanup.
type shellStreamer interface {
	OpenSession(tileID int64, mode tmux.Mode, cols, rows uint16) (shellSession, error)
	HasSession(tileID int64) (bool, error)
	Kill(tileID int64) error
	// ListLiveTileIDs returns the tile ids of every gridwell-owned
	// tmux session currently alive on the backing socket. Used by the
	// startup orphan-cleanup pass to find sessions whose tiles no
	// longer exist.
	ListLiveTileIDs() ([]int64, error)
	// PaneCommand returns the foreground command of the tile's session
	// (e.g. "claude", "vim", "bash"), or "" if the session is gone. Used
	// to label the frozen shell tile on detach, the way URL tiles capture
	// the page title.
	PaneCommand(tileID int64) (string, error)
}

// shellSession is the per-tile handle the ShellStream handler holds.
// Mirrors shelldriver.Session through an interface so tests can swap
// in a fake. Output is a channel rather than a blocking syscall so
// the WS write loop can cancel-safely select on it together with the
// handler's context (required for the takeover path).
type shellSession interface {
	Output() <-chan []byte
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Done() <-chan struct{}
	Close() error
}

// ErrShellSessionGone is returned by acquireShellSession when the
// tile has been snapshotted but its tmux session is no longer alive.
// The WS handler maps this to a 410 Gone so the wasm flips the
// refresh button off — recovery is impossible (bash is gone, only the
// JPEG remains).
var ErrShellSessionGone = errors.New("shell session no longer alive")

// SetShellStreamer wires a streamer into the server. Production
// passes a streamer constructed from a tmux.Controller via
// NewLiveShellStreamer; tests pass an in-memory fake.
func (s *Server) SetShellStreamer(d shellStreamer) { s.shellStreamer = d }

// NewLiveShellStreamer is the production constructor. It composes
// tmux argv via the controller and execs that argv through
// shelldriver, so callers get a PTY-backed tmux client that survives
// detach via the underlying tmux server. Pass the same controller
// the orphan cleanup uses so HasSession queries agree with session
// reality.
func NewLiveShellStreamer(ctrl *tmux.Controller) shellStreamer {
	return &liveShellStreamer{ctrl: ctrl}
}

type liveShellStreamer struct {
	ctrl *tmux.Controller
}

func (l *liveShellStreamer) OpenSession(tileID int64, mode tmux.Mode, cols, rows uint16) (shellSession, error) {
	argv := l.ctrl.Args(tileID, mode, cols, rows, "")
	if len(argv) == 0 {
		return nil, fmt.Errorf("shellstream: empty tmux argv for tile %d mode %v", tileID, mode)
	}
	s, err := shelldriver.Start(shelldriver.Config{
		Cols: cols, Rows: rows,
		BashPath: argv[0],
		Args:     argv[1:],
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (l *liveShellStreamer) HasSession(tileID int64) (bool, error) {
	return l.ctrl.HasSession(tileID)
}

func (l *liveShellStreamer) Kill(tileID int64) error {
	return l.ctrl.KillSession(tileID)
}

func (l *liveShellStreamer) ListLiveTileIDs() ([]int64, error) {
	return l.ctrl.ListSessions()
}

func (l *liveShellStreamer) PaneCommand(tileID int64) (string, error) {
	return l.ctrl.PaneCommand(tileID)
}

// shellSessionEntry mirrors urlSessionEntry — the live session plus a
// stopOld channel closed when a takeover evicts the current WS holder.
type shellSessionEntry struct {
	session shellSession
	stopOld chan struct{}
}

// CleanupOrphanedShellSessions kills tmux sessions on the gridwell
// socket whose tile ids no longer exist in the store. Called once at
// startup to bound the leak when a tile delete races a gridwell
// crash. No-op if no streamer is wired up.
//
// Returns the count of sessions killed (for observability) and the
// first error encountered (or nil). A per-session kill error doesn't
// abort the pass — best-effort cleanup, the next startup catches
// what this one missed.
func (s *Server) CleanupOrphanedShellSessions(ctx context.Context) (int, error) {
	if s.shellStreamer == nil {
		return 0, nil
	}
	live, err := s.shellStreamer.ListLiveTileIDs()
	if err != nil {
		return 0, fmt.Errorf("list live sessions: %w", err)
	}
	if len(live) == 0 {
		return 0, nil
	}
	tileIDs, err := s.primary.AllShellTileIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list shell tiles: %w", err)
	}
	known := make(map[string]bool, len(tileIDs))
	for _, id := range tileIDs {
		known[id] = true
	}
	killed := 0
	var firstErr error
	for _, id := range live {
		if known[strconv.FormatInt(id, 10)] {
			continue
		}
		if err := s.shellStreamer.Kill(id); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("kill orphan tile %d: %w", id, err)
			}
			continue
		}
		killed++
	}
	return killed, firstErr
}

const (
	minShellCols      = 20
	minShellRows      = 5
	defaultShellCols  = 80
	defaultShellRows  = 24
	shellWriteTimeout = 30 * time.Second
)

// acquireShellSession returns the live session for tileID. Three
// outcomes:
//
//   - An active WS already holds a session: takeover. The previous
//     handler is signalled to exit; the SAME PTY is reused. The tmux
//     session is alive by construction (we're already talking to it).
//   - No active WS, but a tmux session exists: open a fresh PTY in
//     ModeAttach and return it.
//   - No active WS, no tmux session: open in ModeCreate only if
//     allowCreate is true. Otherwise return ErrShellSessionGone.
//
// allowCreate is the wasm-side intent encoded as a bool: tiles with
// no snapshot yet have allowCreate=true (fresh tile, user expects a
// new bash to spawn); tiles with a snapshot have allowCreate=false
// (we won't silently fabricate state behind the JPEG).
func (s *Server) acquireShellSession(tileID int64, allowCreate bool, cols, rows uint16) (shellSession, chan struct{}, error) {
	s.activeShellMu.Lock()
	defer s.activeShellMu.Unlock()
	if entry, ok := s.activeShellSessions[tileID]; ok {
		close(entry.stopOld)
		entry.stopOld = make(chan struct{})
		return entry.session, entry.stopOld, nil
	}

	alive, err := s.shellStreamer.HasSession(tileID)
	if err != nil {
		return nil, nil, fmt.Errorf("shellstream: probe tile %d: %w", tileID, err)
	}
	mode := tmux.ModeAttach
	if !alive {
		if !allowCreate {
			return nil, nil, ErrShellSessionGone
		}
		mode = tmux.ModeCreate
	}

	sess, err := s.shellStreamer.OpenSession(tileID, mode, cols, rows)
	if err != nil {
		return nil, nil, err
	}
	if s.activeShellSessions == nil {
		s.activeShellSessions = make(map[int64]*shellSessionEntry)
	}
	s.activeShellSessions[tileID] = &shellSessionEntry{
		session: sess,
		stopOld: make(chan struct{}),
	}
	return sess, s.activeShellSessions[tileID].stopOld, nil
}

// releaseShellSession is the inverse of acquireShellSession. If this
// handler still owns the entry (no takeover happened mid-flight), the
// PTY-side session is closed (which kills the gridwell-spawned tmux
// CLIENT but leaves the tmux SERVER + bash running, so the next
// refresh re-attaches to the same state). On takeover this is a
// no-op — the new WS handler keeps the same session.
func (s *Server) releaseShellSession(tileID int64, mySession shellSession, myStopOld chan struct{}) {
	s.activeShellMu.Lock()
	entry, ok := s.activeShellSessions[tileID]
	matches := ok && entry.session == mySession && entry.stopOld == myStopOld
	if matches {
		delete(s.activeShellSessions, tileID)
	}
	s.activeShellMu.Unlock()
	if matches {
		log.Printf("[shellstream] detach tile=%d", tileID)
		_ = mySession.Close()
		// Capture the foreground program (e.g. "claude") into the tile's
		// label, the way URL tiles capture the page title on close. The
		// bash + tmux server keep running after Close, so the query still
		// resolves. Best-effort and async so detach isn't blocked on it.
		go s.captureShellTitle(tileID)
	}
}

// captureShellTitle stamps the tile's label with its tmux session's
// foreground command. No-op if the session is gone or the query fails.
func (s *Server) captureShellTitle(tileID int64) {
	if s.shellStreamer == nil {
		return
	}
	cmd, err := s.shellStreamer.PaneCommand(tileID)
	if err != nil || cmd == "" {
		return
	}
	if err := s.primary.SetTileAlt(context.Background(), tileID, cmd); err != nil {
		log.Printf("[shellstream] set-title tile=%d cmd=%q err=%v", tileID, cmd, err)
	}
}

// shellStream is the /rpc/ShellStream WebSocket handler. Protocol:
//
//   - Binary frames in either direction: stdin (client→server) and
//     PTY output (server→client) — raw byte passthrough.
//   - Text frames, client→server: ShellStreamMessage (JSON), only
//     "resize" is recognized today.
//   - There is no server→client text channel; the PTY output stream
//     is the only thing the client renders.
func (s *Server) shellStream(w http.ResponseWriter, r *http.Request) {
	if s.shellStreamer == nil {
		http.Error(w, "shell driver not configured", http.StatusServiceUnavailable)
		return
	}
	// Shell tiles live in the primary localdb; the wire id is qualified.
	tileIDStr := stripUUID(r.URL.Query().Get("tile_id"), s.primaryUUID)
	tileID, err := strconv.ParseInt(tileIDStr, 10, 64)
	if err != nil || tileID <= 0 {
		http.Error(w, "missing or invalid tile_id", http.StatusBadRequest)
		return
	}
	tile, err := s.primary.GetTile(r.Context(), strconv.FormatInt(tileID, 10))
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if tile.Kind != rpc.KindShell {
		http.Error(w, "not a shell tile", http.StatusBadRequest)
		return
	}
	cols, rows := parseShellSize(r.URL.Query())

	// Decide whether silently spawning a fresh bash is allowed. A
	// tile with no snapshot yet (PreviewBlobID == 0) is a fresh tile
	// the user wants a session for; a tile with a snapshot has a
	// historical record we won't quietly overwrite by booting a new
	// shell. acquireShellSession turns this into ModeCreate vs
	// ModeAttach below.
	allowCreate := tile.PreviewBlobID == 0

	// Enforce same-origin. The renderer loads from http://127.0.0.1:<port>
	// — the same origin as this server (apps/desktop loadURL(origin+'/')) —
	// so its WebSocket is same-origin and accepted. A non-browser client
	// (the tests, a CLI) sends no Origin header and is also accepted. What
	// this rejects is the real threat: any web page open in any browser on
	// the machine cross-origin-connecting to /rpc/ShellStream to grab a bash
	// PTY. (Previously InsecureSkipVerify:true skipped this check entirely.)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*"},
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, myStopOld, err := s.acquireShellSession(tileID, allowCreate, cols, rows)
	if err != nil {
		log.Printf("[shellstream] open-err tile=%d allowCreate=%v err=%v", tileID, allowCreate, err)
		if errors.Is(err, ErrShellSessionGone) {
			_ = conn.Close(websocket.StatusPolicyViolation, "shell session gone")
		} else {
			_ = conn.Close(websocket.StatusInternalError, "open session failed")
		}
		return
	}
	log.Printf("[shellstream] attach tile=%d allowCreate=%v", tileID, allowCreate)

	// Cancel when the session dies, on takeover, or on client close.
	go func() {
		select {
		case <-session.Done():
			cancel()
		case <-myStopOld:
			cancel()
		case <-ctx.Done():
		}
	}()

	writerDone := make(chan struct{})
	go shellWriteLoop(ctx, conn, session, tileID, writerDone)
	shellReadLoop(ctx, conn, session, tileID)

	cancel()
	<-writerDone
	_ = conn.Close(websocket.StatusNormalClosure, "")
	s.releaseShellSession(tileID, session, myStopOld)
}

// parseShellSize reads cols/rows from the URL query string, clamping
// to a minimum so a misbehaving client can't drive the PTY to a
// winsize that breaks bash. Empty / unparseable / too-small values
// become the shell defaults (80x24).
func parseShellSize(q map[string][]string) (uint16, uint16) {
	parse := func(name string, dflt uint16, min uint16) uint16 {
		vs, ok := q[name]
		if !ok || len(vs) == 0 {
			return dflt
		}
		n, err := strconv.Atoi(vs[0])
		if err != nil || n <= 0 {
			return dflt
		}
		if n < int(min) {
			n = int(min)
		}
		if n > 65535 {
			n = 65535
		}
		return uint16(n)
	}
	return parse("cols", defaultShellCols, minShellCols), parse("rows", defaultShellRows, minShellRows)
}

// shellWriteLoop ferries PTY output to the WS as binary frames. Exits
// on context cancel (which fires on takeover via stopOld, or on the
// WS reader returning), on session death, or on a write error. Using
// the session's output channel rather than a blocking read lets the
// detach path return immediately without orphaning a goroutine on a
// PTY syscall.
func shellWriteLoop(ctx context.Context, conn *websocket.Conn, session shellSession, tileID int64, done chan<- struct{}) {
	defer close(done)
	out := session.Output()
	for {
		select {
		case <-ctx.Done():
			return
		case <-session.Done():
			return
		case chunk, ok := <-out:
			if !ok {
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, shellWriteTimeout)
			err := conn.Write(wctx, websocket.MessageBinary, chunk)
			wcancel()
			if err != nil {
				log.Printf("[shellstream] writer-exit tile=%d err=%v", tileID, err)
				return
			}
		}
	}
}

// shellReadLoop reads from the WS. Binary frames are forwarded as
// stdin; text frames are parsed as ShellStreamMessage control signals.
func shellReadLoop(ctx context.Context, conn *websocket.Conn, session shellSession, tileID int64) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("[shellstream] reader-exit tile=%d err=%v", tileID, err)
			return
		}
		switch mt {
		case websocket.MessageBinary:
			if _, err := session.Write(data); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					log.Printf("[shellstream] session-dead tile=%d", tileID)
					return
				}
				log.Printf("[shellstream] forward-err tile=%d err=%v", tileID, err)
			}
		case websocket.MessageText:
			var msg rpc.ShellStreamMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("[shellstream] bad-json tile=%d err=%v body=%q", tileID, err, string(data))
				continue
			}
			if msg.Kind == "resize" {
				cols, rows := msg.Cols, msg.Rows
				if cols < minShellCols {
					cols = minShellCols
				}
				if rows < minShellRows {
					rows = minShellRows
				}
				if err := session.Resize(cols, rows); err != nil {
					log.Printf("[shellstream] resize-err tile=%d cols=%d rows=%d err=%v", tileID, cols, rows, err)
				}
			}
		}
	}
}
