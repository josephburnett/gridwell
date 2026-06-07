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
)

// fakeShellStreamer is an in-memory shellStreamer. Records every
// session opened so tests can assert on tileID, initial size, and
// freeze-time cwd.
type fakeShellStreamer struct {
	mu       sync.Mutex
	sessions []*fakeShellSession
	// nextCwdResult, if non-empty, is what each created session's Cwd()
	// returns — lets tests fake "user typed cd /tmp" without driving
	// the shell.
	nextCwdResult string
}

func newFakeShellStreamer() *fakeShellStreamer {
	return &fakeShellStreamer{}
}

func (f *fakeShellStreamer) OpenSession(tid int64, cwd string, cols, rows uint16) (shellSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &fakeShellSession{
		tileID:        tid,
		initialCwd:    cwd,
		initialCols:   cols,
		initialRows:   rows,
		outCh:         make(chan []byte, 16),
		done:          make(chan struct{}),
		freezeCwd:     f.nextCwdResult,
		inputReceived: make(chan struct{}, 32),
	}
	f.sessions = append(f.sessions, s)
	return s, nil
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

type fakeShellSession struct {
	tileID      int64
	initialCwd  string
	initialCols uint16
	initialRows uint16

	mu        sync.Mutex
	inputs    [][]byte
	resizes   [][2]uint16
	closed    bool
	freezeCwd string

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

func (s *fakeShellSession) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freezeCwd
}

func (s *fakeShellSession) setFreezeCwd(cwd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezeCwd = cwd
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
	// Closing the output channel models the real session's pump
	// goroutine exiting on PTY EOF — lets the server's writer loop
	// detect the end of the stream and exit cleanly.
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

// TestShellStreamRefusesNonShellTile: the WS handler must check the
// tile kind before opening a session — wiring it up against a URL
// tile would otherwise leak a PTY to the wrong tile.
func TestShellStreamRefusesNonShellTile(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	srv.SetShellStreamer(newFakeShellStreamer())
	// URL tile rather than a shell tile.
	urlTileID := createURLTileViaRPC(t, hs, root, "https://example.com")
	_, resp, err := websocket.Dial(context.Background(), shellStreamURL(hs, urlTileID, 80, 24), nil)
	if err == nil {
		t.Fatal("dial against URL tile succeeded; expected 400")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", resp)
	}
}

// TestShellStreamOpensWithTileCwd: the WS handler must read the tile's
// stored shell_cwd and pass it to the session, so a refresh of a
// previously frozen shell resumes in the right directory.
func TestShellStreamOpensWithTileCwd(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	srv.SetShellStreamer(fake)

	tileID := createShellTileViaRPC(t, hs, root)
	// Stash a specific cwd on the tile to verify it round-trips.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	tile, err := cl.SetShellCwd(context.Background(), &rpc.SetShellCwdRequest{
		TileID: tileID, Version: 0, ShellCwd: "/tmp/work",
	})
	if err != nil {
		t.Fatalf("SetShellCwd: %v", err)
	}
	_ = tile

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
	if s.initialCwd != "/tmp/work" {
		t.Errorf("session initialCwd = %q, want /tmp/work", s.initialCwd)
	}
	if s.initialCols != 100 || s.initialRows != 40 {
		t.Errorf("session size = %dx%d, want 100x40", s.initialCols, s.initialRows)
	}
}

// TestShellStreamForwardsOutputBytes: PTY output reaches the client as
// binary WebSocket frames, bytes intact.
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

// TestShellStreamForwardsStdinBytes: binary frames from the client are
// passed through to the PTY stdin verbatim.
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
	// Arrow keys + Ctrl-C — bytes that aren't typeable but must flow
	// through unchanged.
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

	// Big resize: passes through unchanged.
	big, _ := json.Marshal(rpc.ShellStreamMessage{Kind: "resize", Cols: 132, Rows: 50})
	if err := conn.Write(ctx, websocket.MessageText, big); err != nil {
		t.Fatalf("write big: %v", err)
	}
	// Tiny resize: clamped to minShellCols / minShellRows.
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

// TestShellStreamClosePersistsCwd: at WS close, the handler must read
// session.Cwd() and persist it through SetShellCwd. This is the cwd-
// across-freeze invariant; if it ever regresses, refresh stops
// resuming in the user's last directory.
func TestShellStreamClosePersistsCwd(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	fake.nextCwdResult = "/var/log"
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

	// Reload the tile and check shell_cwd was persisted.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	gridResp, err := cl.GetGrid(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, tile := range gridResp.Tiles {
		if tile.ID == tileID {
			got = tile.ShellCwd
		}
	}
	if got != "/var/log" {
		t.Errorf("persisted shell_cwd = %q, want /var/log", got)
	}
}

// TestShellStreamTakeoverPreservesSession: opening WS B for the same
// tile while WS A is still attached must reuse the SAME bash process —
// state (cwd, environment, scrollback) belongs to the bash session,
// not the WS. WS A exits cleanly; WS B continues against the existing
// PTY.
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

	// Only one session must have been opened.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && fake.sessionCount() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := fake.sessionCount(); got != 1 {
		t.Errorf("session count = %d, want 1 (takeover should reuse)", got)
	}
	// And the session must still be live (not freeze-closed by the
	// takeover handoff).
	if sessA.isClosed() {
		t.Error("takeover closed the session; expected reuse")
	}
}

// TestShellStreamClosePersistsCwdSkipsEmpty: if /proc/<pid>/cwd
// can't be read (session already torn down, sandboxed reader), the
// handler must NOT call SetShellCwd with the empty string — that
// would wipe out the previously-stored cwd.
func TestShellStreamClosePersistsCwdSkipsEmpty(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeShellStreamer()
	fake.nextCwdResult = "" // simulate /proc-read failure
	srv.SetShellStreamer(fake)
	tileID := createShellTileViaRPC(t, hs, root)

	// Stash a known cwd that must NOT be overwritten.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.SetShellCwd(context.Background(), &rpc.SetShellCwdRequest{
		TileID: tileID, Version: 0, ShellCwd: "/persistent",
	}); err != nil {
		t.Fatalf("preload cwd: %v", err)
	}

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

	gridResp, err := cl.GetGrid(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range gridResp.Tiles {
		if tile.ID == tileID && tile.ShellCwd != "/persistent" {
			t.Errorf("shell_cwd = %q, want /persistent (empty Cwd() should be a no-op)", tile.ShellCwd)
		}
	}
}

// TestShellStreamRespectsMinSize: a client that connects with
// undersized cols/rows must have them clamped to the safety minimum so
// bash isn't started against a degenerate winsize.
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

// TestShellStreamWriteAfterCloseReturnsEOF: an internal post-close
// Write returns io.ErrClosedPipe; Output channel closes so the writer
// can detect end-of-stream. The WS handler must surface both as clean
// exits without log-spam errors.
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
