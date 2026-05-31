package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// streamTestServer wires up a fresh in-memory Server.
func streamTestServer(t *testing.T) (*Server, *httptest.Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	srv := New(st, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs, root
}

// fakeStreamer is a non-Chromium urlStreamer for HTTP-level tests.
type fakeStreamer struct {
	available bool
	mu        sync.Mutex
	sessions  []*fakeSession
}

func newFakeStreamer() *fakeStreamer {
	return &fakeStreamer{available: true}
}

func (f *fakeStreamer) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeStreamer) OpenSession(tid int64, url string, w, h int64) (urlSession, error) {
	s := &fakeSession{
		tileID:    tid,
		frames:    make(chan []byte, 4),
		navs:      make(chan string, 8),
		done:      make(chan struct{}),
		lastURL:   url,
		initialW:  w,
		initialH:  h,
		finalJPEG: []byte("final-jpeg-bytes"),
	}
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	return s, nil
}

func (f *fakeStreamer) lastSession() *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

type fakeSession struct {
	tileID int64

	mu       sync.Mutex
	inputs   []urldriver.InputEvent
	resizes  [][2]int64
	lastURL  string
	closed   bool
	captures int
	// cancelNextInputs makes the next N Input calls return
	// context.Canceled without recording the event, simulating a tab
	// mid-swap. Done() stays open so readLoop must treat these as
	// transient.
	cancelNextInputs int

	frames chan []byte
	navs   chan string
	done   chan struct{}

	initialW, initialH int64
	finalJPEG          []byte
}

func (s *fakeSession) Input(ev urldriver.InputEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelNextInputs > 0 {
		s.cancelNextInputs--
		return context.Canceled
	}
	s.inputs = append(s.inputs, ev)
	return nil
}

func (s *fakeSession) Resize(w, h int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, [2]int64{w, h})
	return nil
}

func (s *fakeSession) Frames() <-chan []byte { return s.frames }
func (s *fakeSession) Navs() <-chan string   { return s.navs }
func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) LastURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

func (s *fakeSession) setLastURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastURL = u
}

func (s *fakeSession) CaptureFinal(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captures++
	return s.finalJPEG, nil
}

func (s *fakeSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}

func (s *fakeSession) inputCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputs)
}

func (s *fakeSession) inputAt(i int) urldriver.InputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputs[i]
}

func (s *fakeSession) resizeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.resizes)
}

func (s *fakeSession) lastResize() (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resizes) == 0 {
		return 0, 0
	}
	r := s.resizes[len(s.resizes)-1]
	return r[0], r[1]
}

func (s *fakeSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeSession) captureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captures
}

// createURLTileViaRPC creates a URL tile through CreateURL.
func createURLTileViaRPC(t *testing.T, hs *httptest.Server, root int64, url string) int64 {
	t.Helper()
	var resp rpc.TileResponse
	st, body := callRPC(t, hs, "CreateURL", &rpc.CreateURLRequest{
		Path:   rpc.Path{},
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1,
		URL: url,
	}, &resp)
	if st != 200 {
		t.Fatalf("CreateURL status=%d body=%s", st, body)
	}
	return resp.Tile.ID
}

func urlStreamURL(hs *httptest.Server, tileID int64) string {
	return "ws://" + strings.TrimPrefix(hs.URL, "http://") +
		"/rpc/URLStream?tile_id=" + strconv.FormatInt(tileID, 10)
}

func waitForSession(t *testing.T, fake *fakeStreamer) *fakeSession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := fake.lastSession(); s != nil {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no session opened within 2s")
	return nil
}

func TestURLStreamRefusesWhenStreamerMissing(t *testing.T) {
	_, hs, _ := streamTestServer(t)
	_, resp, err := websocket.Dial(context.Background(), urlStreamURL(hs, 1), nil)
	if err == nil {
		t.Fatal("dial without streamer succeeded; expected 503")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %v, want 503", resp)
	}
}

