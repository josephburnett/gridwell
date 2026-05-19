package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// streamTestServer wires up a fresh in-memory Server and returns the
// *Server (so the caller can call SetURLStreamer), the httptest
// fixture, the test user, and a session cookie.
func streamTestServer(t *testing.T) (*Server, *httptest.Server, *store.User, *http.Cookie) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.UseFastHashing()
	t.Cleanup(func() { _ = st.Close() })
	u, err := st.CreateUser(context.Background(), "alice", "p")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	srv := New(st, Config{SecureCookie: false})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	body := bytes.NewBuffer(nil)
	_ = json.NewEncoder(body).Encode(rpc.LoginRequest{Username: "alice", Password: "p"})
	resp, err := http.Post(hs.URL+"/rpc/Login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	return srv, hs, u, cookie
}

// fakeStreamer is a non-Chromium urlStreamer for HTTP-level tests of
// the URLStream handler. Records sessions opened against it. Each
// fakeSession exposes channels the test can push frames/navs onto and
// counters the test can inspect.
type fakeStreamer struct {
	available bool
	mu        sync.Mutex
	live      map[liveKeyT]bool
	sessions  []*fakeSession
}

type liveKeyT struct {
	user, tile int64
}

func newFakeStreamer() *fakeStreamer {
	return &fakeStreamer{
		available: true,
		live:      map[liveKeyT]bool{},
	}
}

func (f *fakeStreamer) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeStreamer) IsLive(u, t int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[liveKeyT{u, t}]
}

func (f *fakeStreamer) setLive(u, t int64, live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[liveKeyT{u, t}] = live
}

func (f *fakeStreamer) OpenSession(uid, tid int64, url string, w, h int64) (urlSession, error) {
	s := &fakeSession{
		userID: uid, tileID: tid,
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

// fakeSession implements urlSession in-memory for tests.
type fakeSession struct {
	userID, tileID int64

	mu       sync.Mutex
	inputs   []urldriver.InputEvent
	resizes  [][2]int64
	lastURL  string
	closed   bool
	captures int

	frames chan []byte
	navs   chan string
	done   chan struct{}

	initialW, initialH int64
	finalJPEG          []byte
}

func (s *fakeSession) Input(ev urldriver.InputEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// createURLTileViaRPC creates a uri-list tile through the public
// CreateFile RPC. Returns the new tile's ID.
func createURLTileViaRPC(t *testing.T, hs *httptest.Server, cookie *http.Cookie, url string) int64 {
	t.Helper()
	var w rpc.WhoamiResponse
	if st, body := callRPC(t, hs, cookie, "Whoami", &rpc.WhoamiRequest{}, &w); st != 200 {
		t.Fatalf("Whoami status=%d body=%s", st, body)
	}
	var resp rpc.TileResponse
	st, body := callRPC(t, hs, cookie, "CreateFile", &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: rpc.ViewRect{X: -100, Y: -100, W: 200, H: 200},
		GridID: w.RootGridID,
		X:      0, Y: 0, W: 1, H: 1,
		MimeType: rpc.MimeURIList, Data: []byte(url),
	}, &resp)
	if st != 200 {
		t.Fatalf("CreateFile status=%d body=%s", st, body)
	}
	return resp.Tile.ID
}

func urlStreamURL(hs *httptest.Server, tileID int64) string {
	return "ws://" + strings.TrimPrefix(hs.URL, "http://") +
		"/rpc/URLStream?tile_id=" + strconv.FormatInt(tileID, 10)
}

func cookieHeader(c *http.Cookie) http.Header {
	h := http.Header{}
	h.Set("Cookie", c.Name+"="+c.Value)
	return h
}

// waitForSession polls for the first session opened against the
// streamer; returns it or fails the test.
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

func TestURLStreamRefusesWithoutSession(t *testing.T) {
	srv, hs, _, _ := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	_, resp, err := websocket.Dial(context.Background(), urlStreamURL(hs, 1), nil)
	if err == nil {
		t.Fatal("unauthenticated dial succeeded; expected 401")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", resp)
	}
}

func TestURLStreamRefusesWhenStreamerMissing(t *testing.T) {
	_, hs, _, cookie := streamTestServer(t)
	_, resp, err := websocket.Dial(context.Background(), urlStreamURL(hs, 1),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
	if err == nil {
		t.Fatal("dial without streamer succeeded; expected 503")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %v, want 503", resp)
	}
}

func TestURLStreamRefusesNonLive(t *testing.T) {
	srv, hs, _, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	_, resp, err := websocket.Dial(context.Background(), urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
	if err == nil {
		t.Fatal("dial on non-live tile succeeded; expected 409")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %v, want 409", resp)
	}
}

func TestURLStreamOpensSession(t *testing.T) {
	srv, hs, u, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	fake.setLive(u.ID, tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s := waitForSession(t, fake)
	if s.userID != u.ID || s.tileID != tileID {
		t.Errorf("session id = (%d, %d), want (%d, %d)", s.userID, s.tileID, u.ID, tileID)
	}
	if s.initialW != defaultViewportW || s.initialH != defaultViewportH {
		t.Errorf("session viewport = %dx%d, want %dx%d", s.initialW, s.initialH, defaultViewportW, defaultViewportH)
	}
}

func TestURLStreamDeliversFramesAndNav(t *testing.T) {
	srv, hs, u, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	fake.setLive(u.ID, tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
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
	srv, hs, u, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	fake.setLive(u.ID, tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
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
	srv, hs, u, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	fake.setLive(u.ID, tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
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
	srv, hs, u, cookie := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	tileID := createURLTileViaRPC(t, hs, cookie, "https://example.com")
	fake.setLive(u.ID, tileID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	s := waitForSession(t, fake)
	// Simulate a navigation so LastURL is non-empty when the WS closes.
	s.setLastURL("https://example.com/last")

	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Wait for the server's cleanup: Session.Close fires.
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

	// Verify the preview made it to the store.
	tile, err := srv.store.GetTile(context.Background(), u.ID, tileID)
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if tile.URLString != "https://example.com/last" {
		t.Errorf("url_string = %q, want %q", tile.URLString, "https://example.com/last")
	}
	jpeg, err := srv.store.GetTilePreview(context.Background(), u.ID, tileID)
	if err != nil {
		t.Fatalf("GetTilePreview: %v", err)
	}
	if string(jpeg) != "final-jpeg-bytes" {
		t.Errorf("preview_jpeg = %q, want %q", string(jpeg), "final-jpeg-bytes")
	}
}
