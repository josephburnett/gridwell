package server_test

import (
	"context"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/claimtest"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Gestures across a namespace boundary, between the node's home and a far
// node's home through a connection: a left-drag creates a link, a door to
// another namespace for a grid or a leaf link for a leaf, and a right-drag
// creates a real copy, bytes for a leaf and a deep copy for a solid door. The
// left-drag arrives as a plain CreateTile carrying a qualified link, the
// same shape a + menu swatch drop uses, so both forms run through the real
// router seam. Framing and labels ride every link and copy. Each gesture
// runs from the far home (A) to the near one (B), the holder that can reach
// both, since a link is stored only where its target is reachable.

// farHome is the far node's home as the node qualifies it, through the
// connection newTwoHomes declares.
const farHome = localNodeID + "/geneva/rnode1"

// newTwoHomes serves a node whose home and a far node's home, through the
// connection "geneva", are the two namespaces a gesture crosses, with front,
// when set, between the router and the connection. It answers the node's
// client and both homes' root grids.
func newTwoHomes(t *testing.T, front func(namespace.Namespace) namespace.Namespace) (cl *rpc.Client, near, far string) {
	t.Helper()
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, front)
	lp, err := h.localCl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h.localCl, rpc.HomeGrid(lp), farHome + "/" + h.rootBare
}

// in reports whether the qualified id lives in the namespace ns.
func in(id, ns string) bool {
	return strings.HasPrefix(id, ns+"/") && !strings.Contains(strings.TrimPrefix(id, ns+"/"), "/")
}

func TestLinkDoorAcrossNodes(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	// A named door with content inside it, in the far home.
	door, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1, AltText: "recipes"}})
	if err != nil {
		t.Fatalf("CreateDoor: %v", err)
	}
	inner, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# soup"))
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	// The source door has a framing the user set — the preview the link
	// gesture carries along. Descending the link must land exactly where
	// descending the source would.
	framed, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: door.Id,
		Cx:     7, Cy: -2, Zoom: 1.75,
	})
	if err != nil {
		t.Fatalf("SetFraming: %v", err)
	}

	// LEFT-drag the door into the near home: the client commits a CreateDoor
	// carrying the source's qualified child grid, label, framing, and
	// provenance — the destination gains a LINK; there is only one copy of
	// the grid.
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootB, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 3, Y: 3, W: door.W, H: door.H, ChildGridId: door.ChildGridId, AltText: framed.AltText, ViewCx: framed.ViewCx, ViewCy: framed.ViewCy, ViewZoom: framed.ViewZoom}})
	if err != nil {
		t.Fatalf("cross-node link (CreateDoor): %v", err)
	}
	if link.ViewCx != 7 || link.ViewCy != -2 || link.ViewZoom != 1.75 {
		t.Errorf("link framing = (%v, %v, %v), want the source's (7, -2, 1.75) — a link must not reset the viewport",
			link.ViewCx, link.ViewCy, link.ViewZoom)
	}
	if !in(link.Id, localNodeID) {
		t.Errorf("link %q does not live in the destination %q", link.Id, localNodeID)
	}
	if link.ChildGridId != door.ChildGridId {
		t.Errorf("link child = %q, want the SOURCE door's grid %q (shared, not copied)", link.ChildGridId, door.ChildGridId)
	}
	if !link.Link {
		t.Error("cross-node link must be marked Link (dashed border, unlink-only delete)")
	}
	if link.AltText != "recipes" {
		t.Errorf("link label = %q, want the source's name", link.AltText)
	}
	// The grid is SHARED: reading the link's child sees the source's content.
	g, err := cl.GetGrid(ctx, link.ChildGridId)
	if err != nil {
		t.Fatalf("GetGrid through link: %v", err)
	}
	if len(g.Tiles) != 1 || g.Tiles[0].Id != inner.Id {
		t.Errorf("linked grid = %+v, want the source's tile %s", g.Tiles, inner.Id)
	}

	// Deleting the link only unlinks — the source door and its content survive.
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: link.Id})); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if _, err := cl.GetTile(ctx, door.Id); err != nil {
		t.Errorf("deleting the link destroyed the source door: %v", err)
	}
	if _, _, _, err := cl.ReadContent(ctx, inner.Id); err != nil {
		t.Errorf("deleting the link destroyed the source's content: %v", err)
	}
}