func TestURLStreamOpensSession(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	if s.tileID != tileID {
		t.Errorf("session tileID = %d, want %d", s.tileID, tileID)
	}
	if s.initialW != defaultViewportW || s.initialH != defaultViewportH {
		t.Errorf("session viewport = %dx%d, want %dx%d", s.initialW, s.initialH, defaultViewportW, defaultViewportH)
	}
}

func TestURLStreamDeliversFramesAndNav(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	go func() {
		s.frames <- []byte{0xff, 0xd8, 0xff, 0xe0}
		s.navs <- "https://example.com/two"
	}()

	gotBinary, gotNav := false, false
	for !gotBinary || !gotNav {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (binary=%v nav=%v)", err, gotBinary, gotNav)
		}
		switch mt {
		case websocket.MessageBinary:
			if len(data) < 4 || string(data[:4]) != "\xff\xd8\xff\xe0" {
				t.Errorf("binary payload = %x, want JPEG magic prefix", data)
			}
			gotBinary = true
		case websocket.MessageText:
			var msg urlStreamServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode text frame: %v body=%s", err, data)
			}
			if msg.Kind != "nav" || msg.URL != "https://example.com/two" {
				t.Errorf("nav msg = %+v", msg)
			}
			gotNav = true
		}
	}
}

func TestURLStreamForwardsInput(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	payload, _ := json.Marshal(urlStreamMessage{
		Kind: string(urldriver.InputMouseDown),
		X:    17, Y: 23, Button: urldriver.MouseButtonLeft,
	})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.inputCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.inputCount(); got != 1 {
		t.Fatalf("got %d inputs, want 1", got)
	}
	ev := s.inputAt(0)
	if ev.Kind != urldriver.InputMouseDown || ev.X != 17 || ev.Y != 23 || ev.Button != urldriver.MouseButtonLeft {
		t.Errorf("input = %+v", ev)
	}
}

func TestURLStreamHandlesViewport(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	payload, _ := json.Marshal(urlStreamMessage{Kind: "viewport", Width: 1024, Height: 768})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.resizeCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.resizeCount(); got != 1 {
		t.Fatalf("got %d resizes, want 1", got)
	}
	if w, h := s.lastResize(); w != 1024 || h != 768 {
		t.Errorf("resize = %dx%d, want 1024x768", w, h)
	}
}

