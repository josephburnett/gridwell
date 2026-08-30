package store

import (
	"context"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A leaf link (text/url/shell/pane with a link_target_id) is the leaf twin of
// the exit well: the CONTENT lives in another plugin's tile, the local row is
// only a reference plus local framing. The store must never treat the target
// as something it owns — no blob, no content mutation, delete unlinks, clone
// copies the reference. These tests are the leaf faces of the exit-well suite
// above (exit_well_test.go).

const remoteTarget = "remote-uuid/42"

func blobRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return n
}

func TestCreateLeafLinkStoresQualifiedReference(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	for i, kind := range []string{rpc.KindText, rpc.KindURL, rpc.KindShell, rpc.KindPane} {
		ln, err := s.CreateLeafLink(ctx, root, int64(i*2), 10, 1, 1, kind, remoteTarget, "linked "+kind)
		if err != nil {
			t.Fatalf("create %s link: %v", kind, err)
		}
		if ln.Kind != kind || ln.LinkTargetID != remoteTarget {
			t.Errorf("%s link round-trip: kind=%q target=%q", kind, ln.Kind, ln.LinkTargetID)
		}
		if ln.BlobID != 0 || ln.URLString != "" || ln.PreviewBlobID != 0 {
			t.Errorf("%s link carries content of its own: blob=%d url=%q preview=%d", kind, ln.BlobID, ln.URLString, ln.PreviewBlobID)
		}
	}
	verifyRefcounts(t, s)
}

func TestCreateLeafLinkRejectsWellAndBareTargets(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// The well kind links via child_grid_id (the exit well), never
	// link_target_id — one link shape per kind, no second copy of the fact.
	if _, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindWell, remoteTarget, ""); err == nil {
		t.Error("CreateLeafLink accepted kind=well; wells link via child_grid_id")
	}
	// A bare (unqualified) target would be ambiguous outside the allocating
	// plugin — same rule as an exit well's qualified child.
	if _, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindText, "42", ""); err == nil {
		t.Error("CreateLeafLink accepted a bare integer target")
	}
}

func TestDeleteLeafLinkUnlinksOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ln, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindText, remoteTarget, "linked")
	if err != nil {
		t.Fatal(err)
	}
	primeTrash(t, s) // count the delete, not first-use trash minting
	gridsBefore, blobsBefore := gridRowCount(t, s), blobRowCount(t, s)
	hardDelete(t, s, ln.ID)
	if g, b := gridRowCount(t, s), blobRowCount(t, s); g != gridsBefore || b != blobsBefore {
		t.Errorf("deleting a leaf link touched owned storage: grids %d→%d blobs %d→%d", gridsBefore, g, blobsBefore, b)
	}
	verifyRefcounts(t, s)
}

func TestCloneLeafLinkCopiesReference(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ln, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindURL, remoteTarget, "linked")
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID:     ln.ID,
		DestGridID: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone leaf link: %v", err)
	}
	if clone.ID == ln.ID {
		t.Error("clone reused the source row id")
	}
	if clone.LinkTargetID != remoteTarget {
		t.Errorf("clone target = %q, want the shared reference %q", clone.LinkTargetID, remoteTarget)
	}
	verifyRefcounts(t, s)
}

func TestContentMutationOnLeafLinkRejected(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ln, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindText, remoteTarget, "linked")
	if err != nil {
		t.Fatal(err)
	}
	// A link owns no bytes: writing content through the link's own id must be
	// refused (the client routes content mutations by the TARGET id), or the
	// link and the thing it names silently diverge.
	_, err = s.WriteContent(ctx, ln.ID, ln.Version, []byte("smuggled"))
	if err == nil {
		t.Fatal("UpdateText on a leaf link succeeded; content lives in the target")
	}
	if !strings.Contains(err.Error(), "link") {
		t.Errorf("rejection should name the link mechanism, got: %v", err)
	}
	verifyRefcounts(t, s)
}

// Issue #239: text framing on a LINK row persists x/y/w/h but never
// text_mode — the v6 CHECK requires text_mode NULL on links (framing is
// per-link local; the mode is not). Before the fix the unconditional
// text_mode write failed the whole ascent framing save with a CHECK
// violation.
func TestSetTextViewOnLinkKeepsModeNull(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	link, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindText,
		"aabbccddeeff00112233445566778899/7", "linked doc")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
		TileID: link.ID,
		TextX:  3, TextY: 40, TextW: 300, TextH: 200, TextMode: rpc.TextModeRendered,
	})
	if err != nil {
		t.Fatalf("SetTextView on a link: %v", err)
	}
	if got.TextX != 3 || got.TextY != 40 || got.TextW != 300 || got.TextH != 200 {
		t.Errorf("framing not persisted: %+v", got)
	}
	if got.TextMode != "" {
		t.Errorf("text_mode = %q on a link, want empty (the CHECK's NULL)", got.TextMode)
	}
}