// TestCopyDoorAcrossNodesDeepCopies: a solid door right-dragged across
// a namespace boundary DEEP-COPIES — the destination gains an independent
// subtree, byte-identical bodies, framing preserved, provenance carried,
// links inside copied as links — and editing the copy never
// touches the source (no structural sharing across the boundary).
func TestCopyDoorAcrossNodesDeepCopies(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	door, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1, AltText: "recipes"}})
	if err != nil {
		t.Fatalf("CreateDoor: %v", err)
	}
	// Contents: a text body, a NESTED door with its own text, and a leaf
	// LINK (which must copy as a link, not as bytes).
	inner, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# soup"))
	if err != nil {
		t.Fatal(err)
	}
	nested, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 0, W: 1, H: 1, AltText: "drafts"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: nested.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# stock")); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 4, Y: 0, W: 1, H: 1, LinkTargetId: inner.Id, AltText: "soup-link"}}); err != nil {
		t.Fatal(err)
	}
	// Framing on the door (preview = descent = ascent).
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: door.Id, Cx: 7, Cy: 8, Zoom: 2.5,
	}); err != nil {
		t.Fatal(err)
	}

	copyTop, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: door.Id, DestGridId: rootB, X: 3, Y: 3,
	})
	if err != nil {
		t.Fatalf("deep copy: %v", err)
	}
	if !in(copyTop.Id, localNodeID) {
		t.Fatalf("copy %s did not land in the destination %s", copyTop.Id, localNodeID)
	}
	if copyTop.ViewCx != 7 || copyTop.ViewCy != 8 || copyTop.ViewZoom != 2.5 {
		t.Errorf("framing lost: %+v", copyTop)
	}
	if copyTop.Link {
		t.Fatal("the copy must be a SOLID door (a copy, not a link)")
	}

	// The copied child grid: text bytes, the nested subtree, the link.
	cg, err := cl.GetGrid(ctx, copyTop.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	var copiedText, copiedLink, copiedNested *gridwellv1.Tile
	for _, tl := range cg.Tiles {
		switch {
		case tl.Kind == rpc.KindText && tl.LinkTargetId == "":
			copiedText = tl
		case tl.LinkTargetId != "":
			copiedLink = tl
		case tl.Kind == rpc.KindDoor:
			copiedNested = tl
		}
	}
	if copiedText == nil || copiedLink == nil || copiedNested == nil {
		t.Fatalf("copied grid incomplete: %+v", cg.Tiles)
	}
	body, _, _, err := cl.ReadContent(ctx, copiedText.Id)
	if err != nil || string(body) != "# soup" {
		t.Fatalf("copied body = %q (%v)", body, err)
	}
	if copiedLink.LinkTargetId != inner.Id {
		t.Errorf("the leaf link must copy as a link to the ORIGINAL target: %q", copiedLink.LinkTargetId)
	}
	ng, err := cl.GetGrid(ctx, copiedNested.ChildGridId)
	if err != nil || len(ng.Tiles) != 1 {
		t.Fatalf("nested subtree not copied: %v %v", ng, err)
	}

	// Independence: editing the copy leaves the source byte-identical.
	if _, err := cl.WriteContent(ctx, copiedText.Id, rpc.ContentBasis{Version: copiedText.Version}, []byte("# changed")); err != nil {
		t.Fatal(err)
	}
	orig, _, _, err := cl.ReadContent(ctx, inner.Id)
	if err != nil || string(orig) != "# soup" {
		t.Fatalf("editing the copy changed the source: %q (%v)", orig, err)
	}
}

