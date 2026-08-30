package pluginhost_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/compose"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/sourcecache"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
)

// darkableCP forwards to a live plugin until dark is set — then every
// unary call answers Unavailable, exactly what a crashed plugin
// SUBPROCESS looks like to the adapter (distinct from the dark-SOURCE
// case, where the process answers and only the directory read fails).
type darkableCP struct {
	pluginv1.PluginClient
	dark atomic.Bool
}

func (d *darkableCP) Info(ctx context.Context, req *pluginv1.InfoRequest, opts ...grpc.CallOption) (*pluginv1.InfoResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "plugin process dark")
	}
	return d.PluginClient.Info(ctx, req, opts...)
}

func (d *darkableCP) List(ctx context.Context, req *pluginv1.ListRequest, opts ...grpc.CallOption) (*pluginv1.ListResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "plugin process dark")
	}
	return d.PluginClient.List(ctx, req, opts...)
}

func (d *darkableCP) Probe(ctx context.Context, req *pluginv1.ProbeRequest, opts ...grpc.CallOption) (*pluginv1.ProbeResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "plugin process dark")
	}
	return d.PluginClient.Probe(ctx, req, opts...)
}

// A DARK PLUGIN (the subprocess is gone) is answered by the node's ONE
// source cache, one layer up — the seam test for docs/simplify-plan.md
// S7. The adapter itself keeps no memory: when the process stops
// answering, nothing about the node's half can be derived (not even the
// grid's source kind), so the read fails and the cache serves what this
// namespace last said. This crosses the whole seam the production wiring
// crosses — sourcecache.Store.Front over the adapter, through the
// registry, the server and the wire client — because a unit test on
// either side alone would not catch the two disagreeing.
func TestDarkPluginServesItsLastGridThroughTheCache(t *testing.T) {
	root := seedTree(t)
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, cpCloser, err := compose.PluginInProcess(fsplugin.New(root, osRemoveHost{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	dc := &darkableCP{PluginClient: cp}
	cache, err := sourcecache.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	// The production policy for a content plugin: the engine, no crawl.
	cached := cache.Front(pluginhost.New(dc, memStore.Namespace("p1")), sourcecache.Options{})
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", cached, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx := context.Background()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	before, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Tiles) == 0 {
		t.Fatal("empty first read")
	}

	dc.dark.Store(true)
	// Even the handshake is answered from the cache: Info is the plugin's
	// own fact, and without it the client would not know where to land.
	if _, err := cl.Handshake(ctx); err != nil {
		t.Fatalf("dark plugin lost the handshake: %v", err)
	}
	after, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("dark plugin surfaced as an error instead of the remembered answer: %v", err)
	}
	if !after.Grid.Stale {
		t.Fatal("remembered answer not stamped stale")
	}
	if len(after.Tiles) != len(before.Tiles) {
		t.Fatalf("dark plugin changed the tile set: %d != %d", len(after.Tiles), len(before.Tiles))
	}
	for i := range before.Tiles {
		if after.Tiles[i].ID != before.Tiles[i].ID || after.Tiles[i].X != before.Tiles[i].X ||
			after.Tiles[i].AltText != before.Tiles[i].AltText {
			t.Fatalf("remembered tile drifted: %+v != %+v", after.Tiles[i], before.Tiles[i])
		}
	}
	if after.Grid.SourceKind != before.Grid.SourceKind {
		t.Fatalf("source kind drifted dark: %q != %q", after.Grid.SourceKind, before.Grid.SourceKind)
	}

	dc.dark.Store(false)
	healed, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Grid.Stale {
		t.Fatal("healed plugin still stamped stale")
	}
}

// The guiding rule, at the seam that used to break it: a DARK SOURCE
// must not cost the user their arrangement, and a move made WHILE dark
// is a fact of the node's own, so it stands and reads back immediately.
//
// This is why the adapter answers a dark source from its ROWS instead of
// failing into the cache: the cache's grid response is a JOIN of the
// source's facts and the node's, and replaying it would replay the OLD
// placement over the new one — the move would appear to fail and then
// reappear when the source came back.
func TestASourceGoingDarkDoesNotCostTheUserTheirArrangement(t *testing.T) {
	root := seedTree(t)
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	prov := fsplugin.New(root, osRemoveHost{})
	cp, cpCloser, err := compose.PluginInProcess(prov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	cache, err := sourcecache.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	cached := cache.Front(pluginhost.New(cp, memStore.Namespace("p1")), sourcecache.Options{})
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", cached, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx := context.Background()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	before, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var moved rpc.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			moved = tile
		}
	}
	if moved.ID == "" {
		t.Fatal("no notes.md tile to move")
	}

	// The share goes dark: the process answers, the directory does not.
	prov.SetReadDir(func(string) ([]fssource.Entry, error) { return nil, os.ErrPermission })

	// The user drags the tile somewhere free. The write is the node's own
	// half; it must land AND report landing.
	placed, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: moved.ID, X: 9, Y: 9, W: 1, H: 1})
	if err != nil {
		t.Fatalf("a placement while the source is dark must still land: %v", err)
	}
	if placed.X != 9 || placed.Y != 9 {
		t.Fatalf("placement answered %d,%d, want 9,9", placed.X, placed.Y)
	}
	g, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("dark source surfaced as an error: %v", err)
	}
	if !g.Grid.Stale {
		t.Fatal("a dark source must stamp the grid stale")
	}
	if len(g.Tiles) != len(before.Tiles) {
		t.Fatalf("dark source changed the tile set: %d != %d", len(g.Tiles), len(before.Tiles))
	}
	var back rpc.Tile
	for _, tile := range g.Tiles {
		if tile.ID == moved.ID {
			back = tile
		}
	}
	if back.X != 9 || back.Y != 9 {
		t.Fatalf("the move made while dark was lost: %+v", back)
	}
	if back.AltText != "notes.md" {
		t.Fatalf("the label the node minted was lost: %q", back.AltText)
	}

	// The share returns: the same tile, the same id, still where the user
	// put it while nobody could see the source.
	prov.SetReadDir(nil)
	healed, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Grid.Stale {
		t.Fatal("healed source still stamped stale")
	}
	for _, tile := range healed.Tiles {
		if tile.ID == moved.ID && (tile.X != 9 || tile.Y != 9) {
			t.Fatalf("the healed listing overwrote the user's placement: %+v", tile)
		}
	}
}
