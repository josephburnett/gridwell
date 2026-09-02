package server_test

// The connection seam, end to end in-process: a remote node behind a real
// h2c export ⇐ the node's TRANSPORT (declared connections, a fake dialer
// landing on that export) ⇐ the local node's front door. Every id crosses
// "<id>/<conn>/…": the handshake lists the connection,
// its landing is the remote's HOME, reads and writes route through, events
// arrive prefixed, and a dial failure rides the row as its status.

import (
	"context"
	"database/sql"
	"errors"
	"github.com/josephburnett/gridwell/internal/namespace"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "modernc.org/sqlite"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const localNodeID = "lnode1"

type transportHarness struct {
	localCl   *rpc.Client         // the local node's front door
	remoteCl  *rpc.Client         // the remote node's own front door
	tClient   namespace.Namespace // the transport, in process
	rootBare  string              // the remote home's bare root grid id
	localURL  string              // the local node's web door origin
	localHTTP *httptest.Server    // …and the server behind it
	// remoteShell is the REMOTE home's PTY backend — the fake echoing
	// streamer, so a shell attach through the chain can be asserted
	// without a tmux anywhere.
	remoteShell *shellsvctest.FakeStreamer

	mu      sync.Mutex
	dialErr error
	dialed  []dial.Config
}

// newTransportHarness builds the two nodes with the given connections
// declared on the local one; the dialer lands every connection on the
// remote export, or fails with dialErr when set.
func newTransportHarness(t *testing.T, conns []config.ConnectionConfig, dialErr error) *transportHarness {
	t.Helper()
	ctx := context.Background()

	h := &transportHarness{dialErr: dialErr, remoteShell: shellsvctest.New()}

	remoteStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteStore.Close() })
	remoteClient := local.New(remoteStore, shellsvc.NewManager(h.remoteShell))
	remoteReg := plugin.NewRegistry()
	remoteReg.Register("rnode1", "home", remoteClient, nil)
	remoteReg.SetLabel("rnode1", "home")
	remoteSrv := servertest.New(t, remoteReg, server.Config{ID: "rnode1"})
	remoteHTTP := httptest.NewUnstartedServer(nil)
	remoteHTTP.Config.Handler = remoteSrv.ConnectionHandler()
	remoteHTTP.Config.Protocols = server.NodeProtocols()
	remoteHTTP.EnableHTTP2 = true
	remoteHTTP.Start()
	t.Cleanup(remoteHTTP.Close)
	grpcConn, err := grpc.NewClient(strings.TrimPrefix(remoteHTTP.URL, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grpcConn.Close() })
	remoteExport := gridwellv1.NewGridwellClient(grpcConn)

	sqlDB, err := sql.Open("sqlite", t.TempDir()+"/remote.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := connection.NewDB(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := connection.New(db, func(cfg dial.Config) (namespace.Namespace, func(), error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.dialed = append(h.dialed, cfg)
		if h.dialErr != nil {
			return nil, nil, h.dialErr
		}
		return namespace.FromClient(remoteExport), func() {}, nil
	}, "", conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(ctx)
	tClient := transport
	h.tClient = tClient

	localStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localStore.Close() })
	homeClient := local.New(localStore, nil)
	localReg := plugin.NewRegistry()
	localReg.Register(localNodeID, "home", homeClient, nil)
	localReg.SetLabel(localNodeID, "home")
	localReg.SetTransport(tClient, func(ctx context.Context) []plugin.ConnectionRow {
		var out []plugin.ConnectionRow
		for _, r := range transport.Rows(ctx) {
			out = append(out, plugin.ConnectionRow{Name: r.Name, Label: r.Label, RootGridID: r.RootGridID,
				StatusDetail: r.StatusDetail, ViewCx: r.ViewCx, ViewCy: r.ViewCy, ViewZoom: r.ViewZoom})
		}
		return out
	}, nil)
	localSrv := servertest.New(t, localReg, server.Config{ID: localNodeID})
	localHTTP := servertest.Serve(t, localSrv)
	h.localCl = rpc.NewClient(localHTTP.Client(), localHTTP.URL, connect.WithProtoJSON())
	h.localHTTP = localHTTP
	h.localURL = localHTTP.URL
	remoteWeb := servertest.Serve(t, remoteSrv)
	h.remoteCl = rpc.NewClient(remoteWeb.Client(), remoteWeb.URL, connect.WithProtoJSON())
	h.rootBare, err = remoteStore.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestConnectionThroughTheChain(t *testing.T) {
	ctx := context.Background()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Label: "Geneva", Addr: "/far/federation.sock"}}, nil)

	// The handshake: home is a field, the connection is a row under the
	// node's own id, landing on the remote's HOME (its home_grid_id, seen
	// through the chain).
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lp.HomeGridID, localNodeID+"/") {
		t.Fatalf("home_grid_id = %q, want the local home", lp.HomeGridID)
	}
	if len(lp.Connections) != 1 {
		t.Fatalf("connections = %+v, want one", lp.Connections)
	}
	conn := lp.Connections[0]
	wantRoot := localNodeID + "/geneva/rnode1/" + h.rootBare
	if conn.UUID != localNodeID+"/geneva" || conn.Label != "Geneva" || conn.RootGridID != wantRoot {
		t.Fatalf("connection row = %+v, want uuid %s/geneva rooted at %s", conn, localNodeID, wantRoot)
	}
	if h.dialed[0].Addr != "/far/federation.sock" || h.dialed[0].Host != "" {
		t.Fatalf("dialed = %+v, want a direct dial of the declared socket", h.dialed[0])
	}

	// The routed menu for the landing's node is the REMOTE's handshake,
	// re-qualified — home first.
	menu, err := h.localCl.HandshakeNS(ctx, localNodeID+"/geneva")
	if err != nil {
		t.Fatal(err)
	}
	if menu.HomeGridID != wantRoot || len(menu.Plugins) != 1 || menu.Plugins[0].UUID != localNodeID+"/geneva/rnode1" {
		t.Fatalf("routed menu = %+v", menu)
	}
	if menu.ContentToken != "" {
		t.Error("node-local fields must be ZEROED on a forwarded handshake")
	}

	// A grid through the chain carries the serving node's namespace.
	g, err := h.localCl.GetGrid(ctx, wantRoot)
	if err != nil {
		t.Fatal(err)
	}
	if g.Grid.NodeNS != localNodeID+"/geneva" || !g.Grid.Writable {
		t.Fatalf("landing grid = %+v, want node_ns %s/geneva, writable", g.Grid, localNodeID)
	}

	// Write through the chain, read back on the remote's own door.
	txt, err := h.localCl.CreateText(ctx, &rpc.CreateTextRequest{GridID: wantRoot, X: 1, Y: 1, W: 1, H: 1, Data: []byte("# via geneva")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(txt.ID, localNodeID+"/geneva/rnode1/") {
		t.Fatalf("created id = %q, want the four-segment chain", txt.ID)
	}
	bare := strings.TrimPrefix(txt.ID, localNodeID+"/geneva/")
	data, _, _, err := h.remoteCl.ReadContent(ctx, bare)
	if err != nil || string(data) != "# via geneva" {
		t.Fatalf("remote read = %q (%v)", data, err)
	}
}

func TestConnectionEventsArrivePrefixed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := lp.Connections[0].RootGridID
	// Hop 1: the transport's own stream carries the remote's events with
	// the connection segment prepended.
	transportEvents := make(chan *gridwellv1.Event, 32)
	go func() {
		_ = h.tClient.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			select {
			case transportEvents <- ev:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	// Hop 2: the local door's stream carries them under the node's id.
	// (Connect's server-stream call returns only once headers flush — on
	// the first event — so it runs in a goroutine, like server_test does.)
	doorEvents := make(chan rpc.Event, 16)
	doorErr := make(chan error, 1)
	go func() {
		es, err := h.localCl.Subscribe(ctx)
		if err != nil {
			doorErr <- err
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				doorErr <- err
				return
			}
			doorEvents <- ev
		}
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := h.localCl.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	for done := false; !done; {
		select {
		case ev := <-transportEvents:
			if tc := ev.GetTileChanged(); tc != nil {
				if !strings.HasPrefix(tc.Tile.Id, "geneva/rnode1/") {
					t.Fatalf("transport event id = %q, want geneva/rnode1/…", tc.Tile.Id)
				}
				done = true
			}
		case <-ctx.Done():
			t.Fatal("hop 1 (transport stream): no event")
		}
	}
	for {
		select {
		case ev := <-doorEvents:
			if ev.TileChanged != nil {
				if !strings.HasPrefix(ev.TileChanged.Tile.ID, localNodeID+"/geneva/rnode1/") {
					t.Fatalf("event id = %q, want the chain", ev.TileChanged.Tile.ID)
				}
				return
			}
		case err := <-doorErr:
			t.Fatalf("hop 2 (the local door): %v", err)
		case <-ctx.Done():
			t.Fatal("hop 2 (the local door): no event")
		}
	}
}

func TestDialFailureRidesTheRow(t *testing.T) {
	ctx := context.Background()
	// A failing dialer from the start: the row is pending with the
	// failure as its status, and a read through it fails, never answers
	// empty.
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "dead", Label: "Dead", Addr: "/s"}}, errors.New("host key mismatch"))
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.Connections) != 1 || lp.Connections[0].RootGridID != "" || !strings.Contains(lp.Connections[0].StatusDetail, "host key mismatch") {
		t.Fatalf("pending row = %+v, want no root and the dial failure as status", lp.Connections)
	}
	if _, err := h.localCl.GetGrid(ctx, localNodeID+"/dead/x/1"); err == nil {
		t.Fatal("a read through a dead connection must fail, not answer empty")
	}
}

