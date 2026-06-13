package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/tmux"
)

// fakeShellStreamer is an in-memory shellStreamer. Records every
// OpenSession call so tests can assert on tileID, mode, and size.
// HasSession is programmable per tile so tests can drive the
// create / attach / reject decision in the WS handler.
type fakeShellStreamer struct {
	mu sync.Mutex

	// alive maps tileID → "is there a live tmux session?". HasSession
	// reports from this map. Defaults to false (nothing exists).
	alive map[int64]bool

	sessions []*fakeShellSession
	killed   []int64

	// paneCommand is the canned PaneCommand answer (the foreground program).
	paneCommand string
}

func newFakeShellStreamer() *fakeShellStreamer {
	return &fakeShellStreamer{alive: map[int64]bool{}}
}

// setAlive programs HasSession's answer for tileID.
func (f *fakeShellStreamer) setAlive(tileID int64, alive bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[tileID] = alive
}

func (f *fakeShellStreamer) OpenSession(tid int64, mode tmux.Mode, cols, rows uint16) (shellSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &fakeShellSession{
		tileID:        tid,
		openMode:      mode,
		initialCols:   cols,
		initialRows:   rows,
		outCh:         make(chan []byte, 16),
		done:          make(chan struct{}),
		inputReceived: make(chan struct{}, 32),
	}
	f.sessions = append(f.sessions, s)
	// A successful OpenSession leaves the tmux session in alive state
	// (whether we just created it or attached to it).
	f.alive[tid] = true
	return s, nil
}

func (f *fakeShellStreamer) HasSession(tileID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[tileID], nil
}

func (f *fakeShellStreamer) Kill(tileID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, tileID)
	delete(f.alive, tileID)
	return nil
}

func (f *fakeShellStreamer) ListLiveTileIDs() ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for id, alive := range f.alive {
		if alive {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeShellStreamer) PaneCommand(tileID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paneCommand, nil
}

func (f *fakeShellStreamer) lastSession() *fakeShellSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

func (f *fakeShellStreamer) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeShellStreamer) killedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.killed))
	copy(out, f.killed)
	return out
}

type fakeShellSession struct {
	tileID      int64
	openMode    tmux.Mode
	initialCols uint16
	initialRows uint16

	mu      sync.Mutex
	inputs  [][]byte
	resizes [][2]uint16
	closed  bool

	outCh chan []byte
	done  chan struct{}

	inputReceived chan struct{}
}

func (s *fakeShellSession) Output() <-chan []byte { return s.outCh }

func (s *fakeShellSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	dup := make([]byte, len(p))
	copy(dup, p)
	s.inputs = append(s.inputs, dup)
	s.mu.Unlock()
	select {
	case s.inputReceived <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *fakeShellSession) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.resizes = append(s.resizes, [2]uint16{cols, rows})
	return nil
}

func (s *fakeShellSession) Done() <-chan struct{} { return s.done }

func (s *fakeShellSession) Close() error {
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

func (s *fakeShellSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeShellSession) inputsCopy() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.inputs))
	copy(out, s.inputs)
	return out
}

func (s *fakeShellSession) resizesCopy() [][2]uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]uint16, len(s.resizes))
	copy(out, s.resizes)
	return out
}

func createShellTileViaRPC(t *testing.T, hs *httptest.Server, root int64) int64 {
	t.Helper()
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	tile, err := cl.CreateShell(context.Background(), &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("CreateShell: %v", err)
	}
	return tile.ID
}

func shellStreamURL(hs *httptest.Server, tileID int64, cols, rows int) string {
	return "ws://" + strings.TrimPrefix(hs.URL, "http://") +
		"/rpc/ShellStream?tile_id=" + strconv.FormatInt(tileID, 10) +
		"&cols=" + strconv.Itoa(cols) + "&rows=" + strconv.Itoa(rows)
}

func waitForShellSession(t *testing.T, fake *fakeShellStreamer) *fakeShellSession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := fake.lastSession(); s != nil {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no shell session opened within 2s")
	return nil
}

// TestShellStreamRejectsCrossOriginConnect: a WebSocket carrying a
// foreign Origin (a web page open in some browser on the machine trying
// to reach into the bash PTY) must be refused before any session opens.
// WebSockets aren't subject to the same-origin policy on their own — the
// server has to enforce it. Previously InsecureSkipVerify:true skipped it.
func TestShellStreamRejectsCrossOriginConnect(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	hdr := http.Header{}
	hdr.Set("Origin", "http://evil.example")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24),
		&websocket.DialOptions{HTTPHeader: hdr})
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("cross-origin connect accepted; want rejection")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if n := fake.sessionCount(); n != 0 {
		t.Errorf("opened %d sessions on a rejected cross-origin connect, want 0", n)
	}
}

