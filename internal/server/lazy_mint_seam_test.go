package server

// Lazy minting through the whole stack: a real spawned fs plugin, the
// adapter, the router, the browser door, and the store's own file. Every
// assertion here is about the seam, because both halves are individually
// happy with either behavior — the store will mint a row whenever asked, and
// the plugin never knows there are rows at all. What must be true is the
// thing only the seam can be wrong about: browsing a source writes nothing,
// and the first durable touch writes exactly one row, at the placement the
// user was already looking at.

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// lazyStack is newTestServerWithPlugins keeping the store handle, because the
// question these tests ask is "what is in the file".
func lazyStack(t *testing.T) (cl *rpc.Client, st *store.Store, hs httpServer, fsRoot string) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, s)
	fsRoot = t.TempDir()
	// The plugin's namespace is a namespace of THIS store, not a store of its
	// own: the whole question is what lands in the node's one file.
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": fsRoot})
	reg.Register(fsPluginUUID, "fs", pluginhost.New(cp, s.Namespace(fsPluginUUID)), nil)
	reg.SetLabel(fsPluginUUID, "files")
	srv := mustNew(t, reg, Config{})
	h := serveWeb(t, srv)
	return rpc.NewClient(h.Client(), h.URL, connect.WithProtoJSON()), s, httpServer{h.URL, h.Client()}, fsRoot
}

type httpServer struct {
	URL    string
	client *http.Client
}

// pluginRows counts the tile and grid rows any plugin namespace owns. Home is
// ns = ” and is not lazy, so it is excluded: this is exactly the number
// browsing must not move.
func pluginRows(t *testing.T, st *store.Store) (tiles, grids int) {
	t.Helper()
	count := func(q string) int {
		var n int
		if err := st.SQL().QueryRow(q).Scan(&n); err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		return n
	}
	return count(`SELECT count(*) FROM tiles WHERE ns != ''`), count(`SELECT count(*) FROM grids WHERE ns != ''`)
}

// fsGrid seeds n files plus one subdirectory and returns the qualified grid id
// of the plugin's root, WITHOUT mounting it: a mount is a stored reference and
// would mint the root grid's row, which is the very thing being counted.
func fsGrid(t *testing.T, cl *rpc.Client, fsRoot string, n int) string {
	t.Helper()
	for i := range n {
		name := "f" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(fsRoot, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(fsRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fsRoot, "sub", "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range lp.Plugins {
		if p.UUID == fsPluginUUID {
			return p.RootGridID
		}
	}
	t.Fatal("no fs plugin in the handshake")
	return ""
}

// Browsing is a read. A hundred entries listed, descended into, and listed
// again leave the file exactly as they found it.
func TestListingAHundredEntriesMintsNothing(t *testing.T) {
	cl, st, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 100)
	tiles0, grids0 := pluginRows(t, st)

	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 101 {
		t.Fatalf("listed %d tiles, want 101", len(g.Tiles))
	}
	var sub rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "sub" {
			sub = tile
		}
		if tile.ID == "" {
			t.Fatalf("tile with no id: %+v", tile)
		}
	}
	if sub.ChildGridID == "" {
		t.Fatal("the subdirectory well opens into nothing")
	}
	// Descend, and list both grids again: still a read.
	if _, err := cl.GetGrid(ctx, sub.ChildGridID); err != nil {
		t.Fatalf("descent into an unminted well: %v", err)
	}
	if _, err := cl.GetGrid(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.GetGrid(ctx, sub.ChildGridID); err != nil {
		t.Fatal(err)
	}
	if tiles, grids := pluginRows(t, st); tiles != tiles0 || grids != grids0 {
		t.Fatalf("browsing wrote %d tile rows and %d grid rows; a listing must write none",
			tiles-tiles0, grids-grids0)
	}
}

// The derived answer IS the arrangement: touching every tile in a fresh grid
// changes the ids and nothing else — same keys, kinds, labels, placements, in
// the same order. That is what makes lazy minting invisible: the grid a user
// sees before they touch anything is the grid mint-on-list used to write.
func TestTouchingEveryTileChangesOnlyTheIds(t *testing.T) {
	cl, _, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 12)

	derived, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	// Every tile placed where it already is: the durable touch that mints,
	// with nothing about the arrangement changed.
	for _, tile := range derived.Tiles {
		if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
			TileID: tile.ID, GridID: root, X: tile.X, Y: tile.Y, W: tile.W, H: tile.H,
		}); err != nil {
			t.Fatalf("touch %q: %v", tile.AltText, err)
		}
	}
	minted, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(minted.Tiles) != len(derived.Tiles) {
		t.Fatalf("minting changed the tile count: %d != %d", len(minted.Tiles), len(derived.Tiles))
	}
	for i := range derived.Tiles {
		d, m := derived.Tiles[i], minted.Tiles[i]
		if d.AltText != m.AltText || d.Kind != m.Kind ||
			d.X != m.X || d.Y != m.Y || d.W != m.W || d.H != m.H {
			t.Fatalf("tile %d moved when it was minted: %+v != %+v", i, m, d)
		}
		if d.ID == m.ID {
			t.Fatalf("tile %d kept its derived address after a touch: %q", i, m.ID)
		}
	}
}

