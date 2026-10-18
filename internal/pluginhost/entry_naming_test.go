package pluginhost_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// treePlugin is a two-context source: a root with a directory-shaped well and
// a read-only document beside it, and the directory's own listing. Whether the
// root's listing is authoritative, and whether the document is still in it, are
// knobs the naming tests turn.
type treePlugin struct {
	pluginv1.UnimplementedPluginServer
	authoritative bool
	hideDoc       bool
}

func (p *treePlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{
		Kind: "tree", DisplayName: "tree", HostContent: true,
		MenuEntries: []*pluginv1.MenuEntry{{Id: "root", Label: "Tree", Context: "/"}},
	}, nil
}

func (p *treePlugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	switch req.Context {
	case "/":
		entries := []*pluginv1.Entry{
			{Key: "/sub", Kind: rpc.KindWell, Label: "sub", ChildContext: "/sub"},
		}
		if !p.hideDoc {
			entries = append(entries, &pluginv1.Entry{Key: "/note.md", Kind: rpc.KindText, Label: "note.md"})
		}
		return &pluginv1.ListResponse{Entries: entries, Authoritative: p.authoritative}, nil
	case "/sub":
		return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
			{Key: "/sub/deep.md", Kind: rpc.KindText, Label: "deep.md"},
		}}, nil
	}
	return &pluginv1.ListResponse{Authoritative: true}, nil
}

// Probe never commits: the source is reachable but will not say whether a key
// it stopped listing is really gone, which is the arm that KEEPS a row.
func (p *treePlugin) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
}

// treeNode builds the adapter over that plugin and hands back the root grid id
// plus the namespace of the store behind it — the row ids are storage now, so
// a test that needs one reads it where it lives.
func treeNode(t *testing.T, p *treePlugin) (*pluginhost.Adapter, string, *store.Namespace) {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ns := memStore.Namespace("p1")
	a := pluginhost.New(cp, ns, nil)
	info, err := a.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.MenuEntries) != 1 || info.MenuEntries[0].GridId == "" {
		t.Fatalf("handshake = %+v, want the one collection with a grid", info.MenuEntries)
	}
	return a, info.MenuEntries[0].GridId, ns
}

// rowIDOf is the minted row behind one entry, as a reference stored under the
// older rule holds it.
func rowIDOf(t *testing.T, ns *store.Namespace, context, key string) string {
	t.Helper()
	gid, ok, err := ns.LookupContext(context)
	if err != nil || !ok {
		t.Fatalf("context %q has no grid row (%v)", context, err)
	}
	id, ok, err := ns.LiveTileID(gid, key)
	if err != nil || !ok || id == 0 {
		t.Fatalf("entry %q has no row (%v)", key, err)
	}
	return strconv.FormatInt(id, 10)
}

// listed answers the wire tile the root listing carries for a label, or nil.
func listed(t *testing.T, a *pluginhost.Adapter, gridID, label string) *gridwellv1.Tile {
	t.Helper()
	g, err := a.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText == label {
			return tile
		}
	}
	return nil
}

// A plugin entry keeps the id the listing answers it under, across the mint.
// The key-form address names the entry by what it is, so it is derivable
// forever, and the row a first durable fact mints is bookkeeping the client
// never hears about. A rename would take the id out from under a URL restore or
// a descended pane. Both writes below are ones the user makes mid-descent, and
// each one mints: SetFraming on the doorway, and the framing verbs under the
// file.
func TestATouchedEntryKeepsTheIdTheListingAnswers(t *testing.T) {
	a, rootGrid, _ := treeNode(t, &treePlugin{authoritative: true})
	ctx := context.Background()

	sub := listed(t, a, rootGrid, "sub")
	note := listed(t, a, rootGrid, "note.md")
	if sub == nil || note == nil {
		t.Fatal("the root listing is missing its entries")
	}
	if rpc.ShapeOf(sub.Id) != rpc.ShapeKey || rpc.ShapeOf(note.Id) != rpc.ShapeKey {
		t.Fatalf("untouched entries are not named by address: %q, %q", sub.Id, note.Id)
	}

	// The doorway, reframed mid-descent: the URL is still carrying this id.
	if _, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: sub.Id, Cx: 1.5, Cy: -2, Zoom: 3}); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, a, rootGrid, "sub"); got == nil || got.Id != sub.Id {
		t.Fatalf("framing the doorway renamed it: %q, was %q — a URL segment naming it no longer resolves", got.GetId(), sub.Id)
	}

	// The read-only file, under the reader's hand: a scroll (SetTextScroll) and a
	// ctrl+wheel zoom (SetContentZoom). The pane's content id is this id.
	setText := &gridwellv1.SetTileRequest{TileId: note.Id, Tile: &gridwellv1.Tile{
		Kind: rpc.KindText, TextX: 0, TextY: 400, TextW: 600, TextH: 800, TextMode: "rendered",
	}}
	resp, err := a.SetTile(ctx, setText)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetTile().GetId() != note.Id {
		t.Fatalf("SetTextScroll answered %q, want the id it was addressed at (%q)", resp.GetTile().GetId(), note.Id)
	}
	zoom := 1.75
	if _, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: note.Id, ContentZoom: &zoom}); err != nil {
		t.Fatal(err)
	}
	after := listed(t, a, rootGrid, "note.md")
	if after == nil || after.Id != note.Id {
		t.Fatalf("framing the file renamed it: %q, was %q — the pane's content id names nothing", after.GetId(), note.Id)
	}
	if after.TextY != 400 || after.ContentZoom != zoom {
		t.Fatalf("the framing did not persist: %+v", after)
	}

	// And the id still answers everything it did before the mint.
	tile, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: note.Id})
	if err != nil {
		t.Fatal(err)
	}
	if tile.GetTile().GetId() != note.Id {
		t.Fatalf("GetTile(%q) answered %q; the asked name must be the answered name", note.Id, tile.GetTile().GetId())
	}
}