// TestShellStreamAllowsLoopbackOrigin: the renderer's own origin (and any
// loopback origin) is accepted — the same-origin path the real app uses.
func TestShellStreamAllowsLoopbackOrigin(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	hdr := http.Header{}
	// A loopback origin on a different port: not literally same-origin, but
	// the loopback allowlist accepts it (all of 127.0.0.1 is the local host).
	hdr.Set("Origin", "http://127.0.0.1:9999")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24),
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("loopback-origin connect rejected: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	waitForShellSession(t, fake)
}

// TestShellStreamRefusesWhenStreamerMissing: until SetShellStreamer
// has been called, the WS endpoint must 503 — same defensive shape
// the URL endpoint uses.
func TestShellStreamRefusesWhenStreamerMissing(t *testing.T) {
	_, hs, _ := streamTestServer(t)
	_, resp, err := websocket.Dial(context.Background(), shellStreamURL(hs, 1, 80, 24), nil)
	if err == nil {
		t.Fatal("dial without streamer succeeded; expected 503")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %v, want 503", resp)
	}
}

// TestCaptureShellTitleStampsLabel: on detach the server stamps the
// tmux foreground command (e.g. "claude") into the tile's label, the way
// URL tiles capture the page title.
func TestCaptureShellTitleStampsLabel(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	fake.paneCommand = "claude"
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	srv.captureShellTitle(tileID)

	tile, err := srv.store.GetTile(context.Background(), tileID)
	if err != nil {
		t.Fatalf("get tile: %v", err)
	}
	if tile.AltText != "claude" {
		t.Errorf("alt_text = %q, want %q", tile.AltText, "claude")
	}
}

// TestShellStreamRefusesNonShellTile: the WS handler must check the
// tile kind before opening a session — wiring it up against a URL
// tile would otherwise leak a session to the wrong tile.
func TestShellStreamRefusesNonShellTile(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	srv.SetShellStreamer(newFakeShellStreamer())
	urlTileID := createURLTileViaRPC(t, hs, root, "https://example.com")
	_, resp, err := websocket.Dial(context.Background(), shellStreamURL(hs, urlTileID, 80, 24), nil)
	if err == nil {
		t.Fatal("dial against URL tile succeeded; expected 400")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", resp)
	}
}

// TestShellStreamFreshTileOpensInCreateMode: a tile with no snapshot
// yet (PreviewBlobID == 0) is "fresh" — the WS handler must let the
// streamer create a new tmux session. This is the only path where
// silently spawning new bash is correct.
func TestShellStreamFreshTileOpensInCreateMode(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 100, 40), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)
	if s.tileID != tileID {
		t.Errorf("session tileID = %d, want %d", s.tileID, tileID)
	}
	if s.openMode != tmux.ModeCreate {
		t.Errorf("openMode = %v, want ModeCreate (fresh tile)", s.openMode)
	}
	if s.initialCols != 100 || s.initialRows != 40 {
		t.Errorf("session size = %dx%d, want 100x40", s.initialCols, s.initialRows)
	}
}

// TestShellStreamSnapshottedTileWithLiveSessionOpensInAttachMode:
// a tile that has a JPEG snapshot AND whose tmux session is still
// alive must attach to that session. ModeAttach (not ModeCreate)
// guarantees we never silently spawn a new bash on top of an existing
// session's state.
func TestShellStreamSnapshottedTileWithLiveSessionOpensInAttachMode(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	// Stash a JPEG so the tile reads as "previously activated".
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.SetShellPreview(context.Background(), &rpc.SetShellPreviewRequest{
		TileID: tileID, JPEG: []byte("snap"),
	}); err != nil {
		t.Fatalf("SetShellPreview: %v", err)
	}
	// And mark the tmux session as alive.
	fake.setAlive(tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)
	if s.openMode != tmux.ModeAttach {
		t.Errorf("openMode = %v, want ModeAttach (snapshotted tile with live session)", s.openMode)
	}
}

// TestShellStreamSnapshottedTileWithDeadSessionIsRejected: the
// "session is gone, can't bring it back" case. The wasm shouldn't
// have opened the WS at all (its ShellSessionAlive probe should have
// told it not to show the refresh button), but if it does, the
// server must refuse rather than silently spawning a fresh bash on
// top of the JPEG.
func TestShellStreamSnapshottedTileWithDeadSessionIsRejected(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	// Snapshot present, but no tmux session alive.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.SetShellPreview(context.Background(), &rpc.SetShellPreviewRequest{
		TileID: tileID, JPEG: []byte("snap"),
	}); err != nil {
		t.Fatalf("SetShellPreview: %v", err)
	}
	fake.setAlive(tileID, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	// The WS handshake itself succeeds (we accept before deciding),
	// then the server closes with PolicyViolation. Either path is
	// fine from the wasm's perspective. Assert we did NOT spawn a
	// session.
	if err == nil {
		_ = conn.Close(websocket.StatusGoingAway, "")
	}
	// Drain a moment so the server can finish its decision.
	time.Sleep(150 * time.Millisecond)
	if fake.sessionCount() != 0 {
		t.Errorf("session spawned on rejected refresh; sessionCount = %d", fake.sessionCount())
	}
}

