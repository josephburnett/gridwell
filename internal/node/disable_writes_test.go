package node

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A disabled connection's remembered grids read as read-only and its writes
// are a verdict, never a transport failure: a far node answers once, is
// cached, and is switched off with its grid on screen. Across the whole seam,
// from the web door through the router and the source cache to a real far
// node, because the verdict and the stamp are the router's and the memory is
// the cache's.
func TestADisabledConnectionRefusesWritesAndReadsReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	farHome := t.TempDir()
	farCfg, err := BuildConfig(farHome, filepath.Join(farHome, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	farCfg.Web.Bind = "127.0.0.1:0"
	far, err := Start(Options{Home: farHome, Cfg: farCfg})
	if err != nil {
		t.Fatal(err)
	}
	far.ServeBackground()
	t.Cleanup(func() { _ = far.Close() })

	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	cfg.Connections = []config.ConnectionConfig{{Name: "far", Label: "Far", Addr: config.FederationSocket(farHome)}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	t.Cleanup(func() { _ = n.Close() })
	cl := webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword)

	ns := rpc.QualifyID(cfg.ID, "far")
	root := farRoot(ctx, t, cl, ns)
	note, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("far words"))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := cl.GetGrid(ctx, root); err != nil || !g.GetGrid().GetWritable() {
		t.Fatalf("the far home before the disable = %v (%v), want served writable", g.GetGrid(), err)
	}

	// The grid is on screen: the stream is open and the set names it. The
	// stream opens on its first event, so it is read beside the rest.
	changed := make(chan string, 64)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if !ok || err != nil {
				return
			}
			if gc := ev.GetGridChanged(); gc != nil {
				changed <- gc.GetGridId()
			}
		}
	}()
	if err := cl.SetInterest(ctx, []string{root}); err != nil {
		t.Fatal(err)
	}
	awaitInterest(ctx, t, root)

	if err := cl.DisableSource(ctx, ns); err != nil {
		t.Fatal(err)
	}
	for told := false; !told; {
		select {
		case id := <-changed:
			told = id == root
		case <-ctx.Done():
			t.Fatal("the view showing the far home was never told to read it again")
		}
	}

	want := rpc.DisabledReason("Far")
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("a disabled connection's remembered grid did not serve: %v", err)
	}
	if g.GetGrid().GetWritable() || g.GetGrid().GetAcceptsTiles() {
		t.Errorf("the remembered grid is writable=%v accepts_tiles=%v, want neither", g.GetGrid().GetWritable(), g.GetGrid().GetAcceptsTiles())
	}
	if len(g.GetTiles()) == 0 {
		t.Fatal("the remembered grid served no rows")
	}
	for _, tl := range g.GetTiles() {
		if tl.GetReadOnly() != want {
			t.Errorf("row %s read_only = %q, want %q", tl.GetId(), tl.GetReadOnly(), want)
		}
	}
	if tl, err := cl.GetTile(ctx, note.GetId()); err != nil || tl.GetReadOnly() != want {
		t.Errorf("the row by id = %v (%v), want read_only %q", tl, err, want)
	}

	writes := map[string]error{}
	_, writes["SetFraming"] = cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 1, Cy: 2, Zoom: 1})
	_, writes["SetFraming tile"] = cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: note.GetId(), Cx: 1, Cy: 2, Zoom: 1})
	_, writes["WriteContent"] = cl.WriteContent(ctx, note.GetId(), rpc.BasisOf(note), []byte("lost"))
	_, writes["CreateTile"] = cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}})
	_, writes["DeleteTile"] = cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: note.GetId()})
	for verb, err := range writes {
		if !gwerr.IsSourceDisabled(err) {
			t.Errorf("%s on a disabled connection = %v (code %v), want the disabled verdict", verb, err, connect.CodeOf(err))
		}
		if connect.CodeOf(err) == connect.CodeUnavailable {
			t.Errorf("%s on a disabled connection read as a transport failure, which the client parks and retries forever", verb)
		}
	}
}

// farRoot is the connection's landing, once the transport has learned it.
func farRoot(ctx context.Context, t *testing.T, cl *rpc.Client, ns string) string {
	t.Helper()
	for ctx.Err() == nil {
		lp, err := cl.Handshake(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range lp.GetPlugins() {
			if p.GetUuid() == ns && p.GetRootGridId() != "" && p.GetInfoError() == "" {
				return p.GetRootGridId()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("connection %s never landed", ns)
	return ""
}

// awaitInterest waits until the node counts root as on screen (its union
// trace names it); the set and the stream reach it as two calls.
func awaitInterest(ctx context.Context, t *testing.T, root string) {
	t.Helper()
	for ctx.Err() == nil {
		for _, rec := range trace.Default().Snapshot() {
			if rec.Src == "interest" && rec.Kind == "union" && slices.Contains(strings.Fields(rec.Msg), root) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the node never counted %s on screen", root)
}
