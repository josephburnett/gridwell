package providerhost_test

// The stage-4 gate (docs/v2-design.md §7): the SAME directory tree served
// by the legacy fs plugin and by the v2 stack (fs provider + adapter +
// layout engine) must be indistinguishable on the wire — same ids, same
// placement, same framing, same content, no policy blind spots. Both
// stacks are crawled through full servers by the parity oracle; the two
// registries share one uuid so the comparison is literal.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/parity"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/providerhost"
	"github.com/josephburnett/gridwell/internal/server"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
)

const fsUUID = "fsuuidx" // shared by both stacks so ids compare literally

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

func legacyNode(t *testing.T, root string) *rpc.Client {
	t.Helper()
	return legacyNodeAt(t, root, filepath.Join(t.TempDir(), "fs.db"))
}

// legacyNodeAt pins the legacy DB path — the conversion parity test
// converts the very file the legacy node keeps serving.
func legacyNodeAt(t *testing.T, root, dbPath string) *rpc.Client {
	t.Helper()
	// Stamp the pluginmeta identity exactly as `gridwell init` does — the
	// converter verifies it, and every real legacy DB carries it.
	if err := pluginmeta.Create(dbPath, fsUUID, "fs"); err != nil {
		t.Fatal(err)
	}
	p, err := fsplugin.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetRoot(root)
	client, closer, err := plugin.ServeInProcess(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", client, nil)
	srv := server.New(reg, server.Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
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
	prov := fsprovider.New(root, nil)
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

func mustParity(t *testing.T, legacy, v2 *rpc.Client) {
	t.Helper()
	sa, sb, err := parity.CrawlPair(context.Background(), legacy, v2, parity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// menu_entries is the ONE named divergence: legacy fs declares the
	// #258 search tool; the adapter strips creation entries until the
	// userdocs store exists (#271) — never advertise a door you cannot
	// open. Everything else compares with no blind spots.
	pol := parity.Policy{IgnoreFields: map[string]bool{"menu_entries": true}}
	if diffs := parity.Diff(sa, sb, pol); len(diffs) != 0 {
		t.Fatalf("legacy and v2 stacks differ (%d):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
}

func TestFSProviderMatchesLegacyOnTheWire(t *testing.T) {
	root := seedTree(t)
	legacy := legacyNode(t, root)
	v2, _ := providerNode(t, root)
	mustParity(t, legacy, v2)
}

func TestFSProviderPlacementParity(t *testing.T) {
	root := seedTree(t)
	legacy := legacyNode(t, root)
	v2, _ := providerNode(t, root)
	ctx := context.Background()

	// Materialize both, find notes.md on each — the ids must already
	// agree (both stacks mint in listing order from the same tree).
	pl, err := legacy.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := legacy.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	// Materialize the v2 side too (both stacks mint rows on first read,
	// exactly like the legacy reconcile). ListPlugins first: Info mints
	// the root context, as the spawn-time handshake does in production.
	if _, err := v2.ListPlugins(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}
	var notes rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "notes.md" {
			notes = tile
		}
	}
	if notes.ID == "" {
		t.Fatal("notes.md not found")
	}

	// The user drags notes.md — on BOTH stacks (same logical action).
	for _, cl := range []*rpc.Client{legacy, v2} {
		if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
			TileID: notes.ID, Version: notes.Version, GridID: rootGrid, X: 6, Y: 2, W: 2, H: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// And frames the sub directory well.
	var sub rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "sub" {
			sub = tile
		}
	}
	for _, cl := range []*rpc.Client{legacy, v2} {
		if _, err := cl.SetWellView(ctx, &rpc.SetWellViewRequest{
			TileID: sub.ID, Version: sub.Version, ViewX: 2, ViewY: -1, ViewZoom: 1.4,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustParity(t, legacy, v2)

	// New files arrive; both stacks must lay them into the same gaps.
	if err := os.WriteFile(filepath.Join(root, "later.md"), []byte("late"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustParity(t, legacy, v2)
}

func TestFSProviderDeleteSweepParity(t *testing.T) {
	root := seedTree(t)
	legacy := legacyNode(t, root)
	v2, _ := providerNode(t, root)
	// Materialize, then remove a file from disk; the next crawl sweeps
	// it on both sides identically.
	mustParity(t, legacy, v2)
	if err := os.Remove(filepath.Join(root, "data.bin")); err != nil {
		t.Fatal(err)
	}
	mustParity(t, legacy, v2)
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
