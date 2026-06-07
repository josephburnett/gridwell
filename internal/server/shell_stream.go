package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/shelldriver"
)

// shellStreamer is the subset of the shell driver this handler needs.
// Stubbed in tests so the WS handler can run against an in-memory
// session without spawning a real PTY.
type shellStreamer interface {
	OpenSession(tileID int64, cwd string, cols, rows uint16) (shellSession, error)
}

// shellSession is the per-tile handle the ShellStream handler holds.
// Mirrors shelldriver.Session through an interface so tests can swap
// in a fake. Output is a channel rather than a blocking syscall so
// the WS write loop can cancel-safely select on it together with the
// handler's context (required for the takeover path: when a refresh
// from another pane evicts the current WS, we need the writer to
// return immediately without leaving a goroutine blocked on a PTY
// read).
type shellSession interface {
	Output() <-chan []byte
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Cwd() string
	Done() <-chan struct{}
	Close() error
}

// SetShellStreamer wires a streamer into the server. Production passes
// ShellStreamerFromDriver(); tests pass an in-memory fake.
func (s *Server) SetShellStreamer(d shellStreamer) { s.shellStreamer = d }

// ShellStreamerFromDriver is the production adapter — opens a real
// PTY through shelldriver.Start.
func ShellStreamerFromDriver() shellStreamer { return &liveShellStreamer{} }

type liveShellStreamer struct{}

func (liveShellStreamer) OpenSession(tileID int64, cwd string, cols, rows uint16) (shellSession, error) {
	s, err := shelldriver.Start(shelldriver.Config{Cwd: cwd, Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// shellSessionEntry mirrors urlSessionEntry — the live session plus a
// stopOld channel closed when a takeover evicts the current WS holder.
type shellSessionEntry struct {
	session shellSession
	stopOld chan struct{}
}

const (
	minShellCols       = 20
	minShellRows       = 5
	defaultShellCols   = 80
	defaultShellRows   = 24
	shellWriteTimeout  = 30 * time.Second
	shellFreezeTimeout = 5 * time.Second
)

// acquireShellSession returns the live session for tileID. If one
// exists the previous WS handler is signalled to exit; the SAME bash
// process keeps running (state isn't lost across pane swaps). Without
// an existing session, a fresh PTY is started in the tile's stored
// shell_cwd.
func (s *Server) acquireShellSession(tileID int64, cwd string, cols, rows uint16) (shellSession, chan struct{}, error) {
	s.activeShellMu.Lock()
	defer s.activeShellMu.Unlock()
	if entry, ok := s.activeShellSessions[tileID]; ok {
		close(entry.stopOld)
		entry.stopOld = make(chan struct{})
		return entry.session, entry.stopOld, nil
	}
	sess, err := s.shellStreamer.OpenSession(tileID, cwd, cols, rows)
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
// session is closed AND the freeze path runs: read /proc/<pid>/cwd
// and persist it via store.SetShellCwd so the next refresh resumes
// there. On takeover this is a no-op — the new WS handler takes over
// freeze responsibility.
func (s *Server) releaseShellSession(tileID int64, mySession shellSession, myStopOld chan struct{}) {
	s.activeShellMu.Lock()
	entry, ok := s.activeShellSessions[tileID]
	matches := ok && entry.session == mySession && entry.stopOld == myStopOld
	if matches {
		delete(s.activeShellSessions, tileID)
	}
	s.activeShellMu.Unlock()
	if matches {
		s.closeShellSession(tileID, mySession)
	}
}

// closeShellSession is the freeze path: capture cwd BEFORE Close()
// (the PTY teardown invalidates /proc/<pid>/cwd), persist it, then
// kill bash. Errors are logged but never block the teardown.
func (s *Server) closeShellSession(tileID int64, session shellSession) {
	log.Printf("[shellstream] freeze tile=%d", tileID)
	cwd := session.Cwd()
	if cwd != "" {
		ctx, cancel := context.WithTimeout(context.Background(), shellFreezeTimeout)
		defer cancel()
		// Reload the tile so we have a fresh version for the optimistic
		// concurrency check; the version may have moved since this WS
		// was opened (the client might have set the preview JPEG
		// concurrently via the Connect SetShellPreview RPC).
		t, err := s.store.GetTile(ctx, tileID)
		if err != nil {
			log.Printf("[shellstream] cwd-load-err tile=%d err=%v", tileID, err)
		} else {
			_, err = s.store.SetShellCwd(ctx, &rpc.SetShellCwdRequest{
				TileID: tileID, Version: t.Version, ShellCwd: cwd,
			})
			if err != nil {
				log.Printf("[shellstream] cwd-save-err tile=%d cwd=%q err=%v", tileID, cwd, err)
			}
		}
	}
	_ = session.Close()
}

// shellStream is the /rpc/ShellStream WebSocket handler. Protocol:
//
//   - Binary frames in either direction: stdin (client→server) and
//     PTY output (server→client) — raw byte passthrough.
//   - Text frames, client→server: ShellStreamMessage (JSON), only
//     "resize" is recognized today.
//   - There is no server→client text channel; the PTY output stream is
//     the only thing the client renders.
func (s *Server) shellStream(w http.ResponseWriter, r *http.Request) {
	if s.shellStreamer == nil {
		http.Error(w, "shell driver not configured", http.StatusServiceUnavailable)
		return
	}
	tileIDStr := r.URL.Query().Get("tile_id")
	tileID, err := strconv.ParseInt(tileIDStr, 10, 64)
	if err != nil || tileID <= 0 {
		http.Error(w, "missing or invalid tile_id", http.StatusBadRequest)
		return
	}
	tile, err := s.store.GetTile(r.Context(), tileID)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if tile.Kind != rpc.KindShell {
		http.Error(w, "not a shell tile", http.StatusBadRequest)
		return
	}
	cols, rows := parseShellSize(r.URL.Query())

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, myStopOld, err := s.acquireShellSession(tileID, tile.ShellCwd, cols, rows)
	if err != nil {
		log.Printf("[shellstream] open-err tile=%d err=%v", tileID, err)
		_ = conn.Close(websocket.StatusInternalError, "open session failed")
		return
	}
	log.Printf("[shellstream] open tile=%d cwd=%q", tileID, tile.ShellCwd)

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
// to a minimum so a misbehaving client can't drive the PTY to a winsize
// that breaks bash. Empty / unparseable / too-small values become the
// shell defaults (80x24).
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
