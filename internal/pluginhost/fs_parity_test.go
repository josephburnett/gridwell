package pluginhost_test

// The fs stack through a full server — the real gridwell-plugin-fs binary, the
// adapter, the store: placement and framing persist, sweeps remove only the
// dead, the node's own rows answer when the source goes dark, and a retired id
// never returns. The plugin is spawned, never linked, so the source goes dark
// the way a real one does, through an unreadable directory.

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const fsUUID = "fsuuidx"

// seedTree builds the directory both stacks project.
func seedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "notes.md"), []byte("# notes\n\nhello"), 0o644))
	must(os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "data.bin"), []byte{0x00, 0x01, 0x02}, 0o644))
	must(os.Mkdir(filepath.Join(root, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(root, "sub", "deep.md"), []byte("deeper"), 0o644))
	must(os.Mkdir(filepath.Join(root, "sub", "empty"), 0o755))
	return root
}

func pluginNode(t *testing.T, root string) *rpc.Client {
	t.Helper()
	cl, _ := pluginNodeAt(t, root, filepath.Join(t.TempDir(), "mem.db"))
	return cl
}

// pluginNodeAt builds the stack over an existing store path, and hands back the
// node's store beside the client: the rows are the half of the answer the wire
// never shows. The plugin is the shipped binary, configured with root exactly
// as a server.yaml plugins: entry would.
func pluginNodeAt(t *testing.T, root, memPath string) (*rpc.Client, *store.Store) {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), memStore
}

// darken makes root unreadable for the rest of the test — EACCES on every
// directory read, an unmounted share or a chmodded tree — and hands back the
// undo. The mode is restored at the end regardless, or the temp dir could not
// be cleaned up.
func darken(t *testing.T, root string) (lighten func()) {
	t.Helper()
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = os.Chmod(root, 0o755) }
	t.Cleanup(restore)
	return restore
}

func TestPluginServesTouchedRowsWhenSourceDark(t *testing.T) {
	// A source that stops answering costs the user what they ARRANGED and
	// nothing more: the adapter joins an empty non-authoritative listing, so
	// the rows the user touched still read, retiring nothing, while an entry
	// nobody ever touched has no row to read from and is simply absent until
	// the source speaks again.
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	before, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Tiles) == 0 {
		t.Fatalf("bad first read: %+v", before.Grid)
	}
	// One durable touch: the user drags notes.md somewhere. That is what
	// mints a row, and the row is what survives the dark.
	var notes *gridwellv1.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			notes = tile
		}
	}
	if notes.Id == "" {
		t.Fatal("no notes.md tile")
	}
	placed, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: notes.Id, GridId: rootGrid, X: 7, Y: 3, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The source goes dark — EACCES, an unmounted share — so every read fails
	// transiently.
	lighten := darken(t, root)
	after, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("dark source surfaced as an error instead of the remembered answer: %v", err)
	}
	if len(after.Tiles) != 1 {
		t.Fatalf("dark source answered %d tiles, want only the touched one: %+v", len(after.Tiles), after.Tiles)
	}
	if got := after.Tiles[0]; got.Id != placed.Id || got.X != 7 || got.Y != 3 || got.AltText != "notes.md" {
		t.Fatalf("the touched row drifted in the dark: %+v", got)
	}
	// The source returns and every entry is back.
	lighten()
	healed, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(healed.Tiles) != len(before.Tiles) {
		t.Fatalf("healed listing = %d tiles, want the original %d", len(healed.Tiles), len(before.Tiles))
	}
}

func TestDeleteRetiresOnTheWire(t *testing.T) {
	// The delete gesture through the full stack: the source is trashed and the
	// row retires, so Probe answers GONE, reads answer NotFound, a second
	// delete is a no-op, and a recreated file comes back with a fresh ROW,
	// holding none of the arrangement the retired one held.
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var bin *gridwellv1.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "data.bin" {
			bin = tile
		}
	}
	// Arrange it first: a row is what a retirement can burn, so the
	// "recreation mints fresh" half of the contract needs one. An entry nobody
	// ever touched has no row — deleting it is the plugin's verdict and
	// nothing else, which the last stanza pins.
	minted, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: bin.Id, GridId: rootGrid, X: 4, Y: 4, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if minted.Id != bin.Id {
		t.Fatalf("the placement renamed the entry: %q, was %q", minted.Id, bin.Id)
	}
	bin = minted
	if err := v2.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: bin.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); !os.IsNotExist(err) {
		t.Fatalf("source file not deleted: %v", err)
	}
	if _, err := v2.GetTile(ctx, bin.Id); err == nil {
		t.Fatal("a retired tile still reads")
	}
	if err := v2.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: bin.Id}); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	// Recreation mints a fresh ROW. The entry's public id is its key's address
	// and comes back with the key — the plugin contract says a key names one
	// thing for good — but nothing the retired row held comes back with it: the
	// file is listed at its hint again, not at the 4,4 the user had chosen, and
	// the retired row stays retired (TestRetiredKeyStaysRetiredWithoutIdBurn),
	// so a reference stored against it is dead for good.
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{9}, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	remade := tileNamed(g.Tiles, "data.bin")
	if remade.Id != bin.Id {
		t.Fatalf("a recreated file was renamed: %q, want the key's address %q", remade.Id, bin.Id)
	}
	if remade.X == 4 && remade.Y == 4 {
		t.Fatal("a recreated file inherited the retired row's placement")
	}
	// Deleting an UNTOUCHED entry involves no row at all: the plugin trashes
	// the file and the next listing simply does not name it.
	var doc *gridwellv1.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "notes.md" {
			doc = tile
		}
	}
	if err := v2.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: doc.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("untouched entry not deleted at the source: %v", err)
	}
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText == "notes.md" {
			t.Fatal("a deleted untouched entry is still listed")
		}
	}
}

