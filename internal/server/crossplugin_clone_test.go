package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
)

// Cross-plugin gesture semantics (owner decision 2026-07-19): LEFT-drag
// across a plugin boundary creates a LINK (one copy of the content — an exit
// well for a grid, a leaf link via link_target_id for text/url/shell/pane),
// and RIGHT-drag creates a CLONE (a real copy: leaves copy bytes; a solid
// well deep-copies — deepcopy.go, with the offline degrade-to-links rule
// pinned in deepcopy_dark_test.go). The left-drag arrives at the server as a
// plain CreateTile carrying a qualified reference — the same request shape a
// + menu plugin-swatch drop uses — so these tests drive both faces through
// the real router seam. Framing and labels ride every cross-plugin link
// and copy, so a link looks like what it points at.

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
	clientB := local.New(stB, nil)
	reg.Register(uuidB, "home", clientB, nil)
	bareRootB, err := stB.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootB = uuidB + "/" + bareRootB

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), uuidA, rootA, uuidB, rootB
}

func TestLinkWellAcrossPlugins(t *testing.T) {
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
		GridID: well.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# soup"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	// The source well has a framing the user set — the preview the link
	// gesture carries along. Descending the link must land exactly where
	// descending the source would.
	framed, err := cl.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID:  well.ID,
		Framing: rpc.Framing{Cx: 7, Cy: -2, Zoom: 1.75},
	})
	if err != nil {
		t.Fatalf("SetWellView: %v", err)
	}

	// LEFT-drag the well into plugin B: the client commits a CreateWell
	// carrying the source's qualified child grid, label, framing, and
	// provenance — the destination gains a LINK; there is only one copy of
	// the grid.
	link, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootB, X: 3, Y: 3, W: well.W, H: well.H,
		ChildGridID: well.ChildGridID, Label: framed.AltText,
		Framing: rpc.Framing{Cx: framed.ViewCx, Cy: framed.ViewCy, Zoom: framed.ViewZoom},
	})
	if err != nil {
		t.Fatalf("cross-plugin link (CreateWell): %v", err)
	}
	if link.ViewCx != 7 || link.ViewCy != -2 || link.ViewZoom != 1.75 {
		t.Errorf("link framing = (%v, %v, %v), want the source's (7, -2, 1.75) — a link must not reset the viewport",
			link.ViewCx, link.ViewCy, link.ViewZoom)
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
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: link.ID}); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if _, err := cl.GetTile(ctx, well.ID); err != nil {
		t.Errorf("deleting the link destroyed the source well: %v", err)
	}
	if _, _, _, err := cl.ReadContent(ctx, inner.ID); err != nil {
		t.Errorf("deleting the link destroyed the source's content: %v", err)
	}
	_ = uuidA
}

