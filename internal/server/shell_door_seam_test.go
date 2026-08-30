package server

// The shell door, crossed for real: the CLIENT stack (client/shellstream +
// client/shellws, the very packages the wasm client runs) dials the SERVER
// handler (WebHandler, behind the auth cookie) and the bytes come back
// through the whole chain — WebSocket → shell door → OpenShell → the home
// namespace → the PTY.
//
// Charter §4: a unit test on each side of a contract cannot catch a
// contract mismatch, and the mismatch is the bug. Everything here that
// could drift — the address, the frame kinds, the exit verdict — has
// exactly one owner (client/shellwire) and this test proves both ends read
// it the same way.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// shellDoorFixture stands up the real browser door over a home namespace
// whose PTY backend is the fake echoing streamer.
type shellDoorFixture struct {
	hs   *httptest.Server
	cl   *rpc.Client
	fake *shellsvctest.FakeStreamer
	root string
	uuid string
}

func newShellDoorFixture(t *testing.T, cfg Config) *shellDoorFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uuid, err := st.PluginUUID(context.Background())
	if err != nil {
		t.Fatalf("plugin uuid: %v", err)
	}
	fake := shellsvctest.New()
	client, closer, err := plugin.ServeInProcess(local.New(st, shellsvc.NewManager(fake)))
	if err != nil {
		t.Fatalf("serve home: %v", err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(uuid, "home", client, nil)
	bareRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid: %v", err)
	}
	hs := serveWeb(t, mustNew(t, reg, cfg))
	return &shellDoorFixture{
		hs:   hs,
		cl:   rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()),
		fake: fake,
		root: uuid + "/" + bareRoot,
		uuid: uuid,
	}
}

