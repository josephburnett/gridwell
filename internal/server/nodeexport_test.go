package server_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
)

// nodeServer stands up a full Server (node id "node1", one in-process localdb
// registered as uuid "ur1", label "personal") behind its NodeHandler on a real
// TCP listener, and returns a raw gRPC client dialed at it — the exact wire a
// remote ssh-plugin sees after its tunnel. Every request routes by the
// QUALIFIED ids it carries; there is no scoping header and no name-based
// selection.
func nodeServer(t *testing.T) (gridwellv1.GridwellClient, gridwellv1.GridwellClient) {
	return nodeServerCfg(t, server.Config{NodeID: "node1"})
}

// nodeServerCfg is nodeServer with the server config under test control
// (shells_disabled_test.go flips DisableShells; everything else uses the
// plain nodeServer default).
func nodeServerCfg(t *testing.T, cfg server.Config) (gridwellv1.GridwellClient, gridwellv1.GridwellClient) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	impl := localdb.New(st, shellsvc.NewManager(shellsvctest.New()))
	direct, closer, err := plugin.ServeInProcess(impl)
	if err != nil {
		t.Fatalf("serve localdb: %v", err)
	}
	t.Cleanup(closer)

	reg := plugin.NewRegistry()
	reg.Register("ur1", "localdb", direct, nil)
	reg.SetLabel("ur1", "personal")
	srv := server.New(reg, cfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.NodeHandler()}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return gridwellv1.NewGridwellClient(conn), direct
}

func TestNodeExportInfoDescribesTheNode(t *testing.T) {
	// A mounter's Info handshake sees the NODE: its node grid as the root
	// (the plugin-list landing page), watchable (the fan-in), read-only.
	c, _ := nodeServer(t)
	info, err := c.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info over gRPC: %v", err)
	}
	if info.RootGridId != "node1/0" {
		t.Errorf("RootGridId = %q, want node1/0 (the node grid, qualified)", info.RootGridId)
	}
	if !info.Watch || info.Writable {
		t.Errorf("capabilities = watch:%v writable:%v, want watch:true writable:false", info.Watch, info.Writable)
	}
}

func TestNodeExportRoutesByQualifiedID(t *testing.T) {
	// The export speaks the node's QUALIFIED ids — the same view a browser
	// gets — so chains compose: the next hop prepends its own uuid and
	// forwards without translation.
	c, direct := nodeServer(t)
	ctx := context.Background()

	// The node grid lists the plugin as a link tile.
	ng, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "node1/0"})
	if err != nil {
		t.Fatalf("GetGrid(node grid): %v", err)
	}
	if len(ng.Tiles) != 1 || ng.Tiles[0].Id != "node1/ur1" || !ng.Tiles[0].Reference {
		t.Fatalf("node grid tiles = %+v, want one link tile node1/ur1", ng.Tiles)
	}
	pluginRoot := ng.Tiles[0].ChildGridId

	// Create through the export, verify via the direct plugin client: the
	// export writes to the same plugin, ids peeled one segment per hop.
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: pluginRoot,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 1, Y: 1, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile via export: %v", err)
	}
	if got := created.Tile.Id; got[:4] != "ur1/" {
		t.Errorf("created id = %q, want qualified ur1/<n>", got)
	}
	// The body follows through the export's WriteContent; verify via the
	// DIRECT plugin client — the export writes to the same plugin, ids
	// peeled one segment per hop.
	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent via export: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: created.Tile.Id, Version: created.Tile.Version, Data: []byte("# via export")}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := w.CloseAndRecv(); err != nil {
		t.Fatalf("close: %v", err)
	}
	localID := created.Tile.Id[4:]
	r, err := direct.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: localID})
	if err != nil {
		t.Fatalf("direct ReadContent: %v", err)
	}
	var body []byte
	for {
		chunk, rerr := r.Recv()
		if rerr != nil {
			break
		}
		body = append(body, chunk.Data...)
	}
	if string(body) != "# via export" {
		t.Errorf("content = %q, want %q", body, "# via export")
	}
}

func TestNodeExportUnknownPluginIsNotFound(t *testing.T) {
	c, _ := nodeServer(t)
	_, err := c.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: "no-such-plugin/1"})
	if status.Code(err) != gcodes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestNodeExportSubscribeStreamsQualifiedEvents(t *testing.T) {
	// Server-stream through the export: the whole node's fan-in, ids
	// qualified — this is how a mounting laptop hears a remote plugin change.
	c, _ := nodeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sub, err := c.Subscribe(ctx, &gridwellv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ng, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "node1/0"})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	if _, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: ng.Tiles[0].ChildGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1},
	}); err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	ev, err := sub.Recv()
	if err != nil {
		t.Fatalf("no event through the export after a mutation: %v", err)
	}
	if tc := ev.GetTileChanged(); tc != nil && tc.Tile.Id[:4] != "ur1/" {
		t.Errorf("event tile id = %q, want qualified", tc.Tile.Id)
	}
}

func TestNodeExportOpenShellBidi(t *testing.T) {
	// The PTY path is full-duplex and routed by the bind message's tile id;
	// it must survive the export + h2c hop.
	c, _ := nodeServer(t)
	ctx := context.Background()
	ng, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "node1/0"})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	shellTile, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: ng.Tiles[0].ChildGridId,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 3, Y: 3, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}
	stream, err := c.OpenShell(ctx)
	if err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if err := stream.Send(&gridwellv1.OpenShellRequest{TileId: shellTile.Tile.Id, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := stream.Send(&gridwellv1.OpenShellRequest{Data: []byte("ping")}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(resp.Data) != "ping" {
		t.Errorf("shell echo through export = %q, want %q", resp.Data, "ping")
	}
	_ = stream.CloseSend()
}

// TestNodeExportContentStreams drives the new content verbs over the raw
// gRPC export — the wire a remote mounter sees. WriteContent commits at
// close and the response re-qualifies; ReadContent streams the bytes paired
// with their version; PlaceTile routes like every unary.
func TestNodeExportContentStreams(t *testing.T) {
	c, _ := nodeServer(t)
	ctx := context.Background()

	ng, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "node1/0"})
	if err != nil {
		t.Fatalf("GetGrid(node grid): %v", err)
	}
	pluginRoot := ng.Tiles[0].ChildGridId
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: pluginRoot,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}

	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{
		TileId: created.Tile.Id, Version: created.Tile.Version, Data: []byte("# via the export"),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := w.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	if got := resp.Tile.Id; got != created.Tile.Id {
		t.Errorf("response id = %q, want re-qualified %q", got, created.Tile.Id)
	}

	r, err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	var data []byte
	var version int64
	first := true
	for {
		chunk, err := r.Recv()
		if err != nil {
			break
		}
		if first {
			version = chunk.Version
			first = false
		}
		data = append(data, chunk.Data...)
	}
	if string(data) != "# via the export" {
		t.Errorf("content = %q", data)
	}
	if version != resp.Tile.Version {
		t.Errorf("version %d, want %d — the basis must survive the export hop", version, resp.Tile.Version)
	}

	placed, err := c.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: created.Tile.Id, Version: resp.Tile.Version, GridId: pluginRoot, X: 5, Y: 5, W: 3, H: 3,
	})
	if err != nil {
		t.Fatalf("PlaceTile: %v", err)
	}
	if placed.Tile.W != 3 || placed.Tile.X != 5 {
		t.Errorf("placed = (%d,%d %dx%d), want (5,5 3x3)", placed.Tile.X, placed.Tile.Y, placed.Tile.W, placed.Tile.H)
	}
}
