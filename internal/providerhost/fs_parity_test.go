package providerhost_test

// The v2 fs stack (fs provider + adapter + layout engine) through a full
// server: placement and framing persist, sweeps remove only the dead,
// the read-through cache answers when the source goes dark, and a
// retired id never returns. (Until the 2026-08 cutover these behaviors
// were pinned by crawling this stack against the legacy fs plugin; the
// legacy twin is gone, so the assertions are direct now.)

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/providerhost"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
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

func providerNode(t *testing.T, root string) (*rpc.Client, *fsprovider.Provider) {
	t.Helper()
	return providerNodeAt(t, root, filepath.Join(t.TempDir(), "mem.db"))
}

// providerNodeAt builds the v2 stack over an EXISTING memory DB path —
// how the conversion parity test serves a converted file.
func providerNodeAt(t *testing.T, root, memPath string) (*rpc.Client, *fsprovider.Provider) {
	t.Helper()
	mem, err := layout.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	// A plain-remove host: nil would mean the PRODUCTION trash — test
	// deletions must never land in the real freedesktop Trash (they did,
	// until 2026-08-23).
	prov := fsprovider.New(root, osRemoveHost{})
	cp, cpCloser, err := compose.ServeProviderInProcess(prov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	adapter := providerhost.New(cp, mem)
	client, closer, err := plugin.ServeInProcess(adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", client, nil)
	srv := server.New(reg, server.Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), prov
}

func TestProviderServesRememberedListingWhenSourceDark(t *testing.T) {
	// The v2 read-through cache (tenet 6): after one good read, a source
	// that stops answering serves the remembered listing, stamped stale,
	// and retires nothing.
	root := seedTree(t)
	v2, prov := providerNode(t, root)
	ctx := context.Background()
	pl, err := v2.ListPlugins(ctx)
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
	// The source goes dark (EACCES, an unmounted share): every read
	// fails transiently.
	prov.SetReadDir(func(string) ([]fssource.Entry, error) {
		return nil, os.ErrPermission
	})
	after, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("dark source surfaced as an error instead of the remembered answer: %v", err)
	}
	if !after.Grid.Stale {
		t.Fatal("remembered answer not stamped stale")
	}
	if len(after.Tiles) != len(before.Tiles) {
		t.Fatalf("dark source changed the tile set: %d != %d", len(after.Tiles), len(before.Tiles))
	}
	for i := range before.Tiles {
		if after.Tiles[i].ID != before.Tiles[i].ID || after.Tiles[i].X != before.Tiles[i].X {
			t.Fatalf("remembered tile drifted: %+v != %+v", after.Tiles[i], before.Tiles[i])
		}
	}
	// The source returns; the stale stamp clears.
	prov.SetReadDir(nil)
	healed, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Grid.Stale {
		t.Fatal("healed source still stamped stale")
	}
}

func TestDeleteRetiresOnTheWire(t *testing.T) {
	// The delete gesture through the full v2 stack: the source is
	// trashed, the row retires — Probe answers GONE, reads answer
	// NotFound, a second delete is a no-op, and a recreated file is a
	// NEW thing with a fresh id (the identity rule at the wire seam).
	root := seedTree(t)
	v2, _ := providerNode(t, root)
	ctx := context.Background()
	pl, err := v2.ListPlugins(ctx)
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
	if err := v2.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: bin.ID, Version: bin.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); !os.IsNotExist(err) {
		t.Fatalf("source file not deleted: %v", err)
	}
	if _, err := v2.GetTile(ctx, bin.ID); err == nil {
		t.Fatal("a retired tile still reads")
	}
	if err := v2.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: bin.ID, Version: bin.Version}); err != nil {
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
}

// TestFSProviderPlacementAndFramingPersist: the user drags notes.md and
// frames the sub well; a later read serves both back verbatim, and a
// file that arrives afterwards lands in an empty cell, never on top of
// the placed tile ("things stay as you left them").
func TestFSProviderPlacementAndFramingPersist(t *testing.T) {
	root := seedTree(t)
	v2, _ := providerNode(t, root)
	ctx := context.Background()
	pl, err := v2.ListPlugins(ctx)
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
		TileID: notes.ID, Version: notes.Version, GridID: rootGrid, X: 6, Y: 2, W: 2, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.SetWellView(ctx, &rpc.SetWellViewRequest{
		TileID: sub.ID, Version: sub.Version, ViewX: 2, ViewY: -1, ViewZoom: 1.4,
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
	if got.ID != notes.ID || got.X != 6 || got.Y != 2 || got.W != 2 || got.H != 1 {
		t.Fatalf("placement did not persist: %+v", got)
	}
	if sub2.ViewX != 2 || sub2.ViewY != -1 || sub2.ViewZoom != 1.4 {
		t.Fatalf("framing did not persist: %+v", sub2)
	}
	if late.X >= 6 && late.X < 8 && late.Y == 2 {
		t.Fatalf("a new file landed on the placed tile: %+v", late)
	}
}

// TestFSProviderSweepRemovesOnlyTheDead: a file deleted on disk is swept
// on the next read; every surviving tile keeps its id and placement.
func TestFSProviderSweepRemovesOnlyTheDead(t *testing.T) {
	root := seedTree(t)
	v2, _ := providerNode(t, root)
	ctx := context.Background()
	pl, err := v2.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	before, err := v2.GetGrid(ctx, rootGrid)
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
		if tile.AltText == "data.bin" {
			continue
		}
		s, ok := survivors[tile.AltText]
		if !ok || s.ID != tile.ID || s.X != tile.X || s.Y != tile.Y {
			t.Fatalf("survivor %s drifted: %+v != %+v", tile.AltText, s, tile)
		}
	}
}

// osRemoveHost unlinks outright — the test stand-in for the trash.
type osRemoveHost struct{}

func (osRemoveHost) Remove(p string) error    { return os.Remove(p) }
func (osRemoveHost) RemoveAll(p string) error { return os.RemoveAll(p) }
