package server

// The shell door, crossed for real: the client stack the wasm client runs
// (client/shellstream, client/shellws) dials WebHandler behind the auth cookie
// and the bytes come back through the whole chain to the PTY. Everything that
// could drift — the address, the frame kinds, the exit verdict — has one owner
// in client/shellwire, and this proves both ends read it the same way.

import (
	"bytes"
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shelldriver"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/trace"
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

func newShellDoorFixture(t *testing.T, cfg Config, tweak ...func(*Server)) *shellDoorFixture {
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
	client := local.New(st, shellsvc.NewManager(fake))
	reg := plugin.NewRegistry()
	reg.Register(uuid, "home", client, nil)
	bareRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid: %v", err)
	}
	srv := mustNew(t, reg, cfg)
	for _, fn := range tweak {
		fn(srv)
	}
	hs := serveWeb(t, srv)
	return &shellDoorFixture{
		hs:   hs,
		cl:   rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()),
		fake: fake,
		root: uuid + "/" + bareRoot,
		uuid: uuid,
	}
}

func (f *shellDoorFixture) createShell(t *testing.T, x, y int64) *gridwellv1.Tile {
	t.Helper()
	tile, err := f.cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: f.root, Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: x, Y: y, W: 1, H: 1}})
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
	cs.reg.Open("pane-1", tile.Id, 100, 40)
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
	cs.reg.Open("pane-1", tile.Id, 80, 24)
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

// The verdict crosses the seam: a snapshotted tile whose tmux session is gone
// must reach the client as sessionGone, the fact the refresh affordance reads
// through shellconn.DecideShellRefreshVisible. It rides the door's exit frame,
// and shellEndVerdict is the one owner of the classification.
func TestShellDoorReportsSessionGone(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	// A frozen snapshot means "do not fabricate a new bash behind the
	// JPEG"; with no live session, the attach is refused.
	if _, err := f.cl.SetTile(context.Background(), &gridwellv1.SetTileRequest{TileId: tile.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindShell}, Preview: []byte("jpegbytes")}); err != nil {
		t.Fatalf("SetShellPreview: %v", err)
	}
	cs := f.clientStack()
	cs.reg.Open("pane-1", tile.Id, 80, 24)
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

// A platform with no PTY refuses at the driver, and that refusal reaches the
// user as an exit frame carrying the reason: shelldriver returns
// ErrShellsUnavailable, shellsvc hands it up, and the door turns it into the
// client's exit message. A refusal that only logged would look like the shell
// silently vanished. Not SessionGone: nothing went away, the node simply
// cannot host one.
func TestShellDoorSurfacesADriverThatCannotOpen(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	f.fake.OpenErr = shelldriver.ErrShellsUnavailable
	tile := f.createShell(t, 0, 0)
	cs := f.clientStack()
	cs.reg.Open("pane-1", tile.Id, 80, 24)
	select {
	case e := <-cs.exit:
		if e.PaneID != "pane-1" {
			t.Fatalf("exit = %+v, want it addressed to the opening pane", e)
		}
		if !strings.Contains(e.Message, "unavailable") {
			t.Fatalf("exit message = %q; want the driver's reason carried to the user", e.Message)
		}
		if e.SessionGone {
			t.Errorf("exit = %+v; a node that cannot host a PTY is not a session that went away", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a driver that cannot open must surface, not hang")
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
	addr, err := shellwire.AttachURL(f.hs.URL, tile.Id, 80, 24)
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
	addr, err := shellwire.AttachURL(f.hs.URL, tile.Id, 80, 24)
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

// tinyReadBuffer shrinks a connection's receive buffer. A peer that stops
// reading then wedges the writer after kilobytes, rather than after however
// much the kernel decides to buffer, which is what makes the two tests below
// about the timeout instead of about autotuning.
func tinyReadBuffer(c net.Conn) net.Conn {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	return c
}

// deafClient carries hs's auth cookie on a socket that fills fast.
func deafClient(hs *httptest.Server) *http.Client {
	var d net.Dialer
	return &http.Client{
		Jar: hs.Client().Jar,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return tinyReadBuffer(c), nil
		}},
	}
}

// deafListener accepts and never reads what arrives.
type deafListener struct{ net.Listener }

func (l deafListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return tinyReadBuffer(c), nil
}

// A viewer that stops draining its socket must not pin the PTY reader: the
// door bounds one output frame by the server's shell write timeout and the
// attachment ends with the PTY released. Without the bound the writer parks
// forever and the tile stays held by a viewer that is no longer listening.
// The bound is the server's own, because 30 seconds is not a test and a
// package-wide one would be lowered under doors already serving.
func TestShellDoorReleasesThePTYWhenTheViewerStopsDraining(t *testing.T) {
	const bound = 250 * time.Millisecond
	f := newShellDoorFixture(t, Config{}, func(s *Server) { s.shellWriteTimeout = bound })
	tile := f.createShell(t, 0, 0)
	addr, err := shellwire.AttachURL(f.hs.URL, tile.Id, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, addr, &websocket.DialOptions{HTTPClient: deafClient(f.hs)})
	if err != nil {
		t.Fatalf("dial the shell door: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	sess := waitSession(t, f.fake)

	// Keystrokes the fake PTY echoes back, at a volume no socket buffer
	// holds, to a client that never reads a frame.
	blob := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 4; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, blob); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(20 * bound)
	for !sess.IsClosed() {
		if time.Now().After(deadline) {
			t.Fatalf("the PTY was still held %v after the viewer stopped draining; the write bound is %v", 20*bound, bound)
		}
		time.Sleep(bound / 10)
	}
}

// The client's half of the same bound: keystrokes must not park in a wedged
// socket. client/shellws has no tests of its own, so its write bound is bound
// here, where the client stack is already dialed for real. The far end is a
// socket that accepts the upgrade and reads nothing — the shape a hung host
// presents; the door itself never stops reading, so it cannot play that part.
func TestShellClientSurfacesAWedgedSocket(t *testing.T) {
	const bound = 250 * time.Millisecond

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	deaf := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-stop
		conn.CloseNow()
	}))
	deaf.Listener = deafListener{deaf.Listener}
	deaf.Start()
	t.Cleanup(deaf.Close)

	exit := make(chan shellstream.Exit, 4)
	reg := shellstream.New(shellws.Dialer(shellws.Options{Origin: deaf.URL, HTTPClient: deaf.Client(), WriteTimeout: bound}),
		func(string, []byte) {}, func(e shellstream.Exit) { exit <- e })
	reg.Open("pane-1", "wedged1/9", 80, 24)
	t.Cleanup(func() { reg.Close("pane-1") })

	// More keystrokes than any socket buffer holds, at a far end that reads
	// none of them.
	blob := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 4; i++ {
		reg.Write("pane-1", blob)
	}

	select {
	case e := <-exit:
		// The timed-out write tears the socket down, so the read loop may
		// reach the end first; either way the viewer is told, with a reason.
		if e.Message == "" || e.SessionGone {
			t.Fatalf("exit = %+v, want the wedged socket reported with a reason and no claim the session died", e)
		}
	case <-time.After(20 * bound):
		t.Fatalf("a wedged socket parked the terminal for %v; the write bound is %v", 20*bound, bound)
	}
}