func TestURLStreamClosesSessionAndPersists(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	s := waitForSession(t, fake)
	s.setLastURL("https://example.com/last")

	_ = conn.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !s.isClosed() {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.isClosed() {
		t.Fatal("session not closed within 2s of WS disconnect")
	}
	if got := s.captureCount(); got != 1 {
		t.Errorf("CaptureFinal call count = %d, want 1", got)
	}

	tile, err := srv.store.GetTile(context.Background(), tileID)
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if tile.URLString != "https://example.com/last" {
		t.Errorf("url_string = %q, want %q", tile.URLString, "https://example.com/last")
	}
	jpeg, err := srv.store.GetTilePreview(context.Background(), tileID)
	if err != nil {
		t.Fatalf("GetTilePreview: %v", err)
	}
	if string(jpeg) != "final-jpeg-bytes" {
		t.Errorf("preview_jpeg = %q, want %q", string(jpeg), "final-jpeg-bytes")
	}
}

func (s *fakeSession) setCancelNextInputs(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelNextInputs = n
}

// waitForNSessions blocks until the fakeStreamer has at least n sessions,
// then returns the nth-from-last (index n-1) session.
func waitForNSessions(t *testing.T, fake *fakeStreamer, n int) *fakeSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		count := len(fake.sessions)
		fake.mu.Unlock()
		if count >= n {
			fake.mu.Lock()
			s := fake.sessions[n-1]
			fake.mu.Unlock()
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not see %d sessions within 3s", n)
	return nil
}

// TestURLStreamTakeoverClosesOldSession verifies that when a second WS client
// connects for the same tile_id, the first session is closed (the old WS
// receives a close frame) and the store persists the old session's data.
func TestURLStreamTakeoverClosesOldSession(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	// Connect WS A.
	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	connA, _, err := websocket.Dial(ctxA, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}

	// Wait for session A to be registered.
	sessionA := waitForSession(t, fake)
	sessionA.setLastURL("https://example.com/fromA")

	// Connect WS B for the same tile — this triggers the takeover.
	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	connB, _, err := websocket.Dial(ctxB, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close(websocket.StatusNormalClosure, "")

	// WS A must receive a close frame (takeover closes it cleanly).
	connA.CloseRead(ctxA)
	closedA := make(chan struct{})
	go func() {
		defer close(closedA)
		_, _, _ = connA.Read(ctxA) // will return once the server sends close
	}()
	select {
	case <-closedA:
	case <-time.After(3 * time.Second):
		t.Fatal("WS A did not receive a close frame within 3s of takeover")
	}

	// Session A must be closed and its data persisted.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !sessionA.isClosed() {
		time.Sleep(20 * time.Millisecond)
	}
	if !sessionA.isClosed() {
		t.Error("session A not closed after takeover")
	}
	if got := sessionA.captureCount(); got != 1 {
		t.Errorf("session A CaptureFinal call count = %d, want 1", got)
	}

	// The store must reflect session A's last URL.
	tile, err := srv.store.GetTile(context.Background(), tileID)
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if tile.URLString != "https://example.com/fromA" {
		t.Errorf("tile url_string = %q, want %q", tile.URLString, "https://example.com/fromA")
	}

	// WS B is the active session — confirm a second fakeSession was created.
	sessionB := waitForNSessions(t, fake, 2)
	if sessionB == sessionA {
		t.Error("session B is the same object as session A")
	}
	if sessionB.isClosed() {
		t.Error("session B should still be open")
	}
}

// TestURLStreamMinimumViewportClamp verifies that a viewport message with
// dimensions below the minimums results in a Resize call at the clamped values.
func TestURLStreamMinimumViewportClamp(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)

	// Send a viewport that is below both minimums.
	payload, _ := json.Marshal(urlStreamMessage{Kind: "viewport", Width: 100, Height: 80})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.resizeCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.resizeCount(); got != 1 {
		t.Fatalf("got %d resizes, want 1", got)
	}
	if w, h := s.lastResize(); w != minViewportW || h != minViewportH {
		t.Errorf("resize = %dx%d, want %dx%d (minimums)", w, h, minViewportW, minViewportH)
	}
}

// TestURLStreamSurvivesTransientCancel pins the fix for the first-click
// target=_blank bug: an input that races a tab swap fails with
// context.Canceled against the old (now-closing) tab. readLoop must NOT
// tear the WS down for that — the session is still alive — or the client
// shows "page no longer active" on the first _blank click.
func TestURLStreamSurvivesTransientCancel(t *testing.T) {
	srv, hs, root := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, root, "https://example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	// Arm one transient cancel (Done stays open).
	s.setCancelNextInputs(1)

	mk := func() []byte {
		b, _ := json.Marshal(urlStreamMessage{
			Kind: string(urldriver.InputMouseDown), X: 1, Y: 1, Button: urldriver.MouseButtonLeft,
		})
		return b
	}

	// First input: dispatched while "mid-swap" → context.Canceled. The
	// server must swallow it and keep reading.
	if err := conn.Write(ctx, websocket.MessageText, mk()); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	// Second input: should land normally, proving the reader survived.
	if err := conn.Write(ctx, websocket.MessageText, mk()); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.inputCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.inputCount(); got != 1 {
		t.Fatalf("recorded inputs = %d, want 1 (first canceled, second delivered) — readLoop tore down on transient cancel", got)
	}

	// Session must still be alive.
	if s.isClosed() {
		t.Error("session closed after a transient input cancel")
	}
}
