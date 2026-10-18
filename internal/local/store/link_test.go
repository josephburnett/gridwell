package store

import (
	"context"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A leaf link is the exit well's leaf twin: the content lives in another
// tile, and the local row is a link plus local framing. The store never
// treats the target as something it owns, so there is no blob, no content
// mutation, delete unlinks, and copy copies the link.

const remoteTarget = "remote-uuid/42"

func blobRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return n
}

func TestCreateLeafLinkStoresQualifiedLink(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	for i, kind := range []string{rpc.KindText, rpc.KindURL, rpc.KindShell, rpc.KindPane} {
		ln, err := s.CreateLeafLink(ctx, root, int64(i*2), 10, 1, 1, kind, remoteTarget, "linked "+kind)
		if err != nil {
			t.Fatalf("create %s link: %v", kind, err)
		}
		if ln.Kind != kind || ln.LinkTargetId != remoteTarget {
			t.Errorf("%s link round-trip: kind=%q target=%q", kind, ln.Kind, ln.LinkTargetId)
		}
		if ln.BlobId != 0 || ln.UrlString != "" || ln.PreviewBlobId != 0 {
			t.Errorf("%s link carries content of its own: blob=%d url=%q preview=%d", kind, ln.BlobId, ln.UrlString, ln.PreviewBlobId)
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
	hardDelete(t, s, ln.Id)
	if g, b := gridRowCount(t, s), blobRowCount(t, s); g != gridsBefore || b != blobsBefore {
		t.Errorf("deleting a leaf link touched owned storage: grids %d→%d blobs %d→%d", gridsBefore, g, blobsBefore, b)
	}
	verifyRefcounts(t, s)
}

func TestCopyLeafLinkCopiesLink(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ln, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindURL, remoteTarget, "linked")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     ln.Id,
		DestGridId: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy leaf link: %v", err)
	}
	if cp.Id == ln.Id {
		t.Error("copy reused the source row id")
	}
	if cp.LinkTargetId != remoteTarget {
		t.Errorf("copy target = %q, want the shared link %q", cp.LinkTargetId, remoteTarget)
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
	_, err = s.WriteContent(ctx, ln.Id, ln.Version, []byte("smuggled"))
	if err == nil {
		t.Fatal("UpdateText on a leaf link succeeded; content lives in the target")
	}
	if !strings.Contains(err.Error(), "link") {
		t.Errorf("rejection should name the link mechanism, got: %v", err)
	}
	verifyRefcounts(t, s)
}

// Text framing on a link row persists x, y, w and h but never text_mode: the
// CHECK requires it NULL on a link, and an unconditional write fails the whole
// ascent framing save.
func TestSetTextScrollOnLinkKeepsModeNull(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	link, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1, rpc.KindText,
		"aabbccddeeff00112233445566778899/7", "linked doc")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetTextScroll(ctx, link.Id, 3, 40, 300, 200, rpc.TextModeRendered)
	if err != nil {
		t.Fatalf("SetTextScroll on a link: %v", err)
	}
	if got.TextX != 3 || got.TextY != 40 || got.TextW != 300 || got.TextH != 200 {
		t.Errorf("framing not persisted: %+v", got)
	}
	if got.TextMode != "" {
		t.Errorf("text_mode = %q on a link, want empty (the CHECK's NULL)", got.TextMode)
	}
}