// TestShellStreamForwardsOutputBytes: PTY output reaches the client
// as binary WebSocket frames, bytes intact.
func TestShellStreamForwardsOutputBytes(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)
	s.outCh <- []byte("\x1b[1;33mhello\x1b[0m")

	mt, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.MessageBinary {
		t.Errorf("frame type = %v, want binary", mt)
	}
	if string(data) != "\x1b[1;33mhello\x1b[0m" {
		t.Errorf("got %q, want ANSI-escaped hello", data)
	}
}

// TestShellStreamForwardsStdinBytes: binary frames from the client
// are passed through to the PTY stdin verbatim.
func TestShellStreamForwardsStdinBytes(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)
	payload := []byte("\x1b[A\x1b[B\x03")
	if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-s.inputReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("session never received stdin")
	}
	ins := s.inputsCopy()
	if len(ins) != 1 || string(ins[0]) != string(payload) {
		t.Errorf("got %d inputs %q, want one %q", len(ins), ins, payload)
	}
}

// TestShellStreamResizeMessage: text-frame control message resizes
// the PTY. Cols/rows below the safety minimum are clamped.
func TestShellStreamResizeMessage(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)

	big, _ := json.Marshal(rpc.ShellStreamMessage{Kind: "resize", Cols: 132, Rows: 50})
	if err := conn.Write(ctx, websocket.MessageText, big); err != nil {
		t.Fatalf("write big: %v", err)
	}
	tiny, _ := json.Marshal(rpc.ShellStreamMessage{Kind: "resize", Cols: 4, Rows: 2})
	if err := conn.Write(ctx, websocket.MessageText, tiny); err != nil {
		t.Fatalf("write tiny: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(s.resizesCopy()) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	rs := s.resizesCopy()
	if len(rs) != 2 {
		t.Fatalf("got %d resizes, want 2 (saw %+v)", len(rs), rs)
	}
	if rs[0] != [2]uint16{132, 50} {
		t.Errorf("big resize = %v, want [132 50]", rs[0])
	}
	if rs[1] != [2]uint16{minShellCols, minShellRows} {
		t.Errorf("tiny resize = %v, want clamped to [%d %d]", rs[1], minShellCols, minShellRows)
	}
}

// TestShellStreamCloseDetachesNotKills: at WS close (ascent), the
// handler must close the PTY-side session (detaching the tmux
// client) but must NOT call Kill (which would destroy the tmux
// session and bash inside). The "bash survives ascent" invariant
// rides entirely on this.
func TestShellStreamCloseDetachesNotKills(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	s := waitForShellSession(t, fake)
	_ = conn.Close(websocket.StatusNormalClosure, "ascent")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !s.isClosed() {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.isClosed() {
		t.Fatal("session never closed after WS close")
	}
	// Kill must NOT have been called — that's the tile-delete path,
	// not the ascent path. Bash inside tmux must keep running.
	if killed := fake.killedIDs(); len(killed) != 0 {
		t.Errorf("Kill called on ascent: %v; want no kills (detach only)", killed)
	}
}

// TestShellStreamTakeoverPreservesSession: opening WS B for the
// same tile while WS A is still attached must reuse the SAME PTY —
// state belongs to the underlying tmux session. WS A exits cleanly;
// WS B continues against the existing session.
func TestShellStreamTakeoverPreservesSession(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	connA, _, err := websocket.Dial(ctxA, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer connA.Close(websocket.StatusNormalClosure, "")
	sessA := waitForShellSession(t, fake)

	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	connB, _, err := websocket.Dial(ctxB, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && fake.sessionCount() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := fake.sessionCount(); got != 1 {
		t.Errorf("session count = %d, want 1 (takeover should reuse)", got)
	}
	if sessA.isClosed() {
		t.Error("takeover closed the session; expected reuse")
	}
}

// TestShellStreamRespectsMinSize: a client that connects with
// undersized cols/rows must have them clamped to the safety minimum.
func TestShellStreamRespectsMinSize(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 1, 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForShellSession(t, fake)
	if s.initialCols < minShellCols || s.initialRows < minShellRows {
		t.Errorf("initial size = %dx%d, want clamped to >= %dx%d",
			s.initialCols, s.initialRows, minShellCols, minShellRows)
	}
}

// TestCleanupOrphanedShellSessions: a tmux session whose tile id
// has no matching shell row in the DB must be killed; sessions
// whose tile id IS still present must be left alone. This is the
// bound on the "crash during DeleteTile leaves a session forever"
// leak.
func TestCleanupOrphanedShellSessions(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)

	// Real shell tile in the DB.
	live := createShellTileViaRPC(t, hs, root)
	fake.setAlive(live, true)
	// Orphan: alive on the socket, no matching DB row.
	const orphan int64 = 999999
	fake.setAlive(orphan, true)

	killed, err := srv.CleanupOrphanedShellSessions(context.Background())
	if err != nil {
		t.Fatalf("CleanupOrphanedShellSessions: %v", err)
	}
	if killed != 1 {
		t.Errorf("killed = %d, want 1 (only the orphan)", killed)
	}
	got := fake.killedIDs()
	if len(got) != 1 || got[0] != orphan {
		t.Errorf("killed ids = %v, want [%d]", got, orphan)
	}
	// The live tile's session must still be marked alive.
	if alive, _ := fake.HasSession(live); !alive {
		t.Error("live tile's session was killed by orphan cleanup")
	}
}

// TestCleanupOrphanedShellSessionsNoOpWithoutStreamer: defensive —
// the cleanup pass runs before SetShellStreamer in some test setups
// and must not crash.
func TestCleanupOrphanedShellSessionsNoOpWithoutStreamer(t *testing.T) {
	srv, _, _ := streamTestServer(t)
	killed, err := srv.CleanupOrphanedShellSessions(context.Background())
	if err != nil || killed != 0 {
		t.Errorf("CleanupOrphanedShellSessions without streamer = (%d, %v); want (0, nil)", killed, err)
	}
}

// TestDeleteTileKillsShellSession locks in the tile-delete cleanup:
// without it, deleting a tile would leave its tmux session alive on
// the gridwell socket, leaking bash processes and scrollback across
// gridwell restarts. The handler calls Kill unconditionally — Kill
// is idempotent on the tmux side, so a no-op delete of a non-shell
// tile is fine.
func TestDeleteTileKillsShellSession(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)
	fake.setAlive(tileID, true)

	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if err := cl.DeleteTile(context.Background(), &rpc.DeleteTileRequest{
		TileID: tileID, Version: 0,
	}); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	killed := fake.killedIDs()
	if len(killed) != 1 || killed[0] != tileID {
		t.Errorf("Kill not called for deleted shell tile %d; got %v", tileID, killed)
	}
}

// TestShellSessionAliveReportsStreamerProbe: the unary RPC must
// delegate straight to shellStreamer.HasSession. Two scenarios:
//   1. session marked alive → alive=true.
//   2. session not alive    → alive=false.
// This is the contract the wasm's refresh-button gating reads from.
func TestShellSessionAliveReportsStreamerProbe(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// Default: nothing alive yet.
	res, err := cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: tileID})
	if err != nil {
		t.Fatalf("ShellSessionAlive (default): %v", err)
	}
	if res.Alive {
		t.Errorf("alive = true for fresh tile; want false")
	}

	fake.setAlive(tileID, true)
	res, err = cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: tileID})
	if err != nil {
		t.Fatalf("ShellSessionAlive (live): %v", err)
	}
	if !res.Alive {
		t.Errorf("alive = false after setAlive(true); want true")
	}
}

// TestShellSessionAliveWithoutStreamerReportsFalse: defensive — the
// RPC must not crash if SetShellStreamer was never called, and it
// must report not-alive so the wasm hides the refresh button.
func TestShellSessionAliveWithoutStreamerReportsFalse(t *testing.T) {
	_, hs, _ := streamTestServer(t)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	res, err := cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: 1})
	if err != nil {
		t.Fatalf("ShellSessionAlive: %v", err)
	}
	if res.Alive {
		t.Errorf("alive = true without streamer; want false")
	}
}

// TestShellStreamWriteAfterCloseReturnsEOF: an internal post-close
// Write returns io.ErrClosedPipe; Output channel closes so the
// writer can detect end-of-stream.
func TestShellStreamWriteAfterCloseReturnsEOF(t *testing.T) {
	s := &fakeShellSession{outCh: make(chan []byte), done: make(chan struct{})}
	s.Close()
	if _, err := s.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write after Close: %v, want io.ErrClosedPipe", err)
	}
	if _, ok := <-s.Output(); ok {
		t.Errorf("Output() not closed after Close")
	}
}
