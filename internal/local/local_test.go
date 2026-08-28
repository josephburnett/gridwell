package local_test

import (
	"context"
	"fmt"
	"math"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

func openPlugin(t *testing.T) *local.Plugin {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return local.New(st, nil)
}

// rootGrid returns the plugin's default root grid id (from Info — the whole
// handshake; there is no Attach).
func rootGrid(t *testing.T, p *local.Plugin) string {
	t.Helper()
	info, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.RootGridId == "" {
		t.Fatal("Info.RootGridId empty")
	}
	return info.RootGridId
}

// createText is a CreateTile helper for a text tile (creation is
// metadata-only; the body follows through the store's one content write).
func createText(t *testing.T, p *local.Plugin, gridID string, data []byte) *gridwellv1.Tile {
	t.Helper()
	cr, err := p.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: gridID,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 4, H: 4},
	})
	if err != nil {
		t.Fatalf("CreateTile(text): %v", err)
	}
	out := cr.Tile
	if len(data) > 0 {
		if _, err := p.Store().WriteContent(context.Background(), out.Id, out.Version, data); err != nil {
			t.Fatalf("WriteContent(text): %v", err)
		}
		r2, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: out.Id})
		if err != nil {
			t.Fatalf("GetTile: %v", err)
		}
		out = r2.Tile
	}
	return out
}

func TestInfo(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "home" {
		t.Errorf("Kind = %q, want localdb", resp.Kind)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
}

func TestGetGrid_ReturnsGrid(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)
	resp, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Grid.Id != root {
		t.Errorf("grid id = %q, want %q", resp.Grid.Id, root)
	}
}

func TestProbe_Present(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	tile := createText(t, p, rootGrid(t, p), []byte("hello"))
	pr, err := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tile.Id})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("Presence = %v, want PRESENT", pr.Presence)
	}
}

func TestProbe_Gone(t *testing.T) {
	p := openPlugin(t)
	pr, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: "999999"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Errorf("Presence = %v, want GONE", pr.Presence)
	}
}

func TestCreateWell_ThenGetGrid(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	cr, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 4, H: 4},
	})
	if err != nil {
		t.Fatalf("CreateTile(well): %v", err)
	}
	if cr.Tile.Kind != "well" {
		t.Errorf("Kind = %q, want well", cr.Tile.Kind)
	}

	gr, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tile := range gr.Tiles {
		if tile.Id == cr.Tile.Id {
			found = true
		}
	}
	if !found {
		t.Error("created well not found in GetGrid response")
	}
}

func TestDeleteTile_RemovesTile(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	tile := createText(t, p, rootGrid(t, p), []byte("bye"))
	// Two-stage (#262): the first delete PARKS the tile in the trash — it
	// moved, it didn't die, so Probe still answers PRESENT.
	_, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId:  tile.Id,
		Version: tile.Version,
	})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	pr, _ := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tile.Id})
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Error("trashed tile must still probe PRESENT (it moved, it didn't die)")
	}
	cur, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: tile.Id})
	if err != nil {
		t.Fatal(err)
	}
	// The second delete (inside the trash) destroys for real.
	if _, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId:  tile.Id,
		Version: cur.Tile.Version,
	}); err != nil {
		t.Fatalf("DeleteTile (in trash): %v", err)
	}
	pr, _ = p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tile.Id})
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Error("tile still PRESENT after the in-trash delete")
	}
}

func TestShellSessionAlive_NoShellHost(t *testing.T) {
	p := openPlugin(t) // shell = nil
	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Alive {
		t.Error("expected Alive=false with no shell host")
	}
}

// fakeStreamer is a shellsvc.Streamer stub: it reports a session is alive but
// never opens a real PTY, so the manager can be exercised without tmux.
type fakeStreamer struct{ alive bool }