// TestFSPluginPlacementAndFramingPersist: the user drags notes.md and
// frames the sub well; a later read serves both back verbatim, and a
// file that arrives afterwards lands in an empty cell, never on top of
// the placed tile ("things stay as you left them").
func TestFSPluginPlacementAndFramingPersist(t *testing.T) {
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) *gridwellv1.Tile {
		t.Helper()
		for _, tile := range g.Tiles {
			if tile.AltText == name {
				return tile
			}
		}
		t.Fatalf("%s not found", name)
		return &gridwellv1.Tile{}
	}
	notes, sub := find("notes.md"), find("sub")
	if _, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: notes.Id, GridId: rootGrid, X: 6, Y: 2, W: 2, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: sub.Id, Cx: 2, Cy: -1, Zoom: 1.4,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "later.md"), []byte("late"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	got, sub2, late := find("notes.md"), find("sub"), find("later.md")
	if got.X != 6 || got.Y != 2 || got.W != 2 || got.H != 1 {
		t.Fatalf("placement did not persist: %+v", got)
	}
	// The drag minted a row, so the tile is named by it from here on. The
	// address the client was already holding still resolves to the same tile:
	// an id in a bookmark or a link does not go stale because the thing it
	// names finally earned a row.
	if held, err := v2.GetTile(ctx, notes.Id); err != nil || held.Id != got.Id {
		t.Fatalf("the pre-mint address stopped resolving: %+v (%v), want %s", held, err, got.Id)
	}
	if sub2.ViewCx != 2 || sub2.ViewCy != -1 || sub2.ViewZoom != 1.4 {
		t.Fatalf("framing did not persist: %+v", sub2)
	}
	if late.X >= 6 && late.X < 8 && late.Y == 2 {
		t.Fatalf("a new file landed on the placed tile: %+v", late)
	}
}

// TestFSPluginSweepRemovesOnlyTheDead: a file deleted on disk is swept
// on the next read; every surviving tile keeps its id, and one the user
// ARRANGED keeps its placement. An untouched entry's placement is derived,
// so it may reflow when the directory's contents change — that is the whole
// difference between a derived answer and a row.
func TestFSPluginSweepRemovesOnlyTheDead(t *testing.T) {
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	before, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var arranged *gridwellv1.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			arranged = tile
		}
	}
	placed, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: arranged.Id, GridId: rootGrid, X: 6, Y: 6, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "data.bin")); err != nil {
		t.Fatal(err)
	}
	after, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tiles) != len(before.Tiles)-1 {
		t.Fatalf("sweep removed %d tiles, want exactly 1", len(before.Tiles)-len(after.Tiles))
	}
	survivors := map[string]*gridwellv1.Tile{}
	for _, tile := range after.Tiles {
		if tile.AltText == "data.bin" {
			t.Fatal("dead file still listed")
		}
		survivors[tile.AltText] = tile
	}
	for _, tile := range before.Tiles {
		if tile.AltText == "data.bin" || tile.AltText == "notes.md" {
			continue
		}
		s, ok := survivors[tile.AltText]
		if !ok || s.Id != tile.Id {
			t.Fatalf("survivor %s lost its identity: %+v != %+v", tile.AltText, s, tile)
		}
	}
	if s := survivors["notes.md"]; s.Id != placed.Id || s.X != 6 || s.Y != 6 {
		t.Fatalf("the arranged survivor drifted: %+v", s)
	}
}

// TestFSPluginTextViewPersists: a read-only host file's scroll position and
// mode are node facts, and the fs stack keeps them. The client used to skip
// posting SetTextView for a plugin-owned text tile because the pre-plugin fs
// refused the write (#236); this pins the answer the client now relies on, at
// the seam where it would change — the real binary, the adapter, the store.
//
// Framing carries no version claim, so the write must not bump the tile's
// version: what the user's bytes are has not changed.
func TestFSPluginTextViewPersists(t *testing.T) {
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if g.Grid.Writable {
		t.Fatal("the fs root grid answered writable: this test is about a READ-ONLY host tile")
	}
	var notes *gridwellv1.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "notes.md" {
			notes = tile
		}
	}
	if notes.Id == "" || notes.Kind != rpc.KindText {
		t.Fatalf("no read-only notes.md text tile: %+v", notes)
	}
	if _, err := v2.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: notes.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 12, TextY: 340, TextW: 600, TextH: 400, TextMode: rpc.TextModeRendered}}); err != nil {
		t.Fatalf("the fs stack refused text framing for a read-only file: %v", err)
	}
	held, err := v2.GetTile(ctx, notes.Id)
	if err != nil {
		t.Fatal(err)
	}
	if held.TextX != 12 || held.TextY != 340 || held.TextW != 600 || held.TextH != 400 ||
		held.TextMode != rpc.TextModeRendered {
		t.Fatalf("text framing did not persist: %+v", held)
	}
	if held.Version != notes.Version {
		t.Fatalf("framing bumped the version %d -> %d: framing claims no content bytes",
			notes.Version, held.Version)
	}
	// And it survives a fresh listing, which is the read a re-descent makes.
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText != "notes.md" {
			continue
		}
		if tile.TextY != 340 || tile.TextMode != rpc.TextModeRendered {
			t.Fatalf("the listing lost the framing: %+v", tile)
		}
		return
	}
	t.Fatal("notes.md vanished from the listing")
}