// TestLinkLeafAcrossNodes: the leaf case of the left-drag — the destination
// gains a text tile whose content lives in the source namespace's tile
// (link_target_id), readable through the target id, carrying provenance, and
// deleting it only unlinks.
func TestLinkLeafAcrossNodes(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# the one copy"))
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootB, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 2, W: 1, H: 1, LinkTargetId: txt.Id, AltText: txt.AltText}})
	if err != nil {
		t.Fatalf("cross-node leaf link: %v", err)
	}
	if !in(link.Id, localNodeID) {
		t.Errorf("link %q does not live in the destination %q", link.Id, localNodeID)
	}
	if link.Kind != rpc.KindText || link.LinkTargetId != txt.Id {
		t.Errorf("link shape: kind=%q target=%q, want text → %q", link.Kind, link.LinkTargetId, txt.Id)
	}
	if !link.Link {
		t.Error("leaf link must be marked Link (dashed border, unlink-only delete)")
	}
	// One copy: content is read THROUGH the target id the link carries.
	body, _, _, err := cl.ReadContent(ctx, link.LinkTargetId)
	if err != nil {
		t.Fatalf("content through link target: %v", err)
	}
	if string(body) != "# the one copy" {
		t.Errorf("content through target = %q", body)
	}

	// Deleting the link only unlinks — the source and its bytes survive.
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: link.Id})); err != nil {
		t.Fatalf("delete leaf link: %v", err)
	}
	if body, _, _, err := cl.ReadContent(ctx, txt.Id); err != nil || string(body) != "# the one copy" {
		t.Errorf("deleting the link touched the source: body=%q err=%v", body, err)
	}
}

func TestCopyLeafAcrossNodesCopiesBytes(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# portable"))
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	copyT, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     txt.Id,
		DestGridId: rootB, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-node text copy: %v", err)
	}
	if !in(copyT.Id, localNodeID) {
		t.Errorf("copy %q does not live in %q", copyT.Id, localNodeID)
	}
	body, _, _, err := cl.ReadContent(ctx, copyT.Id)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != "# portable" {
		t.Errorf("copied bytes = %q, want %q", body, "# portable")
	}

	// The copies are independent: editing the copy leaves the source alone.
	if _, err := cl.WriteContent(ctx, copyT.Id, rpc.ContentBasis{Version: copyT.Version}, []byte("# changed")); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, _, err := cl.ReadContent(ctx, txt.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != "# portable" {
		t.Errorf("editing the copy changed the source: %q", orig)
	}
}

func TestCopyURLAcrossNodesCopiesAddress(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	u, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: u.Id, DestGridId: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-node url copy: %v", err)
	}
	if cp.UrlString != "https://example.com/" {
		t.Errorf("copied url = %q", cp.UrlString)
	}
}

// TestCopyAcrossNodesCopiesCurrentContent: a copy carries no version
// claim (the SOURCE row is untouched, so a copy is
// layout), and the handler's own hand-rolled re-derivation of the store's
// claim went with it. What the copy must carry is what the source says NOW,
// even though the source's version moved after it was created.
func TestCopyAcrossNodesCopiesCurrentContent(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("v0"))
	if err != nil {
		t.Fatal(err)
	}
	// A real content edit: the source's version is now past what the copy
	// caller last saw.
	if _, err := cl.WriteContent(ctx, txt.Id, rpc.ContentBasis{Version: txt.Version}, []byte("v1")); err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: txt.Id, DestGridId: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-node copy after a content edit: %v", err)
	}
	body, _, _, err := cl.ReadContent(ctx, cp.Id)
	if err != nil {
		t.Fatalf("ReadContent(copy): %v", err)
	}
	if string(body) != "v1" {
		t.Errorf("copied body = %q, want the source's current bytes %q", body, "v1")
	}
}