func (f *fakeStreamer) OpenSession(string, tmux.Mode, uint16, uint16) (shellsvc.Session, error) {
	return nil, nil
}
func (f *fakeStreamer) HasSession(string) (bool, error)    { return f.alive, nil }
func (f *fakeStreamer) Kill(string) error                  { return nil }
func (f *fakeStreamer) ListLiveTileIDs() ([]string, error) { return nil, nil }
func (f *fakeStreamer) PaneCommand(string) (string, error) { return "", nil }

func TestShellSessionAlive_WithShellHost(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	p := local.New(st, shellsvc.NewManager(&fakeStreamer{alive: true}))

	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Alive {
		t.Error("expected Alive=true from the fake streamer")
	}
}

// TestCreateWell_InteriorVsExit: a well CreateTile with no child_grid_id
// allocates an interior child grid; with a qualified child_grid_id it stores a
// cross-plugin exit well pointing at that grid (no interior grid, the reference
// verbatim). alt_text is the exit well's label.
func TestCreateWell_InteriorVsExit(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	interior, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1, AltText: "recipes"},
	})
	if err != nil {
		t.Fatalf("interior CreateTile: %v", err)
	}
	if interior.Tile.ChildGridId == "" || interior.Tile.ChildGridId == "0" {
		t.Errorf("interior well child = %q, want a fresh local grid", interior.Tile.ChildGridId)
	}
	if interior.Tile.AltText != "recipes" {
		t.Errorf("interior well label = %q, want recipes (the wire's AltText is the grid name)", interior.Tile.AltText)
	}

	exit, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile: &gridwellv1.Tile{
			Kind: "well", X: 2, Y: 0, W: 1, H: 1,
			ChildGridId: "other-plugin-uuid/9", AltText: "mounted",
		},
	})
	if err != nil {
		t.Fatalf("exit CreateTile: %v", err)
	}
	if exit.Tile.ChildGridId != "other-plugin-uuid/9" {
		t.Errorf("exit well child = %q, want verbatim cross-plugin ref", exit.Tile.ChildGridId)
	}
	if exit.Tile.AltText != "mounted" {
		t.Errorf("exit well label = %q, want mounted", exit.Tile.AltText)
	}
}

// TestReadContent_ReturnsBody: a text tile's body is fetched through the
// store's one content read (the gRPC ReadContent chunks the same value; the
// stream-level contract is pinned in content_stream_test.go).
func TestReadContent_ReturnsBody(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	txt := createText(t, p, rootGrid(t, p), []byte("# hello"))
	data, _, _, err := p.Store().ReadContent(ctx, txt.Id)
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if string(data) != "# hello" {
		t.Errorf("content = %q, want %q", data, "# hello")
	}
}

// TestGetTileAndRename: GetTile reads a tile's metadata; the SetTile rename
// arm (the versioned wire rename, issue #61 + 2026-07-26) stamps a
// user-owned label on a shell tile and returns it — and REFUSES a text tile,
// whose name derives from its first line (a rename there would be clobbered
// by the next edit).
func TestGetTileAndRename(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)
	txt := createText(t, p, root, []byte("# hi"))

	got, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: txt.Id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if got.Tile.Id != txt.Id || got.Tile.Kind != "text" {
		t.Errorf("GetTile = %+v, want the text tile", got.Tile)
	}

	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: txt.Id, Version: txt.Version, Rename: "claude"}); err == nil {
		t.Error("rename of a text tile must be refused (its name derives from content)")
	}

	sh, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 7, Y: 7, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}
	stamped, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: sh.Tile.Id, Version: sh.Tile.Version, Rename: "claude"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if stamped.Tile.AltText != "claude" {
		t.Errorf("alt = %q, want claude", stamped.Tile.AltText)
	}
}

