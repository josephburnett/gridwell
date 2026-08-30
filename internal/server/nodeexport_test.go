package server_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// writeOne sends one complete value through a namespace's WriteContent —
// the caller's half of the stream, ending at io.EOF (the clean end that
// commits).
func writeOne(t *testing.T, c namespace.Namespace, tileID string, version int64, data []byte) *gridwellv1.TileResponse {
	t.Helper()
	sent := false
	resp, err := c.WriteContent(context.Background(), func() (*gridwellv1.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &gridwellv1.WriteContentRequest{TileId: tileID, Version: version, Data: data}, nil
	})
	if err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	return resp
}

// readOne drains a namespace's ReadContent, returning the bytes and the
// version chunk 1 paired them with.
func readOne(t *testing.T, c namespace.Namespace, tileID string) ([]byte, int64) {
	t.Helper()
	var data []byte
	var version int64
	first := true
	if err := c.ReadContent(context.Background(), &gridwellv1.ReadContentRequest{TileId: tileID},
		func(chunk *gridwellv1.ContentChunk) error {
			if first {
				version = chunk.Version
				first = false
			}
			data = append(data, chunk.Data...)
			return nil
		}); err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	return data, version
}

// homeRoot is where a mounter lands: the node's home root, from the same
// handshake a client boots on.
func homeRoot(t *testing.T, c namespace.Namespace) string {
	t.Helper()
	lp, err := c.Handshake(context.Background(), &gridwellv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	for _, p := range lp.Plugins {
		if p.RootGridId != "" {
			return p.RootGridId
		}
	}
	t.Fatal("no rooted entry in the handshake")
	return ""
}

// nodeServer stands up a full Server (one in-process store
// registered as uuid "ur1", label "personal") behind its FederationHandler on a real
// TCP listener, and returns a raw gRPC client dialed at it — the exact wire a
// remote ssh-plugin sees after its tunnel. Every request routes by the
// QUALIFIED ids it carries; there is no scoping header and no name-based
// selection.
func nodeServer(t *testing.T) (namespace.Namespace, namespace.Namespace) {
	return nodeServerCfg(t, server.Config{})
}

// nodeServerCfg is nodeServer with the server config under test control
// (shells_disabled_test.go flips DisableShells; everything else uses the
// plain nodeServer default).
func nodeServerCfg(t *testing.T, cfg server.Config) (namespace.Namespace, namespace.Namespace) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	impl := local.New(st, shellsvc.NewManager(shellsvctest.New()))
	direct := impl

	reg := plugin.NewRegistry()
	reg.Register("ur1", "home", direct, nil)
	reg.SetLabel("ur1", "personal")
	srv := servertest.New(t, reg, cfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.FederationHandler(), Protocols: server.NodeProtocols()}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return namespace.FromClient(gridwellv1.NewGridwellClient(conn)), direct
}

func TestNodeExportInfoDescribesTheNode(t *testing.T) {
	// A mounter's Info handshake sees the NODE: its HOME as the root (where
	// a direct client lands too), watchable (the fan-in), read-only.
	c, _ := nodeServer(t)
	info, err := c.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info over gRPC: %v", err)
	}
	if want := homeRoot(t, c); info.RootGridId != want {
		t.Errorf("RootGridId = %q, want the home root %q", info.RootGridId, want)
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

	pluginRoot := homeRoot(t, c)
	if pluginRoot[:4] != "ur1/" {
		t.Fatalf("home root = %q, want the plugin's qualified root ur1/<n>", pluginRoot)
	}

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
	writeOne(t, c, created.Tile.Id, created.Tile.Version, []byte("# via export"))
	localID := created.Tile.Id[4:]
	body, _ := readOne(t, direct, localID)
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

	evs := make(chan *gridwellv1.Event, 8)
	go func() {
		_ = c.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			select {
			case evs <- ev:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	time.Sleep(200 * time.Millisecond) // the fan-in is up before the mutation
	if _, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: homeRoot(t, c),
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1},
	}); err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	select {
	case ev := <-evs:
		if tc := ev.GetTileChanged(); tc != nil && tc.Tile.Id[:4] != "ur1/" {
			t.Errorf("event tile id = %q, want qualified", tc.Tile.Id)
		}
	case <-ctx.Done():
		t.Fatal("no event through the export after a mutation")
	}
}