func (f *shellDoorFixture) createShell(t *testing.T, x, y int64) *rpc.Tile {
	t.Helper()
	tile, err := f.cl.CreateShell(context.Background(), &rpc.CreateShellRequest{GridID: f.root, X: x, Y: y, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateShell: %v", err)
	}
	return tile
}

// clientStack is the wasm client's own transport, wired to channels a test
// can read: the lifecycle registry over the WebSocket dialer.
type clientStack struct {
	reg  *shellstream.Registry
	out  chan []byte
	exit chan shellstream.Exit
}

func (f *shellDoorFixture) clientStack() *clientStack {
	cs := &clientStack{out: make(chan []byte, 64), exit: make(chan shellstream.Exit, 4)}
	dial := shellws.Dialer(shellws.Options{
		Origin: f.hs.URL,
		// A browser supplies the page's own cookie; off-browser the test
		// must (helpers_test.serveWeb seeded the jar).
		HTTPClient: f.hs.Client(),
	})
	cs.reg = shellstream.New(dial,
		func(_ string, b []byte) { cs.out <- append([]byte(nil), b...) },
		func(e shellstream.Exit) { cs.exit <- e })
	return cs
}

func waitSession(t *testing.T, fake *shellsvctest.FakeStreamer) *shellsvctest.FakeSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := fake.LastSession(); s != nil {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no PTY session opened within 3s")
	return nil
}

// The end-to-end proof: a keystroke typed into the client stack reaches the
// PTY and its echo comes back as terminal output.
func TestShellDoorRoundTripsBytes(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	cs := f.clientStack()
	cs.reg.Open("pane-1", tile.ID, 100, 40)
	t.Cleanup(func() { cs.reg.Close("pane-1") })

	sess := waitSession(t, f.fake)
	if sess.OpenMode != tmux.ModeCreate {
		t.Errorf("a fresh tile opened in %v, want ModeCreate", sess.OpenMode)
	}
	// The bind's size rides the handshake, not a later frame.
	if sess.InitialCols != 100 || sess.InitialRows != 40 {
		t.Errorf("PTY opened at %dx%d, want 100x40", sess.InitialCols, sess.InitialRows)
	}

	cs.reg.Write("pane-1", []byte("echo me"))
	select {
	case got := <-cs.out:
		if string(got) != "echo me" {
			t.Errorf("round trip = %q, want %q", got, "echo me")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no PTY output within 3s")
	}
}

// A resize is a TEXT control frame in one direction only; it must reach the
// PTY unclamped by the door (shellsvc.ClampSize owns the bounds).
func TestShellDoorForwardsResize(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	cs := f.clientStack()
	cs.reg.Open("pane-1", tile.ID, 80, 24)
	t.Cleanup(func() { cs.reg.Close("pane-1") })
	sess := waitSession(t, f.fake)

	cs.reg.Resize("pane-1", 120, 50)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rs := sess.Resizes(); len(rs) > 0 {
			if rs[len(rs)-1] != [2]uint16{120, 50} {
				t.Fatalf("resize = %v, want [120 50]", rs[len(rs)-1])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("resize never reached the PTY")
}

// The verdict crosses the seam: a snapshotted tile whose tmux session is
// gone must reach the client as sessionGone — the fact the refresh
// affordance reads (shellconn.DecideShellRefreshVisible). It used to be a
// gRPC status code the Electron dialer classified; now it is the door's
// exit frame, and the classification has ONE owner (shellEndVerdict).
func TestShellDoorReportsSessionGone(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	// A frozen snapshot means "do not fabricate a new bash behind the
	// JPEG"; with no live session, the attach is refused.
	if _, err := f.cl.SetShellPreview(context.Background(), &rpc.SetShellPreviewRequest{
		TileID: tile.ID, Version: tile.Version, JPEG: []byte("jpegbytes"),
	}); err != nil {
		t.Fatalf("SetShellPreview: %v", err)
	}
	cs := f.clientStack()
	cs.reg.Open("pane-1", tile.ID, 80, 24)
	select {
	case e := <-cs.exit:
		if !e.SessionGone {
			t.Fatalf("exit = %+v, want SessionGone", e)
		}
		if e.PaneID != "pane-1" || e.Message == "" {
			t.Fatalf("exit = %+v, want the pane and a reason", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no exit reported within 3s")
	}
	if f.fake.SessionCount() != 0 {
		t.Errorf("a dead snapshotted session must not spawn a fresh PTY")
	}
}

// An attach to an id no namespace owns fails on the HANDSHAKE — the client
// sees a failed dial, not a socket that opens and dies.
func TestShellDoorRefusesUnknownTile(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	cs := f.clientStack()
	cs.reg.Open("pane-1", "nosuch1/9", 80, 24)
	select {
	case e := <-cs.exit:
		if e.SessionGone || e.Message == "" {
			t.Fatalf("exit = %+v, want a plain failure with a reason", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unroutable attach must surface, not hang")
	}
}

// The door is the SAME gate as every other page request: no cookie, no PTY.
// (The /content/ door is the one cookie-exempt path; the shell door is not
// it — a leaked content token must never reach a shell.)
func TestShellDoorRequiresTheAuthCookie(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	addr, err := shellwire.AttachURL(f.hs.URL, tile.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A bare client: no cookie jar.
	conn, resp, err := websocket.Dial(ctx, addr, &websocket.DialOptions{HTTPClient: &http.Client{}})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("the shell door opened without the auth cookie")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dial: want 401, got %v", resp)
	}
	if f.fake.SessionCount() != 0 {
		t.Error("an unauthenticated dial must not touch the PTY")
	}
}

// Same-origin: another page on the machine must not be able to open a
// socket to the user's shell on the strength of the browser's cookie.
func TestShellDoorRefusesCrossOrigin(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	addr, err := shellwire.AttachURL(f.hs.URL, tile.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, addr, &websocket.DialOptions{
		HTTPClient: f.hs.Client(), // authenticated, as a browser would be
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example.com"}},
	})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("a cross-origin page opened the shell door")
	}
	if f.fake.SessionCount() != 0 {
		t.Error("a cross-origin dial must not touch the PTY")
	}
}

// disable_shells closes THIS door too — the node-wide refusal lives on the
// one shared shell route, so neither door can be the exception.
func TestShellDoorRefusedWhenShellsDisabled(t *testing.T) {
	off := newShellDoorFixture(t, Config{DisableShells: true})
	// Any shell id at all: the refusal precedes resolution, exactly as it
	// does on the node export, so it cannot depend on the tile existing.
	addr, err := shellwire.AttachURL(off.hs.URL, off.uuid+"/1", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, addr, &websocket.DialOptions{HTTPClient: off.hs.Client()})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("the shell door opened on a shells-disabled node")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("shells-disabled dial: want 403, got %v", resp)
	}
}