// One move, one row — and the row is not undone by the next listing.
func TestOneMoveMintsExactlyOneRow(t *testing.T) {
	cl, st, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 8)
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	tiles0, grids0 := pluginRows(t, st)

	moved, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: g.Tiles[3].ID, GridID: root, X: 9, Y: 4, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tiles1, grids1 := pluginRows(t, st)
	if tiles1 != tiles0+1 {
		t.Fatalf("a move wrote %d tile rows, want exactly 1", tiles1-tiles0)
	}
	if grids1 != grids0+1 {
		t.Fatalf("a move wrote %d grid rows, want exactly the one the tile belongs to", grids1-grids0)
	}
	// Re-listing does not undo it, and does not mint anything else.
	again, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tile := range again.Tiles {
		if tile.ID == moved.ID {
			found = true
			if tile.X != 9 || tile.Y != 4 || tile.W != 2 || tile.H != 2 {
				t.Fatalf("the move did not survive re-listing: %+v", tile)
			}
		}
	}
	if !found {
		t.Fatal("the moved tile is not in the next listing")
	}
	if tiles2, grids2 := pluginRows(t, st); tiles2 != tiles1 || grids2 != grids1 {
		t.Fatalf("re-listing after a move wrote %d tile and %d grid rows", tiles2-tiles1, grids2-grids1)
	}
}

// A link is a durable fact ABOUT the target, so dropping one on an untouched
// entry mints it — and what lands in the home row is the row id, never the
// derived address, because a reference at rest must name something that
// cannot reflow.
func TestALinkOntoAnUntouchedEntryMintsItAsARow(t *testing.T) {
	cl, st, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 5)
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	homeGrid := rpc.HomeGrid(lp)
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var leaf, well rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "sub" {
			well = tile
		} else if leaf.ID == "" {
			leaf = tile
		}
	}
	if leaf.ID == "" || well.ID == "" {
		t.Fatalf("need a leaf and a well: %+v", g.Tiles)
	}
	if _, ok := rpc.TileKey(rpc.LocalOf(leaf.ID)); !ok {
		t.Fatalf("the leaf was already minted: %q", leaf.ID)
	}

	// The left-drag onto home: a leaf link at its target, an exit well at the
	// directory's grid. The client sends exactly the ids it was shown.
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: homeGrid, X: 0, Y: 4, W: 1, H: 1, Kind: rpc.KindText,
		LinkTargetID: leaf.ID, Label: leaf.AltText,
	})
	if err != nil {
		t.Fatalf("leaf link onto an untouched entry: %v", err)
	}
	mount, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: homeGrid, X: 1, Y: 4, W: 1, H: 1,
		ChildGridID: well.ChildGridID, Label: "sub",
	})
	if err != nil {
		t.Fatalf("exit well onto an untouched directory: %v", err)
	}

	// What is at rest. A TILE reference names a row: the entry earned one when
	// the link was stored, because a derived placement can reflow and a link
	// must keep naming the same thing. A GRID reference keeps the address: a
	// grid answers to its address for good (the pane the user is standing in
	// holds that name), and the context key is as permanent as the plugin's
	// keys, so there is nothing a row would make safer.
	ns, local, ok := rpc.SplitID(link.LinkTargetID)
	if !ok || ns != fsPluginUUID || rpc.ShapeOf(local) != rpc.ShapeRow {
		t.Fatalf("link_target_id = %q: a leaf link at rest must name a row in the fs plugin", link.LinkTargetID)
	}
	if mount.ChildGridID != well.ChildGridID {
		t.Fatalf("child_grid_id = %q, want the grid the user dragged, %q", mount.ChildGridID, well.ChildGridID)
	}
	stored := storedReference(t, st, link.ID)
	if stored != link.LinkTargetID {
		t.Fatalf("the file holds %q, the wire says %q", stored, link.LinkTargetID)
	}
	// The link resolves to the same file it was dropped on.
	body, _, _, err := cl.ReadContent(ctx, link.ID)
	if err != nil {
		t.Fatalf("read through the link: %v", err)
	}
	if string(body) != leaf.AltText {
		t.Fatalf("the link reads %q, want the file %q", body, leaf.AltText)
	}
	if tiles, _ := pluginRows(t, st); tiles != 1 {
		t.Fatalf("the two drops minted %d tile rows, want exactly the one the leaf link names", tiles)
	}
}

