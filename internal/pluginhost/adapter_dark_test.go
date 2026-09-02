package pluginhost_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

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

// A dark plugin — the subprocess is gone — fails honestly. Nothing between the
// router and the plugin remembers what it said: a plugin is a subprocess on
// this machine, and pretending it answered would hand the user a room that no
// longer exists. What the node itself minted is durable, so the arrangement
// comes back untouched the moment the process does.
//
// This crosses the whole seam the production wiring crosses — the adapter,
// through the registry, the server, and the wire client — because a unit test
// on either side alone would not catch the two disagreeing about what an
// unreachable plugin looks like.
func TestADarkPluginFailsHonestlyAndKeepsTheNodesRows(t *testing.T) {
	root := seedTree(t)
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	dc := &darkableCP{PluginClient: cp}
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", pluginhost.New(dc, memStore.Namespace("p1")), nil)
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
	// One durable touch: the arrangement is what a ROW holds, and the row is
	// the node's own fact, so it is what must survive the outage.
	var notes rpc.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			notes = tile
		}
	}
	if notes.ID == "" {
		t.Fatal("no notes.md tile to move")
	}
	placed, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: notes.ID, X: 7, Y: 3, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}

	dc.dark.Store(true)
	if g, err := cl.GetGrid(ctx, rootGrid); err == nil {
		t.Fatalf("a dark plugin answered %+v; with no memory of the source there is nothing to serve", g.Grid)
	}

	dc.dark.Store(false)
	healed, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("the plugin is back and the read still failed: %v", err)
	}
	if healed.Grid.Stale {
		t.Fatal("a live read must never wear the stale bit")
	}
	if len(healed.Tiles) != len(before.Tiles) {
		t.Fatalf("healed listing = %d tiles, want the original %d", len(healed.Tiles), len(before.Tiles))
	}
	var back rpc.Tile
	for _, tile := range healed.Tiles {
		if tile.ID == placed.ID {
			back = tile
		}
	}
	if back.X != 7 || back.Y != 3 || back.AltText != "notes.md" {
		t.Fatalf("the arrangement did not survive the outage: %+v", back)
	}
}

// A dark SOURCE — the plugin answers, its directory does not — is a different
// outage from a dark plugin, and it must not cost the user their arrangement.
// A move made while dark is a fact of the node's own, so it lands and reads
// back immediately, out of the rows the adapter overlays on an empty
// non-authoritative listing.
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
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", pluginhost.New(cp, memStore.Namespace("p1")), nil)
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
	// The rows answer, stamped stale and retiring nothing. An entry nobody
	// touched has no row to read from and is simply absent until the source
	// speaks again.
	if !g.Grid.Stale {
		t.Fatal("a rows-only answer must say so on the wire")
	}
	if len(g.Tiles) != 1 {
		t.Fatalf("dark source answered %d tiles, want only the touched one: %+v", len(g.Tiles), g.Tiles)
	}
	back := g.Tiles[0]
	if back.ID != moved.ID || back.X != 9 || back.Y != 9 {
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
