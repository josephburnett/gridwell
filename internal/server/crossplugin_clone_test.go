package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/internal/plugin/fs"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// Cross-plugin clone semantics (CLAUDE.md "Identity and clone semantics"):
// right-dragging a tile across a plugin boundary must NOT copy the subtree —
// a well becomes a LINK in the destination (an exit well pointing back at the
// source plugin's grid), and a leaf (text/url) copies its bytes into the
// destination plugin. This was documented but unimplemented: CloneTile
// forwarded the foreign DestGridId to the source plugin, whose store failed
// with "invalid dest_grid_id".
//
// Why was this not caught? Every clone test cloned within one plugin; the
// cross-plugin gesture had no test at the routing seam where it actually
// branches.

// twoPluginServer stands up a server with two localdb plugins and returns the
// client plus each plugin's uuid and qualified root grid id.
func twoPluginServer(t *testing.T) (cl *rpc.Client, uuidA, rootA, uuidB, rootB string) {
	t.Helper()
	ctx := context.Background()
	reg := plugin.NewRegistry()

	stA, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stA.Close() })
	uuidA, rootA = registerPrimaryLocaldb(t, reg, stA)

	stB, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stB.Close() })
	uuidB, err = stB.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientB, closerB, err := plugin.ServeInProcess(localdb.New(stB, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closerB)
	reg.Register(uuidB, "localdb", clientB, nil)
	bareRootB, err := stB.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootB = uuidB + "/" + bareRootB

	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), uuidA, rootA, uuidB, rootB
}

func TestCloneWellAcrossPluginsIsALink(t *testing.T) {
	cl, uuidA, rootA, uuidB, rootB := twoPluginServer(t)
	ctx := context.Background()

	// A named well with content inside it, in plugin A.
	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Label: "recipes",
	})
	if err != nil {
		t.Fatalf("CreateWell: %v", err)
	}
	inner, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{WellIDs: []string{well.ID}}, GridID: well.ChildGridID,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("# soup"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	// The source well has a framing the user set — the preview the clone
	// gesture is copying. It must ride along: descending the link should
	// land exactly where descending the source would.
	framed, err := cl.SetWellView(ctx, &rpc.SetWellViewRequest{
		Path: rpc.Path{}, TileID: well.ID, Version: well.Version,
		ViewX: 7, ViewY: -2, ViewZoom: 1.75,
	})
	if err != nil {
		t.Fatalf("SetWellView: %v", err)
	}

	// Right-drag the well into plugin B: the destination gains a LINK.
	link, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, Version: framed.Version,
		DestGridID: rootB, X: 3, Y: 3,
	})
	if err != nil {
		t.Fatalf("cross-plugin CloneTile: %v", err)
	}
	if link.ViewX != 7 || link.ViewY != -2 || link.ViewZoom != 1.75 {
		t.Errorf("link framing = (%v, %v, %v), want the source's (7, -2, 1.75) — a clone must not reset the viewport",
			link.ViewX, link.ViewY, link.ViewZoom)
	}
	if u, _, _ := rpc.SplitID(link.ID); u != uuidB {
		t.Errorf("link lives in %q, want destination plugin %q", u, uuidB)
	}
	if link.ChildGridID != well.ChildGridID {
		t.Errorf("link child = %q, want the SOURCE well's grid %q (shared, not copied)", link.ChildGridID, well.ChildGridID)
	}
	if !link.Reference {
		t.Error("cross-plugin link must be marked Reference (dashed border, unlink-only delete)")
	}
	if link.AltText != "recipes" {
		t.Errorf("link label = %q, want the source's name", link.AltText)
	}

	// The grid is SHARED: reading the link's child sees the source's content.
	g, err := cl.GetGrid(ctx, link.ChildGridID)
	if err != nil {
		t.Fatalf("GetGrid through link: %v", err)
	}
	if len(g.Tiles) != 1 || g.Tiles[0].ID != inner.ID {
		t.Errorf("linked grid = %+v, want the source's tile %s", g.Tiles, inner.ID)
	}

	// Deleting the link only unlinks — the source well and its content survive.
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: link.ID, Version: link.Version}); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if _, err := cl.GetTile(ctx, well.ID); err != nil {
		t.Errorf("deleting the link destroyed the source well: %v", err)
	}
	if _, _, err := cl.GetTileContent(ctx, inner.ID); err != nil {
		t.Errorf("deleting the link destroyed the source's content: %v", err)
	}
	_ = uuidA

	// An UNNAMED well links too: the link's alt is simply empty. (Regression:
	// the exit-well insert turned "" into SQL NULL against a NOT NULL column,
	// so only named wells could cross a plugin boundary.)
	plain, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 4, Y: 4, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("CreateWell (unnamed): %v", err)
	}
	if _, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: plain.ID, Version: plain.Version, DestGridID: rootB, X: 5, Y: 5,
	}); err != nil {
		t.Errorf("cross-plugin clone of an UNNAMED well failed: %v", err)
	}
}

func TestCloneLeafAcrossPluginsCopiesBytes(t *testing.T) {
	cl, _, rootA, uuidB, rootB := twoPluginServer(t)
	ctx := context.Background()

	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# portable"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	copyT, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: txt.ID, Version: txt.Version,
		DestGridID: rootB, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-plugin text clone: %v", err)
	}
	if u, _, _ := rpc.SplitID(copyT.ID); u != uuidB {
		t.Errorf("copy lives in %q, want %q", u, uuidB)
	}
	body, _, err := cl.GetTileContent(ctx, copyT.ID)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != "# portable" {
		t.Errorf("copied bytes = %q, want %q", body, "# portable")
	}

	// The copies are independent: editing the copy leaves the source alone.
	if _, err := cl.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: copyT.ID, Version: copyT.Version, Data: []byte("# changed"),
	}); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, err := cl.GetTileContent(ctx, txt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != "# portable" {
		t.Errorf("editing the copy changed the source: %q", orig)
	}
}

