package server_test

// The connection seam, end to end in-process: a remote node behind a real
// h2c export ⇐ the node's TRANSPORT (declared connections, a fake dialer
// landing on that export) ⇐ the local node's front door. Every id crosses
// "<id>/<conn>/…" (docs/one-node.md): the handshake lists the connection,
// its landing is the remote's HOME, reads and writes route through, events
// arrive prefixed, and a dial failure rides the row as its status.

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "modernc.org/sqlite"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const localNodeID = "lnode1"

type transportHarness struct {
	localCl  *rpc.Client               // the local node's front door
	remoteCl *rpc.Client               // the remote node's own front door
	tClient  gridwellv1.GridwellClient // the transport, in process
	rootBare string                    // the remote home's bare root grid id

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

	remoteStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteStore.Close() })
	remoteClient, remoteCloser, err := compose.ServeInProcess(local.New(remoteStore, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remoteCloser)
	remoteReg := plugin.NewRegistry()
	remoteReg.Register("rnode1", "home", remoteClient, nil)
	remoteReg.SetLabel("rnode1", "home")
	remoteSrv := servertest.New(t, remoteReg, server.Config{ID: "rnode1"})
	remoteHTTP := httptest.NewUnstartedServer(nil)
	remoteHTTP.Config.Handler = remoteSrv.FederationHandler()
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

	h := &transportHarness{dialErr: dialErr}
	sqlDB, err := sql.Open("sqlite", t.TempDir()+"/remote.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := remote.NewDB(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := remote.New(db, func(cfg dial.Config) (gridwellv1.GridwellClient, func(), error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.dialed = append(h.dialed, cfg)
		if h.dialErr != nil {
			return nil, nil, h.dialErr
		}
		return remoteExport, func() {}, nil
	}, "", conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(ctx)
	tClient, tCloser, err := compose.ServeInProcess(transport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tCloser)
	h.tClient = tClient

	localStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localStore.Close() })
	homeClient, homeCloser, err := compose.ServeInProcess(local.New(localStore, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(homeCloser)
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
	ts, err := h.tClient.Subscribe(ctx, &gridwellv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
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
	for {
		ev, err := ts.Recv()
		if err != nil {
			t.Fatalf("hop 1 (transport stream): %v", err)
		}
		if tc := ev.GetTileChanged(); tc != nil {
			if !strings.HasPrefix(tc.Tile.Id, "geneva/rnode1/") {
				t.Fatalf("transport event id = %q, want geneva/rnode1/…", tc.Tile.Id)
			}
			break
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
