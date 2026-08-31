package pluginhost_test

// The fs stack — the REAL gridwell-plugin-fs binary, the adapter, and the
// store — through a full server: placement and framing persist, sweeps remove
// only the dead, the node's own rows answer when the source goes dark, and a
// retired id never returns.
//
// The plugin is spawned, never linked: it is another repository's module now,
// and the subprocess is the only door it has. So the source goes dark the way
// a real one does — an unreadable directory — instead of through an injected
// reader.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	return pluginNodeAt(t, root, filepath.Join(t.TempDir(), "mem.db"))
}

// pluginNodeAt builds the stack over an existing store path: how the
// conversion parity test serves a converted file. The plugin is the shipped
// binary, configured with root exactly as a server.yaml plugins: entry would.
func pluginNodeAt(t *testing.T, root, memPath string) *rpc.Client {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
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
	// the rows the user touched still read, stamped stale and retiring
	// nothing, while an entry nobody ever touched has no row to read from and
	// is simply absent until the source speaks again.
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	before, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Tiles) == 0 || before.Grid.Stale {
		t.Fatalf("bad first read: %+v", before.Grid)
	}
	// One durable touch: the user drags notes.md somewhere. That is what
	// mints a row, and the row is what survives the dark.
	var notes rpc.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			notes = tile
		}
	}
	if notes.ID == "" {
		t.Fatal("no notes.md tile")
	}
	placed, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: notes.ID, GridID: rootGrid, X: 7, Y: 3, W: 1, H: 1})
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
	if !after.Grid.Stale {
		t.Fatal("remembered answer not stamped stale")
	}
	if len(after.Tiles) != 1 {
		t.Fatalf("dark source answered %d tiles, want only the touched one: %+v", len(after.Tiles), after.Tiles)
	}
	if got := after.Tiles[0]; got.ID != placed.ID || got.X != 7 || got.Y != 3 || got.AltText != "notes.md" {
		t.Fatalf("the touched row drifted in the dark: %+v", got)
	}
	// The source returns; the stale stamp clears and every entry is back.
	lighten()
	healed, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Grid.Stale {
		t.Fatal("healed source still stamped stale")
	}
	if len(healed.Tiles) != len(before.Tiles) {
		t.Fatalf("healed listing = %d tiles, want the original %d", len(healed.Tiles), len(before.Tiles))
	}
}

func TestDeleteRetiresOnTheWire(t *testing.T) {
	// The delete gesture through the full stack: the source is trashed and the
	// row retires, so Probe answers GONE, reads answer NotFound, a second
	// delete is a no-op, and a recreated file is a new thing with a fresh
	// id.
	root := seedTree(t)
	v2 := pluginNode(t, root)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var bin rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "data.bin" {
			bin = tile
		}
	}
	// Arrange it first: identity is a property of a ROW, so the "recreation
	// mints fresh" half of the contract needs one. An entry nobody ever
	// touched has no id to burn — deleting it is the plugin's verdict and
	// nothing else, which the last stanza pins.
	minted, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: bin.ID, GridID: rootGrid, X: 4, Y: 4, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	bin = *minted
	if err := v2.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: bin.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); !os.IsNotExist(err) {
		t.Fatalf("source file not deleted: %v", err)
	}
	if _, err := v2.GetTile(ctx, bin.ID); err == nil {
		t.Fatal("a retired tile still reads")
	}
	if err := v2.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: bin.ID}); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	// Recreation mints fresh identity.
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{9}, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText == "data.bin" && tile.ID == bin.ID {
			t.Fatal("a recreated file reused the retired id")
		}
	}
	// Deleting an UNTOUCHED entry involves no row at all: the plugin trashes
	// the file and the next listing simply does not name it.
	var doc rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "notes.md" {
			doc = tile
		}
	}
	if err := v2.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: doc.ID}); err != nil {
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
	rootGrid := pl.Plugins[0].RootGridID
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) rpc.Tile {
		t.Helper()
		for _, tile := range g.Tiles {
			if tile.AltText == name {
				return tile
			}
		}
		t.Fatalf("%s not found", name)
		return rpc.Tile{}
	}
	notes, sub := find("notes.md"), find("sub")
	if _, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: notes.ID, GridID: rootGrid, X: 6, Y: 2, W: 2, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: sub.ID, Framing: rpc.Framing{Cx: 2, Cy: -1, Zoom: 1.4},
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
	if held, err := v2.GetTile(ctx, notes.ID); err != nil || held.ID != got.ID {
		t.Fatalf("the pre-mint address stopped resolving: %+v (%v), want %s", held, err, got.ID)
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
	rootGrid := pl.Plugins[0].RootGridID
	before, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var arranged rpc.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "notes.md" {
			arranged = tile
		}
	}
	placed, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: arranged.ID, GridID: rootGrid, X: 6, Y: 6, W: 1, H: 1})
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
	survivors := map[string]rpc.Tile{}
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
		if !ok || s.ID != tile.ID {
			t.Fatalf("survivor %s lost its identity: %+v != %+v", tile.AltText, s, tile)
		}
	}
	if s := survivors["notes.md"]; s.ID != placed.ID || s.X != 6 || s.Y != 6 {
		t.Fatalf("the arranged survivor drifted: %+v", s)
	}
}