// storedReference reads a home tile's link_target_id straight out of the file,
// so the assertion is about what is at rest, not about what the wire says.
func storedReference(t *testing.T, st *store.Store, qualifiedID string) string {
	t.Helper()
	var target sql.NullString
	if err := st.SQL().QueryRow(`SELECT link_target_id FROM tiles WHERE id = ? AND ns = ''`,
		rpc.LocalOf(qualifiedID)).Scan(&target); err != nil {
		t.Fatal(err)
	}
	return target.String
}

// Every read answers a derived address, all the way out to the browser door.
func TestKeyFormReadsRoundTrip(t *testing.T) {
	cl, st, hs, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 3)
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var leaf rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "f1.txt" {
			leaf = tile
		}
	}
	if leaf.ID == "" {
		t.Fatalf("no f1.txt among %+v", g.Tiles)
	}

	got, err := cl.GetTile(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("GetTile on a derived address: %v", err)
	}
	if got.ID != leaf.ID || got.AltText != "f1.txt" || got.X != leaf.X || got.Y != leaf.Y {
		t.Fatalf("GetTile answered a different tile: %+v != %+v", got, leaf)
	}
	body, media, _, err := cl.ReadContent(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("ReadContent on a derived address: %v", err)
	}
	if string(body) != "f1.txt" || !strings.HasPrefix(media, "text/plain") {
		t.Fatalf("content = (%q, %q)", body, media)
	}
	// The /content/ door: the same id in a URL path, served as HTTP.
	url := hs.URL + "/content/" + ContentToken(testPassword) + "/" + leaf.ID + "/"
	res, err := hs.client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the door answered %d for a derived address (%s)", res.StatusCode, url)
	}
	buf := make([]byte, 64)
	n, _ := res.Body.Read(buf)
	if string(buf[:n]) != "f1.txt" {
		t.Fatalf("the door served %q", buf[:n])
	}
	if tiles, grids := pluginRows(t, st); tiles != 0 || grids != 0 {
		t.Fatalf("reading minted %d tile and %d grid rows", tiles, grids)
	}
}

// A URL naming an untouched leaf resolves through the server, not just
// through the codec: decode it, walk the descent the way the client does, and
// the leaf's bytes come back.
func TestURLDescentToAKeyFormLeafResolves(t *testing.T) {
	cl, _, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 3)
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var sub rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "sub" {
			sub = tile
		}
	}
	deep, err := cl.GetGrid(ctx, sub.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deep.Tiles) != 1 {
		t.Fatalf("sub/ = %+v", deep.Tiles)
	}
	leafID := deep.Tiles[0].ID

	// What the address bar would hold, projected and read back.
	raw := pane.EncodeURL(pane.URLState{
		Anchor:  sub.ChildGridID,
		TileIDs: []string{rpc.LocalOf(leafID)},
	})
	st, err := pane.DecodeURL(raw)
	if err != nil {
		t.Fatalf("DecodeURL(%q): %v", raw, err)
	}
	if st.Anchor != sub.ChildGridID || len(st.TileIDs) != 1 {
		t.Fatalf("decoded %+v from %q", st, raw)
	}
	// The boot walk: the anchor grid, then the descent segment qualified with
	// the anchor's namespace.
	anchor, err := cl.GetGrid(ctx, st.Anchor)
	if err != nil {
		t.Fatalf("boot: anchor grid %q: %v", st.Anchor, err)
	}
	if anchor.Grid.ID == "" {
		t.Fatal("boot: anchor grid has no id")
	}
	qualified := rpc.QualifyID(rpc.UUIDOf(st.Anchor), st.TileIDs[0])
	tile, err := cl.GetTile(ctx, qualified)
	if err != nil {
		t.Fatalf("boot: descent to %q: %v", qualified, err)
	}
	if tile.AltText != "deep.txt" {
		t.Fatalf("boot landed on %q", tile.AltText)
	}
	body, _, _, err := cl.ReadContent(ctx, tile.ID)
	if err != nil || string(body) != "deep" {
		t.Fatalf("boot read %q (%v)", body, err)
	}
}