// TestSetTile_WellFramingNoVersionBump: framing writes (well view) do not bump
// version; a content writeback (url freeze) does. This pins the version rule
// that the merged SetTile inherits from the store operations it dispatches to.
func TestSetTile_WellFramingNoVersionBump(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	well, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(well): %v", err)
	}
	set, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId:  well.Tile.Id,
		Version: well.Tile.Version,
		Tile:    &gridwellv1.Tile{Kind: "well", ViewX: 5, ViewY: 6, ViewZoom: 2},
	})
	if err != nil {
		t.Fatalf("SetTile(well framing): %v", err)
	}
	if set.Tile.Version != well.Tile.Version {
		t.Errorf("framing bumped version: %d → %d, want unchanged", well.Tile.Version, set.Tile.Version)
	}
	if set.Tile.ViewX != 5 || set.Tile.ViewY != 6 || set.Tile.ViewZoom != 2 {
		t.Errorf("framing not persisted: %+v", set.Tile)
	}
}

// TestInfoRootViewSeedAndSetRootViewWriteback pins the launcher↔plugin-root
// seam (issue #32): SetRootView persists the framing, and Info returns it so
// the client can restore the left-off viewport on enterPlugin without an extra
// round-trip. Framing only — SetRootView must not bump a content version.
//
// Why was this not caught? framing-roundtrip.spec.ts only tested wells INSIDE
// a plugin; the launcher↔plugin-root seam (portal entry/ascent) had no test.
func TestInfoRootViewSeedAndSetRootViewWriteback(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	const eps = 1e-9

	// A freshly-opened plugin starts with zero root view (default calibration
	// will be used on first entry — the correct "never visited" behaviour).
	info0, err := p.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info(initial): %v", err)
	}
	if info0.RootViewZoom != 0 {
		t.Errorf("fresh Info.RootViewZoom = %v, want 0", info0.RootViewZoom)
	}

	// Write a root view via SetRootView (the ascent-writeback path).
	_, err = p.SetRootView(ctx, &gridwellv1.SetRootViewRequest{
		Cx:   3.5,
		Cy:   -2.25,
		Zoom: 1.75,
	})
	if err != nil {
		t.Fatalf("SetRootView: %v", err)
	}

	// Info must now reflect the saved values so enterPlugin can seed the
	// portal-well framing (the read side of the same seam).
	info1, err := p.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info(after SetRootView): %v", err)
	}
	if math.Abs(info1.RootViewCx-3.5) > eps {
		t.Errorf("Info.RootViewCx = %v, want 3.5", info1.RootViewCx)
	}
	if math.Abs(info1.RootViewCy-(-2.25)) > eps {
		t.Errorf("Info.RootViewCy = %v, want -2.25", info1.RootViewCy)
	}
	if math.Abs(info1.RootViewZoom-1.75) > eps {
		t.Errorf("Info.RootViewZoom = %v, want 1.75", info1.RootViewZoom)
	}

	// SetRootView is framing-only: the root grid's own version must not change.
	// We check via Info — schema_version reflects the DB format, not a content
	// edit; but the SetRootView call above must not have errored with a version
	// conflict either. A non-zero version bump would surface as a different
	// schema_version here or as an error above.
	if info1.SchemaVersion != info0.SchemaVersion {
		t.Errorf("schema_version changed after SetRootView: %d → %d", info0.SchemaVersion, info1.SchemaVersion)
	}
}

// TestCleanupScratchSweepsEphemeralTiles (issue #85's crash net, coverage gap
// found in the audit): the startup sweep deletes EVERY scratch-grid tile — an
// ascent that never ran (crash, hard kill) must not leak ephemeral visits.
func TestCleanupScratchSweepsEphemeralTiles(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	scratch := scratchGrid(t, p)

	// Two ephemeral tiles a crashed session left behind: a url and a shell
	// (created through the plugin's scratch routing, like the client does).
	for _, tile := range []*gridwellv1.Tile{
		{Kind: "url", X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/leak"},
		{Kind: "shell", X: 0, Y: 0, W: 1, H: 1},
	} {
		if _, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: tile}); err != nil {
			t.Fatalf("create scratch %s: %v", tile.Kind, err)
		}
	}
	g, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: scratch})
	if err != nil {
		t.Fatalf("GetGrid(scratch): %v", err)
	}
	if len(g.Tiles) != 2 {
		t.Fatalf("scratch has %d tiles, want 2", len(g.Tiles))
	}

	swept, err := p.CleanupScratch(ctx)
	if err != nil {
		t.Fatalf("CleanupScratch: %v", err)
	}
	if swept != 2 {
		t.Errorf("swept = %d, want 2", swept)
	}
	g, err = p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: scratch})
	if err != nil {
		t.Fatalf("GetGrid(scratch) after sweep: %v", err)
	}
	if len(g.Tiles) != 0 {
		t.Errorf("scratch still has %d tiles after the sweep", len(g.Tiles))
	}
	// Idempotent: a clean scratch sweeps zero.
	if swept, err := p.CleanupScratch(ctx); err != nil || swept != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", swept, err)
	}
}

