package pluginhost_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// darkableCP forwards to a live plugin until dark is set, after which every
// unary call answers Unavailable: what a crashed plugin subprocess looks like
// to the adapter. That is distinct from a dark source, where the process
// answers and only the directory read fails.
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

// A dark plugin, whose subprocess is gone, is answered by the node's one
// source cache, one layer up. The adapter itself keeps no memory: when the
// process stops answering, nothing about the node's half can be derived, not
// even the grid's declared face, so the read fails and the cache serves what
// this namespace last said. This crosses the whole seam the production wiring
// crosses — sourcecache.Store.Front over the adapter, through the registry,
// the server, and the wire client — because a unit test on either side alone
// would not catch the two disagreeing.
func TestDarkPluginServesItsLastGridThroughTheCache(t *testing.T) {
	root := seedTree(t)
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	dc := &darkableCP{PluginClient: cp}
	cache, err := sourcecache.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	// The content-plugin policy — the engine, no crawl — with the serve-first
	// window shrunk to a second (fetched_at is second-granular), so a short
	// sleep puts the dark read past it and it wears the signal this test
	// pins: within the window a remembered answer serves unstamped,
	// indistinguishable from live on purpose.
	cached := cache.Front(pluginhost.New(dc, memStore.Namespace("p1")),
		sourcecache.Options{FreshWindow: time.Second})
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
	// Nothing here has been touched, so every tile in that answer is
	// SYNTHESIZED — named by its key, backed by no row. Those are exactly the
	// tiles the cache has to be able to replay, and the ones a store-only
	// memory could not have produced.
	for _, tile := range before.Tiles {
		if rpc.ShapeOf(rpc.LocalOf(tile.ID)) != rpc.ShapeKey {
			t.Fatalf("browsing minted a row for %q (%s)", tile.AltText, tile.ID)
		}
	}

	dc.dark.Store(true)
	// Even the handshake is answered from the cache: Info is the plugin's own
	// fact, and without it the client would not know where to land.
	if _, err := cl.Handshake(ctx); err != nil {
		t.Fatalf("dark plugin lost the handshake: %v", err)
	}
	time.Sleep(1200 * time.Millisecond) // past the window: the read must say remembered
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
	// The plugin's DECLARATIONS are what the client renders the grid with, so
	// a dark plugin whose face changed would repaint the room: the host tint
	// would drop off every tile and the folder would turn into a well. They
	// come back from the cache with everything else. fs is the real spawned
	// binary here, so this also pins its declaration reaching the wire.
	if !before.Grid.HostContent {
		t.Fatal("the fs plugin no longer declares host_content: the host treatment is what this pins across dark")
	}
	if after.Grid.HostContent != before.Grid.HostContent || after.Grid.Glyph != before.Grid.Glyph {
		t.Fatalf("declared face drifted dark: host_content %v != %v, glyph %q != %q",
			after.Grid.HostContent, before.Grid.HostContent, after.Grid.Glyph, before.Grid.Glyph)
	}

	dc.dark.Store(false)
	// The healed read still serves the remembering — every read here is past
	// the shrunk window — but it kicks a live revalidation, and once one
	// lands the stamp clears. Poll: the window is a nanosecond, so only a
	// read that races in right behind a landed revalidation sees it fresh,
	// and the eventual unstamped answer is the healing this pins.
	deadline := time.Now().Add(10 * time.Second)
	for {
		healed, err := cl.GetGrid(ctx, rootGrid)
		if err != nil {
			t.Fatal(err)
		}
		if !healed.Grid.Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("healed plugin still stamped stale")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A dark source must not cost the user their arrangement, and a move made
// while dark is a fact of the node's own, so it stands and reads back
// immediately.
//
// Under serve-first the read after the move is answered by the cache's
// remembered listing, so the promise rests on two things: a write's response
// folds into the remembered rows, which is what keeps the move from being
// replayed over by the old placement; and the adapter's dark-source answer —
// its rows only, stamped stale — is what the revalidation sees and refuses
// to store, so the remembered listing (untouched entries included) survives
// the outage instead of being overwritten by the degraded one.
//
// The arrangement is what a ROW holds, so the test arranges the tile before
// the dark: an entry nobody has touched has no row and cannot be moved while
// the source is dark — the node would be minting a thing it cannot describe.
// That refusal is the last stanza.
func TestASourceGoingDarkDoesNotCostTheUserTheirArrangement(t *testing.T) {
	root := seedTree(t)
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
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
	// The arrangement the dark must not cost: one move while the source is
	// still readable, which is what mints the row.
	first, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: moved.ID, X: 8, Y: 8, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	moved = *first

	// The source goes dark: the process answers, the directory does not.
	lighten := darken(t, root)

	// The user drags the tile somewhere free. The write is the node's own
	// half, so it must land and report landing.
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
	// Within the serve-first window the remembered listing answers unstamped
	// — nothing vanishes just because the directory blinked — and the move
	// already reads back from it, folded in by the write's response.
	if g.Grid.Stale {
		t.Fatal("a within-window remembering must serve unstamped")
	}
	if len(g.Tiles) != len(before.Tiles) {
		t.Fatalf("dark source answered %d tiles, want the remembered %d: %+v", len(g.Tiles), len(before.Tiles), g.Tiles)
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

	// The source returns: the same tile, the same id, still where the user put
	// it while nobody could see the source.
	lighten()
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
	if len(healed.Tiles) != len(before.Tiles) {
		t.Fatalf("healed listing = %d tiles, want the original %d", len(healed.Tiles), len(before.Tiles))
	}

	// An entry with no row cannot be arranged while its source is dark: there
	// is nothing to derive a tile from, so the refusal is a plain NotFound
	// the client surfaces, never a silent no-op.
	var untouched rpc.Tile
	for _, tile := range healed.Tiles {
		if tile.AltText == "data.bin" {
			untouched = tile
		}
	}
	if untouched.ID == "" {
		t.Fatal("no untouched tile to try")
	}
	darken(t, root)
	if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: untouched.ID, X: 1, Y: 1, W: 1, H: 1}); err == nil {
		t.Fatal("placing an untouched entry while its source is dark must refuse, not invent a row")
	}
}
