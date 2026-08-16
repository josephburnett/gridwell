package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TestCreatePaneDefaultsAndGuards: a fresh pane tile has no layout blob
// ("never arranged" — descent installs the default single pane), carries its
// workspace name, and SetPaneLayout enforces the kind and version guards.
func TestCreatePaneDefaultsAndGuards(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	pt, err := s.CreatePane(ctx, root, 0, 0, 2, 2, "workbench", nil, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	if pt.Kind != rpc.KindPane || pt.BlobID != 0 || pt.AltText != "workbench" {
		t.Fatalf("fresh pane tile: %+v", pt)
	}
	id, err := parseID(pt.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Kind guard: a text tile refuses the layout verb.
	txt, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 4, Y: 0, W: 1, H: 1, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	txtID, _ := parseID(txt.ID)
	if _, err := s.SetPaneLayout(ctx, txtID, txt.Version, []byte(`{"v":1}`)); !errors.Is(err, ErrNotPaneTile) {
		t.Errorf("SetPaneLayout on text tile: err = %v, want ErrNotPaneTile", err)
	}
	// Version guard: a stale claim conflicts.
	if _, err := s.SetPaneLayout(ctx, id, pt.Version+7, []byte(`{"v":1}`)); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: err = %v, want ErrVersionConflict", err)
	}
	// An empty layout can never be written (NULL means never-arranged; an
	// arranged workspace always has at least one pane).
	if _, err := s.SetPaneLayout(ctx, id, pt.Version, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty layout: err = %v, want ErrInvalidArgument", err)
	}
}

// TestSetPaneLayoutStoresTypedBlob: the layout lands as a content-addressed
// blob tagged with the codec's media type (self-describing media — read back,
// never hard-coded at the read site).
func TestSetPaneLayoutStoresTypedBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	pt, err := s.CreatePane(ctx, root, 0, 0, 2, 2, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := parseID(pt.ID)
	layout := []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)
	out, err := s.SetPaneLayout(ctx, id, pt.Version, layout)
	if err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}
	if out.BlobID == 0 {
		t.Fatal("layout write left blob_id NULL")
	}
	var data []byte
	var mt string
	if err := s.db.QueryRow(`SELECT data, media_type FROM blobs WHERE id = ?`, out.BlobID).Scan(&data, &mt); err != nil {
		t.Fatal(err)
	}
	if string(data) != string(layout) {
		t.Errorf("stored layout differs")
	}
	if mt != panelayout.LayoutMediaType {
		t.Errorf("media_type = %q, want %q", mt, panelayout.LayoutMediaType)
	}
}

// TestPaneCloneSharesBlobThenDiverges: clone is an eager copy sharing the
// layout blob by content address (refcount), and an edit to one copy can
// never touch the other — the first SetPaneLayout moves the editor to a new
// blob while the sibling keeps the old one.
func TestPaneCloneSharesBlobThenDiverges(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	orig, err := s.CreatePane(ctx, root, 0, 0, 2, 2, "ws",
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if orig.BlobID == 0 {
		t.Fatal("initial layout not stored")
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: orig.ID, Version: orig.Version,
		DestGridID: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Kind != rpc.KindPane || clone.BlobID != orig.BlobID {
		t.Fatalf("clone must share the layout blob: %+v", clone)
	}

	cloneID, _ := parseID(clone.ID)
	edited, err := s.SetPaneLayout(ctx, cloneID, clone.Version,
		[]byte(`{"v":1,"root":{"split":{"dir":"h","ratio":0.5,"a":{"pane":{"id":"p1","zoom":1}},"b":{"pane":{"id":"p2","zoom":1}}}},"focus":"p2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if edited.BlobID == orig.BlobID {
		t.Fatal("edit did not diverge the clone's blob")
	}
	// The original still reads its own bytes and its blob survives.
	if !blobExists(t, s, orig.BlobID) {
		t.Error("original's blob was released by the clone's edit")
	}

	// Deleting both tiles releases both blobs (tileRefs owns the mapping —
	// without the pane arm the kind would silently own nothing and the blobs
	// would leak forever).
	for _, tile := range []*rpc.Tile{orig, edited} {
		hardDelete(t, s, tile.ID)
	}
	if blobExists(t, s, orig.BlobID) || blobExists(t, s, edited.BlobID) {
		t.Error("layout blobs leaked after both tiles were deleted")
	}
}

// TestWorkspaceRefsMatchTheInjectedIdentity is the production shape of issue
// #196: the plugin identity is the CONFIG id, injected post-verify
// (SetPluginID) — NOT the bootstrap-minted system.plugin_uuid. Workspace
// layout blobs qualify their references with the config id, so the
// ephemeral-refs matcher must speak it too; before the fix it compared
// against the mint, never matched, and the boot scratch sweep reaped
// workspace-owned shells (killing their tmux sessions). The older
// self-consistent tests couldn't catch this: they built the blob FROM
// PluginUUID, matching whatever it returned.
func TestWorkspaceRefsMatchTheInjectedIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	// The production identity: a config id DIFFERENT from the minted uuid.
	s.SetPluginID("k3x9m2q")

	pt, err := s.CreatePane(ctx, root, 0, 0, 1, 1, "ws", nil, "")
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	// A layout blob referencing scratch tile 41, qualified by the CONFIG id
	// — exactly what the client persister writes.
	layout := `{"v":1,"root":{"pane":{"id":"p1","anchor":"k3x9m2q/` + root + `","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"k3x9m2q/41"}},"focus":"p1"}`
	if _, err := s.WriteContent(ctx, pt.ID, pt.Version, []byte(layout)); err != nil {
		t.Fatalf("write layout: %v", err)
	}

	refs, unreadable, err := s.WorkspaceEphemeralRefs(ctx)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if unreadable {
		t.Fatal("blob unexpectedly unreadable")
	}
	if !refs["41"] {
		t.Fatalf("the config-id-qualified reference was not recognized (refs=%v) — the sweep would reap a workspace-owned shell (issue #196)", refs)
	}
}