// TestCopyWorkspaceAcrossNodesCopiesLayout: a workspace crosses a namespace
// boundary as a BYTE COPY of its layout blob (like text) — and because the
// layout's ids are owner-frame-relative by the codec's rule, the copy's panes
// keep naming the ORIGINAL places: arrangement copies, referenced content is
// shared. The copies then diverge independently (content addressing).
func TestCopyWorkspaceAcrossNodesCopiesLayout(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	layout := []byte(`{"v":1,"root":{"pane":{"id":"p1","anchor":"elsewhere/1","zoom":1}},"focus":"p1"}`)
	pt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 0, Y: 0, W: 2, H: 2, AltText: "ws"}}, layout)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: pt.Id, DestGridId: rootB, X: 1, Y: 1,
	})
	if err != nil {
		t.Fatalf("cross-node pane copy: %v", err)
	}
	if !in(cp.Id, localNodeID) {
		t.Errorf("copy %q does not live in %q", cp.Id, localNodeID)
	}
	if cp.Kind != rpc.KindWorkspace || cp.AltText != "ws" {
		t.Errorf("copy shape: %+v", cp)
	}
	body, _, _, err := cl.ReadContent(ctx, cp.Id)
	if err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if string(body) != string(layout) {
		t.Errorf("copied layout = %q, want the source bytes (references preserved verbatim)", body)
	}

	// Independence: rearranging the copy leaves the source's layout alone.
	if _, err := cl.WriteContent(ctx, cp.Id, rpc.ContentBasis{Version: cp.Version},
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	orig, _, _, err := cl.ReadContent(ctx, pt.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(layout) {
		t.Errorf("editing the copy changed the source layout: %q", orig)
	}
}

// TestCopyWorkspaceAcrossNodesNeverArranged: a workspace tile with no layout blob
// copies as an empty workspace (no Data round trip to fail on).
func TestCopyWorkspaceAcrossNodesNeverArranged(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	pt, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 0, Y: 0, W: 2, H: 2, AltText: "empty"}})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: pt.Id, DestGridId: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("cross-node copy of never-arranged pane: %v", err)
	}
	if cp.BlobId != 0 {
		t.Errorf("never-arranged copy grew a blob: %+v", cp)
	}
}

// TestCopyURLAcrossNodesCarriesPreview: the frozen preview is what a url tile
// looks like from outside, so a copy of one wears the same preview — at the top
// level, exactly as a url one level down inside a deep-copied door does.
func TestCopyURLAcrossNodesCarriesPreview(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()

	u, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/album"}})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	jpeg := []byte("\xff\xd8frozen-preview")
	frozen, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId:  u.Id,
		Tile:    &gridwellv1.Tile{Kind: rpc.KindURL, AltText: "album", UrlHistory: `["https://example.com/"]`},
		Preview: jpeg,
	})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if frozen.PreviewBlobId == 0 {
		t.Fatalf("the source never froze: %+v", frozen)
	}

	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId: u.Id, DestGridId: rootB, X: 2, Y: 2,
	})
	if err != nil {
		t.Fatalf("cross-node url copy: %v", err)
	}
	if cp.PreviewBlobId == 0 {
		t.Errorf("the copy answered with no preview: %+v", cp)
	}
	got, err := cl.GetTile(ctx, cp.Id)
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if got.PreviewBlobId == 0 {
		t.Fatalf("the copy carries no frozen preview: %+v", got)
	}
	preview, err := cl.GetTilePreview(ctx, cp.Id)
	if err != nil {
		t.Fatalf("copy preview: %v", err)
	}
	if string(preview) != string(jpeg) {
		t.Errorf("copied preview = %q, want the source's %q", preview, jpeg)
	}
	if got.UrlHistory != frozen.UrlHistory {
		t.Errorf("copied history = %q, want the source's %q", got.UrlHistory, frozen.UrlHistory)
	}
}
