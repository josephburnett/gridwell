package store

import (
	"context"
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A fresh workspace tile has no layout blob, meaning never arranged, carries its
// name, and SetWorkspaceLayout enforces the kind guard and the version claim.
func TestCreateWorkspaceDefaultsAndGuards(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	pt, err := s.CreateWorkspace(ctx, root, 0, 0, 2, 2, "workbench", false, nil)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if pt.Kind != rpc.KindWorkspace || pt.BlobId != 0 || pt.AltText != "workbench" {
		t.Fatalf("fresh workspace tile: %+v", pt)
	}
	id, err := parseID(pt.Id)
	if err != nil {
		t.Fatal(err)
	}

	// Kind guard: a text tile refuses the layout verb.
	txt, err := s.CreateText(ctx, root, 4, 0, 1, 1, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	txtID, _ := parseID(txt.Id)
	if _, err := s.SetWorkspaceLayout(ctx, txtID, txt.Version, []byte(`{"v":1}`)); !errors.Is(err, ErrNotWorkspace) {
		t.Errorf("SetWorkspaceLayout on text tile: err = %v, want ErrNotWorkspace", err)
	}
	// A layout is the user's bytes and claims its version. See
	// version_rule_test.go.
	if _, err := s.SetWorkspaceLayout(ctx, id, pt.Version+7, []byte(`{"v":1}`)); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale claim: err = %v, want ErrVersionConflict", err)
	}
	// An empty layout can never be written (NULL means never-arranged; an
	// arranged workspace always has at least one pane).
	if _, err := s.SetWorkspaceLayout(ctx, id, pt.Version, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty layout: err = %v, want ErrInvalidArgument", err)
	}
}

// TestSetWorkspaceLayoutStoresTypedBlob: the layout lands as a content-addressed
// blob tagged with the codec's media type (self-describing media — read back,
// never hard-coded at the read site).
func TestSetWorkspaceLayoutStoresTypedBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	pt, err := s.CreateWorkspace(ctx, root, 0, 0, 2, 2, "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := parseID(pt.Id)
	layout := []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)
	out, err := s.SetWorkspaceLayout(ctx, id, pt.Version, layout)
	if err != nil {
		t.Fatalf("SetWorkspaceLayout: %v", err)
	}
	if out.BlobId == 0 {
		t.Fatal("layout write left blob_id NULL")
	}
	var data []byte
	var mt string
	if err := s.db.QueryRow(`SELECT data, media_type FROM blobs WHERE id = ?`, out.BlobId).Scan(&data, &mt); err != nil {
		t.Fatal(err)
	}
	if string(data) != string(layout) {
		t.Errorf("stored layout differs")
	}
	if mt != panelayout.LayoutMediaType {
		t.Errorf("media_type = %q, want %q", mt, panelayout.LayoutMediaType)
	}
}

// Copy shares the layout blob by content address, and an edit to one copy can
// never touch the other: the first SetWorkspaceLayout moves the editor to a new
// blob while the sibling keeps the old one.
func TestWorkspaceCopySharesBlobThenDiverges(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	orig, err := s.CreateWorkspace(ctx, root, 0, 0, 2, 2, "ws", false,
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if orig.BlobId == 0 {
		t.Fatal("initial layout not stored")
	}
	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     orig.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Kind != rpc.KindWorkspace || cp.BlobId != orig.BlobId {
		t.Fatalf("copy must share the layout blob: %+v", cp)
	}

	copyID, _ := parseID(cp.Id)
	edited, err := s.SetWorkspaceLayout(ctx, copyID, cp.Version,
		[]byte(`{"v":1,"root":{"split":{"dir":"h","ratio":0.5,"a":{"pane":{"id":"p1","zoom":1}},"b":{"pane":{"id":"p2","zoom":1}}}},"focus":"p2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if edited.BlobId == orig.BlobId {
		t.Fatal("edit did not diverge the copy's blob")
	}
	// The original still reads its own bytes and its blob survives.
	if !blobExists(t, s, orig.BlobId) {
		t.Error("original's blob was released by the copy's edit")
	}

	// Deleting both tiles releases both blobs (tileRefs owns the mapping —
	// without the workspace arm the kind would silently own nothing and the blobs
	// would leak forever).
	for _, tile := range []*gridwellv1.Tile{orig, edited} {
		hardDelete(t, s, tile.Id)
	}
	if blobExists(t, s, orig.BlobId) || blobExists(t, s, edited.BlobId) {
		t.Error("layout blobs leaked after both tiles were deleted")
	}
}

// An ephemeral visit lives while a workspace tile's layout names it, whichever
// pane ends it, so a delete of one a layout holds keeps the row and a delete
// once none does deletes it; a layout that will not read could name any. The
// node is a config id other than the minted one, which the layout speaks.
func TestDeleteKeepsAVisitALayoutHolds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.SetNodeID("k3x9m2q")
	root := rootID(t, s)
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	visit := func(x int64) *gridwellv1.Tile {
		v, err := s.CreateURL(ctx, scratch, x, 0, 1, 1, "https://example.com", "", false)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	gone := func(v *gridwellv1.Tile) bool {
		_, err := s.GetTile(ctx, v.Id)
		return errors.Is(err, ErrNotFound)
	}
	held, loose := visit(0), visit(1)
	ws, err := s.CreateWorkspace(ctx, root, 0, 0, 1, 1, "ws", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	naming := func(id string) []byte {
		return []byte(`{"v":1,"root":{"pane":{"id":"p1","anchor":"k3x9m2q/` + scratch + `","path":["k3x9m2q/` + id + `"]}},"focus":"p1"}`)
	}
	n, err := s.WriteContent(ctx, ws.Id, ws.Version, naming(held.Id))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTile(ctx, rpc.DeleteOf(held)); err != nil || gone(held) {
		t.Fatalf("delete of a visit a layout names = %v, gone %v; want kept", err, gone(held))
	}
	if err := s.DeleteTile(ctx, rpc.DeleteOf(loose)); err != nil || !gone(loose) {
		t.Fatalf("delete of a visit no layout names = %v, gone %v; want deleted", err, gone(loose))
	}

	if n, err = s.WriteContent(ctx, ws.Id, n.Version, []byte("{not a layout")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, rpc.DeleteOf(held)); err != nil || gone(held) {
		t.Fatalf("delete beside a layout that will not read = %v, gone %v; want kept", err, gone(held))
	}

	if _, err = s.WriteContent(ctx, ws.Id, n.Version, naming("999")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, rpc.DeleteOf(held)); err != nil || !gone(held) {
		t.Fatalf("delete once no layout names it = %v, gone %v; want deleted", err, gone(held))
	}
}
