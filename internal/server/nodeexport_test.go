package server_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
)

// nodeServer stands up a full Server (one in-process localdb registered as
// uuid "ur1", label "personal") behind its NodeHandler on a real TCP listener,
// and returns a raw gRPC client dialed at it — the exact wire a remote
// ssh-plugin sees after its tunnel. Nothing here uses go-plugin or Electron:
// this is the "remote gridwell serve" side of the federation seam.
func nodeServer(t *testing.T) (gridwellv1.GridwellClient, gridwellv1.GridwellClient) {
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
	srv := server.New(reg, server.Config{})

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

// scoped returns a context carrying the gridwell-plugin selector.
func scoped(key string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), server.PluginHeader, key)
}

func TestNodeHandlerFrontDoorSpeaksGRPC(t *testing.T) {
	// The same port a browser hits over HTTP/1.1 must answer the gRPC
	// protocol over h2c: an UNSCOPED call reaches the client-facing Connect
	// handler (which speaks gRPC natively once HTTP/2 is negotiable).
	c, _ := nodeServer(t)
	resp, err := c.ListPlugins(context.Background(), &gridwellv1.ListPluginsRequest{})
	if err != nil {
		t.Fatalf("front-door ListPlugins over gRPC: %v", err)
	}
	if len(resp.Plugins) != 1 || resp.Plugins[0].Uuid != "ur1" || resp.Plugins[0].Label != "personal" {
		t.Fatalf("plugins = %+v, want the one registered localdb", resp.Plugins)
	}
}

func TestNodeExportScopesToOnePluginVerbatim(t *testing.T) {
	// A call carrying gridwell-plugin metadata must reach THAT plugin's own
	// service verbatim — local ids, the plugin's own Info — exactly what a
	// local plugin subprocess would answer. Transparency is the contract:
	// compare against the plugin's direct answers byte-for-byte.
	c, direct := nodeServer(t)

	want, err := direct.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("direct Info: %v", err)
	}
	got, err := c.Info(scoped("ur1"), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("scoped Info: %v", err)
	}
	if got.Kind != want.Kind || got.RootGridId != want.RootGridId || got.Watch != want.Watch || got.Writable != want.Writable {
		t.Fatalf("scoped Info = %+v, want the plugin's own %+v", got, want)
	}
	if strings.Contains(got.RootGridId, "/") {
		t.Fatalf("RootGridId %q is qualified; the export must speak LOCAL ids", got.RootGridId)
	}

	// Create + read through the export, verify via the direct client: the
	// export writes to the same plugin, in the plugin's own id space.
	created, err := c.CreateTile(scoped("ur1"), &gridwellv1.CreateTileRequest{
		GridId: got.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 1, Y: 1, W: 2, H: 2},
		Data:   []byte("# via export"),
	})
	if err != nil {
		t.Fatalf("scoped CreateTile: %v", err)
	}
	body, err := direct.GetTileContent(context.Background(), &gridwellv1.GetTileContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("direct GetTileContent: %v", err)
	}
	if string(body.Data) != "# via export" {
		t.Errorf("content = %q, want %q", body.Data, "# via export")
	}
}

func TestNodeExportResolvesConfigName(t *testing.T) {
	c, _ := nodeServer(t)
	if _, err := c.Info(scoped("personal"), &gridwellv1.InfoRequest{}); err != nil {
		t.Fatalf("Info scoped by config name: %v", err)
	}
}

func TestNodeExportUnknownPluginIsNotFound(t *testing.T) {
	c, _ := nodeServer(t)
	_, err := c.Info(scoped("no-such-plugin"), &gridwellv1.InfoRequest{})
	if status.Code(err) != gcodes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestNodeExportSubscribeStreams(t *testing.T) {
	// Server-stream through the export: subscribe to the plugin's own event
	// stream, mutate through the export, and receive the event — this is how
	// a laptop's fan-in hears a remote plugin change.
	c, _ := nodeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := c.Info(scoped("ur1"), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	sub, err := c.Subscribe(metadata.AppendToOutgoingContext(ctx, server.PluginHeader, "ur1"), &gridwellv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := c.CreateTile(scoped("ur1"), &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1},
		Data:   []byte("x"),
	}); err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	if _, err := sub.Recv(); err != nil {
		t.Fatalf("no event through the export after a mutation: %v", err)
	}
}

func TestNodeExportOpenShellBidi(t *testing.T) {
	// The PTY path is full-duplex; it must survive the export + h2c hop.
	c, _ := nodeServer(t)
	info, err := c.Info(scoped("ur1"), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	shellTile, err := c.CreateTile(scoped("ur1"), &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 3, Y: 3, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}
	stream, err := c.OpenShell(scoped("ur1"))
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