func TestNodeExportOpenShellBidi(t *testing.T) {
	// The PTY path is full-duplex and routed by the bind message's tile id;
	// it must survive the export + h2c hop.
	c, _ := nodeServer(t)
	ctx := context.Background()
	shellTile, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: homeRoot(t, c),
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 3, Y: 3, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}
	shellCtx, shellCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shellCancel()
	up := make(chan *gridwellv1.OpenShellRequest, 2)
	up <- &gridwellv1.OpenShellRequest{TileId: shellTile.Tile.Id, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}}
	up <- &gridwellv1.OpenShellRequest{Data: []byte("ping")}
	down := make(chan []byte, 4)
	go func() {
		_ = c.OpenShell(shellCtx, func() (*gridwellv1.OpenShellRequest, error) {
			select {
			case m := <-up:
				return m, nil
			case <-shellCtx.Done():
				return nil, io.EOF
			}
		}, func(r *gridwellv1.OpenShellResponse) error {
			select {
			case down <- r.Data:
			case <-shellCtx.Done():
			}
			return nil
		})
	}()
	select {
	case got := <-down:
		if string(got) != "ping" {
			t.Errorf("shell echo through export = %q, want %q", got, "ping")
		}
	case <-shellCtx.Done():
		t.Fatal("no shell output through the export")
	}
}

// TestNodeExportContentStreams drives the new content verbs over the raw
// gRPC export — the wire a remote mounter sees. WriteContent commits at
// close and the response re-qualifies; ReadContent streams the bytes paired
// with their version; PlaceTile routes like every unary.
func TestNodeExportContentStreams(t *testing.T) {
	c, _ := nodeServer(t)
	ctx := context.Background()

	pluginRoot := homeRoot(t, c)
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: pluginRoot,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}

	resp := writeOne(t, c, created.Tile.Id, created.Tile.Version, []byte("# via the export"))
	if got := resp.Tile.Id; got != created.Tile.Id {
		t.Errorf("response id = %q, want re-qualified %q", got, created.Tile.Id)
	}

	data, version := readOne(t, c, created.Tile.Id)
	if string(data) != "# via the export" {
		t.Errorf("content = %q", data)
	}
	if version != resp.Tile.Version {
		t.Errorf("version %d, want %d — the basis must survive the export hop", version, resp.Tile.Version)
	}

	placed, err := c.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: created.Tile.Id, GridId: pluginRoot, X: 5, Y: 5, W: 3, H: 3,
	})
	if err != nil {
		t.Fatalf("PlaceTile: %v", err)
	}
	if placed.Tile.W != 3 || placed.Tile.X != 5 {
		t.Errorf("placed = (%d,%d %dx%d), want (5,5 3x3)", placed.Tile.X, placed.Tile.Y, placed.Tile.W, placed.Tile.H)
	}
}

// TestNodeExportSearches pins that Search crosses the node export. Why this
// existed as a gap: the export delegates every unary verb by hand, Search was
// simply never added, and BOTH downstream layers (the remote transport and
// the connect handler's fan-out) treat any per-hop error — Unimplemented
// included — as "that hop contributes nothing", so a federated search
// silently answered empty instead of failing loudly anywhere.
func TestNodeExportSearches(t *testing.T) {
	c, _ := nodeServer(t)
	ctx := context.Background()

	pluginRoot := homeRoot(t, c)
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: pluginRoot,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 1, Y: 1, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	writeOne(t, c, created.Tile.Id, created.Tile.Version, []byte("# xylophone notes"))

	// Free text finds the tile, id qualified for THIS hop's view.
	resp, err := c.Search(ctx, &gridwellv1.SearchRequest{Query: "xylophone", Limit: 10})
	if err != nil {
		t.Fatalf("Search via export: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tile.Id != created.Tile.Id {
		t.Fatalf("search results = %+v, want the created tile %s", resp.Results, created.Tile.Id)
	}

	// The id: form routes too.
	resp, err = c.Search(ctx, &gridwellv1.SearchRequest{Query: "id:" + created.Tile.Id, Limit: 1})
	if err != nil {
		t.Fatalf("Search id: via export: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tile.Id != created.Tile.Id {
		t.Fatalf("id search results = %+v, want %s", resp.Results, created.Tile.Id)
	}
}