func TestCloneURLAcrossPluginsCopiesAddress(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()

	u, err := cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com/",
	})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	cp, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: u.ID, Version: u.Version, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-plugin url clone: %v", err)
	}
	if cp.URLString != "https://example.com/" {
		t.Errorf("copied url = %q", cp.URLString)
	}
}

func TestCloneAcrossPluginsChecksVersion(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()

	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: txt.ID, Version: txt.Version + 7, DestGridID: rootB, X: 0, Y: 0,
	})
	if err == nil {
		t.Fatal("stale-version cross-plugin clone succeeded, want conflict")
	}
}

// TestClonePaneAcrossPluginsCopiesLayout: a workspace crosses a plugin
// boundary as a BYTE COPY of its layout blob (like text) — and because the
// layout's ids are owner-frame-relative by the codec's rule, the copy's panes
// keep naming the ORIGINAL places: arrangement copies, referenced content is
// shared. The copies then diverge independently (content addressing).
func TestClonePaneAcrossPluginsCopiesLayout(t *testing.T) {
	cl, _, rootA, uuidB, rootB := twoPluginServer(t)
	ctx := context.Background()

	layout := []byte(`{"v":1,"root":{"pane":{"id":"p1","anchor":"someplugin/1","zoom":1}},"focus":"p1"}`)
	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{
		GridID: rootA, X: 0, Y: 0, W: 2, H: 2, Label: "ws", Data: layout,
	})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	cp, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: pt.ID, Version: pt.Version, DestGridID: rootB, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-plugin pane clone: %v", err)
	}
	if u, _, _ := rpc.SplitID(cp.ID); u != uuidB {
		t.Errorf("copy lives in %q, want %q", u, uuidB)
	}
	if cp.Kind != rpc.KindPane || cp.AltText != "ws" {
		t.Errorf("copy shape: %+v", cp)
	}
	body, _, err := cl.GetTileContent(ctx, cp.ID)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != string(layout) {
		t.Errorf("copied layout = %q, want the source bytes (references preserved verbatim)", body)
	}

	// Independence: rearranging the copy leaves the source's layout alone.
	if _, err := cl.SetPaneLayout(ctx, cp.ID, cp.Version,
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, err := cl.GetTileContent(ctx, pt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(layout) {
		t.Errorf("editing the copy changed the source layout: %q", orig)
	}
}

// TestCloneDirWellFromFsPluginIsALink (issue #171): right-dragging a
// directory from an fs grid into a localdb grid failed "GetTile is not
// implemented" — cloneAcrossPlugins' FIRST call is src.GetTile, which the fs
// (and proc) plugins never implemented. This crosses the real seam: server
// routing → in-process fs plugin → link created in the localdb destination.
func TestCloneDirWellFromFsPluginIsALink(t *testing.T) {
	ctx := context.Background()
	reg := plugin.NewRegistry()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, dstRoot := registerPrimaryLocaldb(t, reg, st)

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fsP, err := fsplugin.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("fs open: %v", err)
	}
	t.Cleanup(func() { _ = fsP.Close() })
	fsP.SetRoot(dir)
	info, err := fsP.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("fs info: %v", err)
	}
	fsClient, fsCloser, err := plugin.ServeInProcess(fsP)
	if err != nil {
		t.Fatalf("fs serve: %v", err)
	}
	t.Cleanup(fsCloser)
	const fsUUID = "fs-src-uuid"
	reg.Register(fsUUID, "fs", fsClient, nil)

	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// GetGrid materializes the directory's tiles (the user is dragging a
	// visible tile from the rendered fs grid).
	g, err := cl.GetGrid(ctx, fsUUID+"/"+info.RootGridId)
	if err != nil {
		t.Fatalf("GetGrid (fs root): %v", err)
	}
	var sub *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].AltText == "sub" {
			sub = &g.Tiles[i]
		}
	}
	if sub == nil {
		t.Fatalf("sub dir tile missing: %+v", g.Tiles)
	}

	link, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: sub.ID, Version: sub.Version, DestGridID: dstRoot, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-plugin CloneTile from fs: %v", err)
	}
	if link.ChildGridID != sub.ChildGridID {
		t.Errorf("link child = %q, want the fs dir's grid %q (shared, not copied)",
			link.ChildGridID, sub.ChildGridID)
	}
	if !link.Reference {
		t.Error("cross-plugin link must be marked Reference (dashed border)")
	}
	if link.AltText != "sub" {
		t.Errorf("link label = %q, want the directory's name", link.AltText)
	}
}

// TestClonePaneAcrossPluginsNeverArranged: a pane tile with no layout blob
// clones as an empty workspace (no Data round trip to fail on).
func TestClonePaneAcrossPluginsNeverArranged(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()

	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{
		GridID: rootA, X: 0, Y: 0, W: 2, H: 2, Label: "empty",
	})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	cp, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: pt.ID, Version: pt.Version, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-plugin clone of never-arranged pane: %v", err)
	}
	if cp.BlobID != 0 {
		t.Errorf("never-arranged copy grew a blob: %+v", cp)
	}
}
