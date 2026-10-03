package store

import (
	"testing"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A plugin entry that names a link_target is a link row in the node's
// memory: derived as one, minted as one, converted to one in place when an
// existing content row's entry becomes a link, and back.

func linkEntry(key, targetContext, targetKey string) *pluginv1.Entry {
	return &pluginv1.Entry{Key: key, Kind: rpc.KindURL, Label: "link " + key, StatusDetail: "seen",
		ServesPage: true, LinkTarget: &pluginv1.EntryRef{Context: targetContext, Key: targetKey}}
}

// isLinkTo checks the content facts a link row holds: the target and none of
// its own.
func isLinkTo(t *testing.T, what string, tl ExtTile, target string) {
	t.Helper()
	if tl.LinkTargetId != target || tl.UrlString != "" || tl.ServesPage || tl.PreviewBlobId != 0 {
		t.Fatalf("%s = link %q url %q page %v preview %d, want a link to %q owning no content",
			what, tl.LinkTargetId, tl.UrlString, tl.ServesPage, tl.PreviewBlobId, target)
	}
}

func TestALinkEntryIsDerivedAndMintedAsALinkRow(t *testing.T) {
	st, d := openExt(t)
	target := rpc.EntryTileID("everything", "t1")
	entries := []*pluginv1.Entry{linkEntry("t1", "everything", "t1")}

	derived, err := d.Overlay(0, entries)
	if err != nil {
		t.Fatal(err)
	}
	isLinkTo(t, "the derived tile", extByKey(t, derived, "t1"), target)
	if n := rowCount(t, st); n != 0 {
		t.Fatalf("the join wrote %d rows", n)
	}

	gid, err := d.ContextID("box")
	if err != nil {
		t.Fatal(err)
	}
	tiles := mintAll(t, d, gid, entries, false)
	row := extByKey(t, tiles, "t1")
	stored, err := d.tiles(gid)
	if err != nil {
		t.Fatal(err)
	}
	got := extByKey(t, stored, "t1")
	isLinkTo(t, "the minted row", got, target)
	if got.ID != row.ID || got.Kind != rpc.KindURL || got.AltText != "link t1" {
		t.Fatalf("minted row = %+v", got)
	}
	// The outage path presents the stored row, still a link.
	outage, err := d.Overlay(gid, nil)
	if err != nil {
		t.Fatal(err)
	}
	isLinkTo(t, "the row its source does not list", extByKey(t, outage, "t1"), target)
}

func TestAContentRowListedAsALinkIsConvertedInPlace(t *testing.T) {
	st, d := openExt(t)
	gid, err := d.ContextID("box")
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Mint(gid, pageEntry("t1"), 0, 0, 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Place(id, 6, -3, 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.SetURLPreview(id, []byte("shot")); err != nil {
		t.Fatal(err)
	}

	if err := d.Refresh(gid, []*pluginv1.Entry{linkEntry("t1", "everything", "t1")}); err != nil {
		t.Fatal(err)
	}
	stored, err := d.tiles(gid)
	if err != nil {
		t.Fatal(err)
	}
	got := extByKey(t, stored, "t1")
	isLinkTo(t, "the converted row", got, rpc.EntryTileID("everything", "t1"))
	if got.ID != id || got.X != 6 || got.Y != -3 || got.W != 2 {
		t.Fatalf("conversion moved the row: %+v, want id %d at (6,-3) 2 wide", got, id)
	}
	// The screenshot was the row's own face; a link's face is its target's.
	verifyRefcounts(t, st)

	// A link's target is the listing's to move, like every other content fact.
	if err := d.Refresh(gid, []*pluginv1.Entry{linkEntry("t1", "everything", "t9")}); err != nil {
		t.Fatal(err)
	}
	stored, _ = d.tiles(gid)
	isLinkTo(t, "the re-targeted row", extByKey(t, stored, "t1"), rpc.EntryTileID("everything", "t9"))

	// And an entry that stops being a link converts back, under the same id.
	if err := d.Refresh(gid, []*pluginv1.Entry{{Key: "t1", Kind: rpc.KindURL, Label: "t1", UrlString: "https://x"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = d.tiles(gid)
	back := extByKey(t, stored, "t1")
	if back.ID != id || back.LinkTargetId != "" || back.UrlString != "https://x" {
		t.Fatalf("converted back = %+v", back)
	}
}