// The shipped fs binary, through the adapter and a full server: it declares
// its collection and no place of its own, and that declaration is what opens.
// Only the seam sees this — the plugin declares a context key and never learns
// whether it resolves, and the node resolves an id and never learns what the
// plugin meant by it. It is also the whole of issue #281: a plugin with
// collections and no root is healthy, and the collection is the doorway.
func TestAnEntriesOnlyPluginPresentsAndServes(t *testing.T) {
	root := seedTree(t)
	cl := pluginNode(t, root)
	ctx := context.Background()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := pl.Plugins[0]
	if row.RootGridId != "" {
		t.Errorf("a plugin is not a place; it named a grid of its own: %q", row.RootGridId)
	}
	if row.InfoError != "" {
		t.Errorf("declaring no place of its own is not a failure: %q", row.InfoError)
	}
	if len(row.MenuEntries) != 1 || row.MenuEntries[0].GridId == "" {
		t.Fatalf("row = %+v, want its one collection, resolved to a grid", row)
	}
	g, err := cl.GetGrid(ctx, row.MenuEntries[0].GridId)
	if err != nil {
		t.Fatalf("the declared collection does not serve: %v", err)
	}
	if len(g.Tiles) == 0 {
		t.Errorf("the collection listed nothing; the tree has files in it")
	}
}

// healthStream turns a client's event stream into the health transitions it
// carries. The Subscribe call rides the goroutine too, because the server
// sends nothing until it has something to say: a test waiting for the stream
// itself would hang where it should fail.
func healthStream(ctx context.Context, cl *rpc.Client) <-chan *gridwellv1.EventPluginHealth {
	out := make(chan *gridwellv1.EventPluginHealth, 16)
	go func() {
		s, err := cl.Subscribe(ctx)
		if err != nil {
			return // the awaiting test says so, with what it was waiting for
		}
		for {
			ev, ok, rerr := s.Recv()
			if rerr != nil || !ok {
				return
			}
			h := ev.GetPluginHealth()
			if h == nil {
				continue
			}
			select {
			case out <- h:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// awaitHealth waits for the next transition and says what it was.
func awaitHealth(t *testing.T, ch <-chan *gridwellv1.EventPluginHealth, healthy bool, why string) *gridwellv1.EventPluginHealth {
	t.Helper()
	select {
	case h := <-ch:
		if h.GetHealthy() != healthy {
			t.Fatalf("%s: got healthy=%v (%s) instead", why, h.GetHealthy(), h.GetDetail())
		}
		return h
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: no health event arrived", why)
	}
	return nil
}

// A source that stops answering is this namespace's health, published by the
// adapter: the client hears it on the stream it already holds, with no call of
// its own having to fail, and the rooms that source serves are memories from
// then on (client/cache.SourceDark). It is the same event and the same uuid
// the supervisor uses for the subprocess, because "a declared source is not
// answering" is one fact whichever half of the plugin it is. Across the real
// wiring — the shipped binary, the node's fan-in, the client's stream —
// because the fact crosses all three.
func TestADarkSourceIsPublishedAsHealth(t *testing.T) {
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}

	// The source goes dark with nobody attached. A subscriber arriving now is
	// owed the outage, since the transition it missed is not repeated.
	lighten := darken(t, root)
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatalf("a dark source must still serve the node's rows: %v", err)
	}
	health := healthStream(ctx, v2)
	down := awaitHealth(t, health, false, "a subscriber arriving mid-outage is told")
	if down.GetPluginUuid() == "" {
		t.Error("the fan-in must name the namespace the outage is about")
	}
	if !strings.Contains(down.GetDetail(), "source is not answering") {
		t.Errorf("detail = %q, want the source named rather than the process", down.GetDetail())
	}

	// The recovery is a transition on the stream this client is holding.
	lighten()
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}
	awaitHealth(t, health, true, "the source answered again")

	// Only the transitions: every listing would otherwise republish an outage
	// the client already knows about, and each one costs it a full resync.
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-health:
		t.Fatalf("a second healthy listing republished health: %+v", h)
	case <-time.After(time.Second):
	}

	// And the user's own case: the source dies under a live stream.
	darken(t, root)
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}
	awaitHealth(t, health, false, "the source went dark under a live stream")
}