// TestCloneWellAcrossPluginsDeepCopies (issue #200, completing the
// 2026-07-19 right-drag-copies decision): a solid well right-dragged across
// a plugin boundary DEEP-COPIES — the destination gains an independent
// subtree, byte-identical bodies, framing preserved, provenance carried,
// references inside copied as references — and editing the copy never
// touches the source (no structural sharing across the boundary).
func TestCloneWellAcrossPluginsDeepCopies(t *testing.T) {
	cl, _, rootA, uuidB, rootB := twoPluginServer(t)
	ctx := context.Background()

	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Label: "recipes",
	})
	if err != nil {
		t.Fatalf("CreateWell: %v", err)
	}
	// Contents: a text body, a NESTED well with its own text, and a leaf
	// LINK (which must copy as a reference, not as bytes).
	inner, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# soup"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: well.ChildGridID, X: 2, Y: 0, W: 1, H: 1, Label: "drafts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: nested.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# stock"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: well.ChildGridID, X: 4, Y: 0, W: 1, H: 1, Kind: rpc.KindText,
		LinkTargetID: inner.ID, Label: "soup-link",
	}); err != nil {
		t.Fatal(err)
	}
	// Framing on the well (preview = descent = ascent).
	if _, err := cl.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: well.ID, Framing: rpc.Framing{Cx: 7, Cy: 8, Zoom: 2.5},
	}); err != nil {
		t.Fatal(err)
	}

	copyTop, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, DestGridID: rootB, X: 3, Y: 3,
	})
	if err != nil {
		t.Fatalf("deep copy: %v", err)
	}
	if got := uuidOfTest(copyTop.ID); got != uuidB {
		t.Fatalf("copy landed in %s, want plugin B (%s)", got, uuidB)
	}
	if copyTop.ViewCx != 7 || copyTop.ViewCy != 8 || copyTop.ViewZoom != 2.5 {
		t.Errorf("framing lost: %+v", copyTop)
	}
	if copyTop.Reference {
		t.Fatal("the copy must be a SOLID well (a copy, not a link)")
	}

	// The copied child grid: text bytes, the nested subtree, the reference.
	cg, err := cl.GetGrid(ctx, copyTop.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	var copiedText, copiedLink, copiedNested *rpc.Tile
	for i := range cg.Tiles {
		tl := &cg.Tiles[i]
		switch {
		case tl.Kind == rpc.KindText && tl.LinkTargetID == "":
			copiedText = tl
		case tl.LinkTargetID != "":
			copiedLink = tl
		case tl.Kind == rpc.KindWell:
			copiedNested = tl
		}
	}
	if copiedText == nil || copiedLink == nil || copiedNested == nil {
		t.Fatalf("copied grid incomplete: %+v", cg.Tiles)
	}
	body, _, _, err := cl.ReadContent(ctx, copiedText.ID)
	if err != nil || string(body) != "# soup" {
		t.Fatalf("copied body = %q (%v)", body, err)
	}
	if copiedLink.LinkTargetID != inner.ID {
		t.Errorf("the leaf link must copy as a reference to the ORIGINAL target: %q", copiedLink.LinkTargetID)
	}
	ng, err := cl.GetGrid(ctx, copiedNested.ChildGridID)
	if err != nil || len(ng.Tiles) != 1 {
		t.Fatalf("nested subtree not copied: %v %v", ng, err)
	}

	// Independence: editing the copy leaves the source byte-identical.
	if _, err := cl.WriteContent(ctx, copiedText.ID, copiedText.Version, []byte("# changed")); err != nil {
		t.Fatal(err)
	}
	orig, _, _, err := cl.ReadContent(ctx, inner.ID)
	if err != nil || string(orig) != "# soup" {
		t.Fatalf("editing the copy changed the source: %q (%v)", orig, err)
	}
}

// uuidOfTest returns the namespace of a qualified id (test-local twin of
// rpc.UUIDOf, avoiding the import juggling in this file).
func uuidOfTest(id string) string {
	return rpc.UUIDOf(id)
}

// TestLinkLeafAcrossPlugins: the leaf face of the left-drag — the destination
// gains a text tile whose content lives in the source plugin's tile
// (link_target_id), readable through the target id, carrying provenance, and
// deleting it only unlinks.
func TestLinkLeafAcrossPlugins(t *testing.T) {
	cl, _, rootA, uuidB, rootB := twoPluginServer(t)
	ctx := context.Background()

	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# the one copy"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: rootB, X: 2, Y: 2, W: 1, H: 1,
		Kind: rpc.KindText, LinkTargetID: txt.ID,
		Label: txt.AltText,
	})
	if err != nil {
		t.Fatalf("cross-plugin leaf link: %v", err)
	}
	if u, _, _ := rpc.SplitID(link.ID); u != uuidB {
		t.Errorf("link lives in %q, want destination plugin %q", u, uuidB)
	}
	if link.Kind != rpc.KindText || link.LinkTargetID != txt.ID {
		t.Errorf("link shape: kind=%q target=%q, want text → %q", link.Kind, link.LinkTargetID, txt.ID)
	}
	if !link.Reference {
		t.Error("leaf link must be marked Reference (dashed border, unlink-only delete)")
	}
	// One copy: content is read THROUGH the target id the link carries.
	body, _, _, err := cl.ReadContent(ctx, link.LinkTargetID)
	if err != nil {
		t.Fatalf("content through link target: %v", err)
	}
	if string(body) != "# the one copy" {
		t.Errorf("content through target = %q", body)
	}

	// Deleting the link only unlinks — the source and its bytes survive.
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: link.ID}); err != nil {
		t.Fatalf("delete leaf link: %v", err)
	}
	if body, _, _, err := cl.ReadContent(ctx, txt.ID); err != nil || string(body) != "# the one copy" {
		t.Errorf("deleting the link touched the source: body=%q err=%v", body, err)
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
		TileID:     txt.ID,
		DestGridID: rootB, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-plugin text clone: %v", err)
	}
	if u, _, _ := rpc.SplitID(copyT.ID); u != uuidB {
		t.Errorf("copy lives in %q, want %q", u, uuidB)
	}
	body, _, _, err := cl.ReadContent(ctx, copyT.ID)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != "# portable" {
		t.Errorf("copied bytes = %q, want %q", body, "# portable")
	}

	// The copies are independent: editing the copy leaves the source alone.
	if _, err := cl.WriteContent(ctx, copyT.ID, copyT.Version, []byte("# changed")); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, _, err := cl.ReadContent(ctx, txt.ID)
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
		TileID: u.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-plugin url clone: %v", err)
	}
	if cp.URLString != "https://example.com/" {
		t.Errorf("copied url = %q", cp.URLString)
	}
}