// Back-compat: an id minted before this rule — and stored, in a link target or
// a saved layout — still names its entry. Only what the LISTING advertises
// changed; the row is still an address the adapter reads on the way in, so
// every reference made under the old rule resolves to the thing it named.
func TestAStoredRowIdStillResolvesAfterTheListingKeepsTheAddress(t *testing.T) {
	a, rootGrid, ns := treeNode(t, &treePlugin{authoritative: true})
	ctx := context.Background()

	note := listed(t, a, rootGrid, "note.md")
	if note == nil {
		t.Fatal("no note.md")
	}
	// A durable touch mints the row a link stored under the older rule holds.
	if _, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: note.Id, X: 4, Y: 4, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	rowID := rowIDOf(t, ns, "/", "/note.md")

	// Reads resolve.
	got, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: rowID})
	if err != nil {
		t.Fatalf("a stored row id no longer reads: %v", err)
	}
	if got.GetTile().GetId() != note.Id {
		t.Fatalf("GetTile(%q) = %q, want the entry's one public id %q", rowID, got.GetTile().GetId(), note.Id)
	}
	// Writes resolve, and land on the same row the address does.
	zoom := 2.5
	if _, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: rowID, ContentZoom: &zoom}); err != nil {
		t.Fatalf("a stored row id no longer writes: %v", err)
	}
	if after := listed(t, a, rootGrid, "note.md"); after == nil || after.ContentZoom != zoom {
		t.Fatalf("the write through the row id did not reach the entry: %+v", after)
	}

	// A doorway held by a stored reference descends: the well's child grid is
	// the same grid the listing names.
	sub := listed(t, a, rootGrid, "sub")
	if _, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: sub.Id, Zoom: 2}); err != nil {
		t.Fatal(err)
	}
	subRow := rowIDOf(t, ns, "/", "/sub")
	viaRow, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: subRow})
	if err != nil {
		t.Fatal(err)
	}
	if viaRow.GetTile().GetChildGridId() != sub.ChildGridId {
		t.Fatalf("a stored doorway descends elsewhere: %q, want %q", viaRow.GetTile().GetChildGridId(), sub.ChildGridId)
	}
	if _, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: viaRow.GetTile().GetChildGridId()}); err != nil {
		t.Fatalf("the descent from a stored doorway failed: %v", err)
	}

	// And a reference RE-STORED from an old row id canonicalizes forward, so
	// the copy and the listing name one thing. MintRef is the router's one
	// door for that, and it mints nothing: the address is already canonical.
	fwd, err := a.MintRef(ctx, rowID)
	if err != nil {
		t.Fatal(err)
	}
	if fwd != note.Id {
		t.Fatalf("MintRef(%q) = %q, want the entry's public id %q", rowID, fwd, note.Id)
	}
	if same, err := a.MintRef(ctx, note.Id); err != nil || same != note.Id {
		t.Fatalf("MintRef(%q) = (%q, %v), want it unchanged", note.Id, same, err)
	}
	// A GRID row id re-stored canonicalizes forward too. Digits do not say
	// which table they came from, so this is the arm that would silently pass
	// a grid row through as if it were a tile's.
	gid, _, err := ns.LookupContext("/sub")
	if err != nil {
		t.Fatal(err)
	}
	gfwd, err := a.MintRef(ctx, strconv.FormatInt(gid, 10))
	if err != nil {
		t.Fatal(err)
	}
	if gfwd != sub.ChildGridId {
		t.Fatalf("MintRef on grid row %d = %q, want the grid's public id %q", gid, gfwd, sub.ChildGridId)
	}
}

// A minted row whose entry has left the source keeps its name too. The row
// records the key it was minted for, so the address is still derivable from the
// node's own fact — the id does not depend on the source being there to say it.
func TestAMintedRowNamesItselfWhenTheEntryLeavesTheSource(t *testing.T) {
	p := &treePlugin{} // non-authoritative: absence is not a verdict
	a, rootGrid, _ := treeNode(t, p)
	ctx := context.Background()

	note := listed(t, a, rootGrid, "note.md")
	if note == nil {
		t.Fatal("no note.md")
	}
	zoom := 1.25
	if _, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: note.Id, ContentZoom: &zoom}); err != nil {
		t.Fatal(err)
	}

	p.hideDoc = true
	after := listed(t, a, rootGrid, "note.md")
	if after == nil {
		t.Fatal("the row the user touched stopped being listed when the source stopped naming it")
	}
	if after.Id != note.Id {
		t.Fatalf("the row renamed itself once the source went quiet: %q, was %q", after.Id, note.Id)
	}
	if _, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: after.Id}); err != nil {
		t.Fatalf("the surviving row's own id does not read: %v", err)
	}
}