// A shell on a MOUNTED remote attaches through the /shell door: the web
// door's WebSocket → this node's shell route → the transport → the remote
// node's export → the remote home's PTY. The transport is just another
// namespace to the door — which is the whole point of the id chain — but
// the Electron relay it replaced dialed the CONNECTION DOOR instead, so
// nothing pinned that the browser door forwards a PTY. This does.
func TestShellDoorThroughAConnection(t *testing.T) {
	ctx := context.Background()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Label: "Geneva", Addr: "/far/federation.sock"}}, nil)

	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := lp.Connections[0].RootGridID

	// The shell tile lives on the REMOTE, created through the chain.
	tile, err := h.localCl.CreateShell(ctx, &rpc.CreateShellRequest{GridID: remoteRoot, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateShell through the connection: %v", err)
	}
	if !strings.HasPrefix(tile.ID, localNodeID+"/geneva/") {
		t.Fatalf("tile id %q is not a chain through the connection", tile.ID)
	}

	out := make(chan []byte, 8)
	exits := make(chan shellstream.Exit, 4)
	reg := shellstream.New(
		shellws.Dialer(shellws.Options{Origin: h.localURL, HTTPClient: h.localHTTP.Client()}),
		func(_ string, b []byte) { out <- append([]byte(nil), b...) },
		func(e shellstream.Exit) { exits <- e },
	)
	reg.Open("pane-1", tile.ID, 90, 30)
	t.Cleanup(func() { reg.Close("pane-1") })

	reg.Write("pane-1", []byte("across the wire"))
	select {
	case got := <-out:
		if string(got) != "across the wire" {
			t.Fatalf("round trip = %q", got)
		}
	case e := <-exits:
		t.Fatalf("the attach ended instead of echoing: %+v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("no PTY output from the mounted remote within 5s")
	}
	if h.remoteShell.SessionCount() != 1 {
		t.Errorf("remote PTY sessions = %d, want 1", h.remoteShell.SessionCount())
	}
}

// TestTwoSubscribersEachSeeExactlyOnePrefix is the MESSAGE-OWNERSHIP seam
// (namespace's contract): with no wire between the router and its
// namespaces, one *pb.Event travels to every subscriber by pointer — the
// transport's hub fans the same value out, and the router qualifies it per
// hop. Qualification therefore has to CLONE (api/rpc.TransitQualifyTiles,
// server.qualifyTiles); if any hop rewrote ids in place, the second
// subscriber would read "lnode1/lnode1/geneva/…" or worse, and the
// corruption would be invisible to a single-subscriber test. gRPC used to
// hide this by handing every stream its own decoded copy — the reason a
// general deep-copy layer looks unnecessary is that the clone already lives
// in the one place that mutates.
func TestTwoSubscribersEachSeeExactlyOnePrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := lp.Connections[0].RootGridID

	const subscribers = 4
	ids := make(chan string, subscribers*8)
	errs := make(chan error, subscribers)
	for i := 0; i < subscribers; i++ {
		go func() {
			es, err := h.localCl.Subscribe(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer es.Close()
			for {
				ev, ok, err := es.Recv()
				if err != nil || !ok {
					return
				}
				if ev.TileChanged != nil {
					select {
					case ids <- ev.TileChanged.Tile.ID:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	time.Sleep(500 * time.Millisecond) // every subscriber is fanned in

	if _, err := h.localCl.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	want := localNodeID + "/geneva/rnode1/"
	seen := 0
	for seen < subscribers {
		select {
		case id := <-ids:
			if !strings.HasPrefix(id, want) {
				t.Fatalf("subscriber saw id %q, want the single chain %q…", id, want)
			}
			if strings.Count(id, localNodeID+"/") != 1 || strings.Count(id, "geneva/") != 1 {
				t.Fatalf("subscriber saw id %q — a segment was applied twice (an event mutated in place)", id)
			}
			seen++
		case err := <-errs:
			t.Fatalf("subscribe: %v", err)
		case <-ctx.Done():
			t.Fatalf("only %d of %d subscribers saw the event", seen, subscribers)
		}
	}
}
