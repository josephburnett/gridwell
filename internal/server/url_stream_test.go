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
// fixture, and a session cookie for the "alice" user.
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
// the URLStream handler. Records every Subscribe + ForwardInput call
// and exposes a method to push frames to all current subscribers.
type fakeStreamer struct {
	available bool
	mu        sync.Mutex
	live      map[liveKeyT]bool
	subs      []urldriver.Subscriber
	inputs    []urldriver.InputEvent
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

func (f *fakeStreamer) Subscribe(_, _ int64, sub urldriver.Subscriber) func() {
	f.mu.Lock()
	f.subs = append(f.subs, sub)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, s := range f.subs {
			if s == sub {
				f.subs = append(f.subs[:i], f.subs[i+1:]...)
				return
			}
		}
	}
}

func (f *fakeStreamer) ForwardInput(_, _ int64, ev urldriver.InputEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, ev)
	return nil
}

func (f *fakeStreamer) pushFrame(jpeg []byte) {
	f.mu.Lock()
	subs := append([]urldriver.Subscriber(nil), f.subs...)
	f.mu.Unlock()
	for _, s := range subs {
		s.SendFrame(jpeg)
	}
}

func (f *fakeStreamer) pushNav(u string) {
	f.mu.Lock()
	subs := append([]urldriver.Subscriber(nil), f.subs...)
	f.mu.Unlock()
	for _, s := range subs {
		s.SendNavigation(u)
	}
}

func (f *fakeStreamer) inputCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func (f *fakeStreamer) inputAt(i int) urldriver.InputEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[i]
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
		X: 0, Y: 0, W: 1, H: 1,
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

func TestURLStreamRefusesWithoutSession(t *testing.T) {
	srv, hs, _, _ := streamTestServer(t)
	fake := newFakeStreamer()
	srv.SetURLStreamer(fake)
	// No session cookie on the WS handshake.
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
	// Don't SetURLStreamer.
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
	// Don't mark it live in the fake.
	_, resp, err := websocket.Dial(context.Background(), urlStreamURL(hs, tileID),
		&websocket.DialOptions{HTTPHeader: cookieHeader(cookie)})
	if err == nil {
		t.Fatal("dial on non-live tile succeeded; expected 409")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %v, want 409", resp)
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

	go func() {
		time.Sleep(50 * time.Millisecond)
		fake.pushFrame([]byte{0xff, 0xd8, 0xff, 0xe0}) // JPEG magic prefix
		fake.pushNav("https://example.com/two")
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

	payload, _ := json.Marshal(urlStreamMessage{
		Kind: string(urldriver.InputMouseDown),
		X: 17, Y: 23, Button: urldriver.MouseButtonLeft,
	})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fake.inputCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := fake.inputCount(); got != 1 {
		t.Fatalf("got %d inputs, want 1", got)
	}
	ev := fake.inputAt(0)
	if ev.Kind != urldriver.InputMouseDown || ev.X != 17 || ev.Y != 23 || ev.Button != urldriver.MouseButtonLeft {
		t.Errorf("input = %+v", ev)
	}
}