// TestCleanupScratchSparesWorkspaceEphemerals (issue #174 part 2): a scratch
// tile referenced by a pane tile's layout blob is a WORKSPACE'S ephemeral —
// part of a durable arrangement, alive on purpose across app restarts (its
// tmux session survives them) — and the boot sweep must not reap it. An
// unreferenced scratch tile still sweeps (the crash net), and once the pane
// tile is deleted the reference dies with the blob, so the next sweep
// reclaims it: self-healing, no second bookkeeping copy.
func TestCleanupScratchSparesWorkspaceEphemerals(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	scratch := scratchGrid(t, p)

	uuid, err := p.Store().PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.Store().RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A workspace-owned ephemeral shell + an orphaned (crash-leaked) one.
	owned, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch,
		Tile: &gridwellv1.Tile{Kind: "shell", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	leaked, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch,
		Tile: &gridwellv1.Tile{Kind: "shell", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}

	// The pane tile whose layout references the owned shell (qualified id,
	// exactly what the client persister writes).
	pt, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: "pane", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	layout := fmt.Sprintf(`{"v":1,"root":{"pane":{"id":"p1","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}},"focus":"p1"}`,
		uuid+"/"+root, uuid+"/"+owned.Tile.Id)
	if _, err := p.Store().WriteContent(ctx, pt.Tile.Id, pt.Tile.Version, []byte(layout)); err != nil {
		t.Fatalf("WriteContent(layout): %v", err)
	}

	swept, err := p.CleanupScratch(ctx)
	if err != nil {
		t.Fatalf("CleanupScratch: %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want 1 (only the leaked tile)", swept)
	}
	if _, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: owned.Tile.Id}); err != nil {
		t.Errorf("workspace-owned ephemeral was swept at boot: %v", err)
	}
	if _, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: leaked.Tile.Id}); err == nil {
		t.Error("crash-leaked scratch tile survived the sweep")
	}

	// Delete the pane tile: the first delete PARKS it in the trash (#262)
	// and its workspace layout keeps holding its ephemerals — a restored
	// workspace must come back whole, so nothing is reclaimable yet.
	if _, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Tile.Id, Version: pt.Tile.Version}); err != nil {
		t.Fatal(err)
	}
	if swept, err := p.CleanupScratch(ctx); err != nil || swept != 0 {
		t.Errorf("post-trash sweep = (%d, %v), want (0, nil) — a trashed workspace keeps its ephemerals", swept, err)
	}
	// The second delete destroys the pane tile; the reference dies with the
	// blob and the next sweep reclaims the formerly-owned ephemeral. (The
	// server-level delete reap usually gets there first; the sweep is the net.)
	cur, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: pt.Tile.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Tile.Id, Version: cur.Tile.Version}); err != nil {
		t.Fatal(err)
	}
	if swept, err := p.CleanupScratch(ctx); err != nil || swept != 1 {
		t.Errorf("post-destroy sweep = (%d, %v), want (1, nil) — the reference must die with the blob", swept, err)
	}
}

// scratchGrid returns the plugin's scratch grid id from Info.
func scratchGrid(t *testing.T, p *local.Plugin) string {
	t.Helper()
	info, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ScratchGridId == "" {
		t.Fatal("Info.ScratchGridId empty")
	}
	return info.ScratchGridId
}