// TestCloneAcrossPluginsCopiesCurrentContent: a clone carries no version
// claim (docs/simplify-plan.md S5 — the SOURCE row is untouched, so a copy is
// layout), and the handler's own hand-rolled re-derivation of the store's
// claim went with it. What the copy must carry is what the source says NOW,
// even though the source's version moved after it was created.
func TestCloneAcrossPluginsCopiesCurrentContent(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()

	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A real content edit: the source's version is now past what the clone
	// caller last saw.
	if _, err := cl.WriteContent(ctx, txt.ID, txt.Version, []byte("v1")); err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	cp, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: txt.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-plugin clone after a content edit: %v", err)
	}
	body, _, _, err := cl.ReadContent(ctx, cp.ID)
	if err != nil {
		t.Fatalf("ReadContent(copy): %v", err)
	}
	if string(body) != "v1" {
		t.Errorf("copied body = %q, want the source's current bytes %q", body, "v1")
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
		TileID: pt.ID, DestGridID: rootB, X: 1, Y: 1,
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
	body, _, _, err := cl.ReadContent(ctx, cp.ID)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != string(layout) {
		t.Errorf("copied layout = %q, want the source bytes (references preserved verbatim)", body)
	}

	// Independence: rearranging the copy leaves the source's layout alone.
	if _, err := cl.WriteContent(ctx, cp.ID, cp.Version,
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, _, err := cl.ReadContent(ctx, pt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(layout) {
		t.Errorf("editing the copy changed the source layout: %q", orig)
	}
}

// TestLinkDirWellFromFsPlugin (formerly the issue-#171 clone test): dragging
// a directory from an fs grid into a localdb grid creates a LINK — under the
// 2026-07-19 gestures that is the LEFT-drag, committed as a CreateWell
// carrying the fs dir's qualified grid. This crosses the real seam: server
// routing → in-process fs plugin (GetGrid materializes the dir tiles) → link
// created in the localdb destination.
func TestLinkDirWellFromFsPlugin(t *testing.T) {
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
	fsClient := newPluginClient(t, "fs", fsplugin.New(dir, nil))
	info, err := fsClient.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("fs info: %v", err)
	}
	const fsUUID = "fs-src-uuid"
	reg.Register(fsUUID, "fs", fsClient, nil)

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
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

	// The left-drag commit: the client builds the link from its cached tile
	// (the one it is dragging) — no read from the fs plugin is needed.
	link, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: dstRoot, X: 1, Y: 1, W: sub.W, H: sub.H,
		ChildGridID: sub.ChildGridID, Label: sub.AltText,
	})
	if err != nil {
		t.Fatalf("cross-plugin link from fs: %v", err)
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

	// The right-drag (clone) of a dir well is refused loudly — deep copy of a
	// host directory is unimplemented.
	if _, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: sub.ID, DestGridID: dstRoot, X: 3, Y: 3,
	}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("clone of an fs dir well: err=%v, want unimplemented refusal", err)
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
		TileID: pt.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-plugin clone of never-arranged pane: %v", err)
	}
	if cp.BlobID != 0 {
		t.Errorf("never-arranged copy grew a blob: %+v", cp)
	}
}