// Both wedge tests above run at a bound of their own, so neither would notice
// the declared values drifting, and the door reads the Server's field rather
// than the constant. Thirty seconds is the budget a briefly stalled peer gets
// before its socket counts as gone: long enough that a paused viewer or a slow
// hop is not killed, short enough that nothing holds a PTY for a minute.
func TestShellWriteBoundsAreTheDeclaredOnes(t *testing.T) {
	if defaultShellWriteTimeout != 30*time.Second {
		t.Errorf("the door bounds a PTY-output frame write at %v, want 30s", defaultShellWriteTimeout)
	}
	if shellws.DefaultWriteTimeout != 30*time.Second {
		t.Errorf("the client bounds a keystroke frame write at %v, want 30s", shellws.DefaultWriteTimeout)
	}
	srv := mustNew(t, plugin.NewRegistry(), Config{})
	if srv.shellWriteTimeout != defaultShellWriteTimeout {
		t.Errorf("New built a server bounding writes at %v, want defaultShellWriteTimeout (%v)", srv.shellWriteTimeout, defaultShellWriteTimeout)
	}
}

// A shell attachment that opens and closes is a PTY's whole life on this
// node, and the ring is where it is written down: the tile, the open, and how
// the attachment ended.
func TestTheShellDoorTracesAnAttachment(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 6, 6)
	cs := f.clientStack()
	cs.reg.Open("pane-trace", tile.Id, 20, 10)
	waitSession(t, f.fake)
	cs.reg.Close("pane-trace")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var opened, closed bool
		for _, rec := range trace.Default().Snapshot() {
			if rec.Src != "shelldoor" || rec.KV["tile"] != tile.Id {
				continue
			}
			opened = opened || rec.Msg == "open"
			closed = closed || strings.HasPrefix(rec.Msg, "close")
		}
		if opened && closed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the attachment left no open and close records")
}

// A refused attach never touches a PTY, and that refusal is the record.
func TestTheShellDoorTracesARefusal(t *testing.T) {
	f := newShellDoorFixture(t, Config{DisableShells: true})
	res, err := f.hs.Client().Get(f.hs.URL + shellwire.Path + "?" + shellwire.QueryTileID + "=" + f.uuid + "/1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "shelldoor" && rec.KV["tile"] == f.uuid+"/1" && strings.HasPrefix(rec.Msg, "refused:") {
			return
		}
	}
	t.Error("a refused attach left no record")
}
