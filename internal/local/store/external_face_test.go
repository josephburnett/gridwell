package store

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A plugin url tile keeps a screenshot and a standing freeze through the same
// writers home's rows take: one blob swap, one frozen write.

func mintOne(t *testing.T, d *Namespace, e *pluginv1.Entry) int64 {
	t.Helper()
	gid, err := d.ContextID("root")
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Mint(gid, e, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pageEntry(key string) *pluginv1.Entry {
	return &pluginv1.Entry{Key: key, Kind: rpc.KindURL, Label: key, ServesPage: true}
}

func TestAPluginURLTileKeepsAScreenshotThroughTheSharedBlobSwap(t *testing.T) {
	st, d := openExt(t)
	ctx := context.Background()
	a := mintOne(t, d, pageEntry("a.html"))
	b := mintOne(t, d, pageEntry("b.html"))

	if err := d.SetURLPreview(a, []byte("shot-1")); err != nil {
		t.Fatal(err)
	}
	got, err := d.Preview(a)
	if err != nil || string(got) != "shot-1" {
		t.Fatalf("Preview = %q (%v), want shot-1", got, err)
	}
	// The same bytes on another row share the one blob.
	if err := d.SetURLPreview(b, []byte("shot-1")); err != nil {
		t.Fatal(err)
	}
	verifyRefcounts(t, st)
	// A new capture replaces the face, and an empty one keeps it.
	if err := d.SetURLPreview(a, []byte("shot-2")); err != nil {
		t.Fatal(err)
	}
	if err := d.SetURLPreview(a, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Preview(a); string(got) != "shot-2" {
		t.Fatalf("Preview = %q, want shot-2", got)
	}
	verifyRefcounts(t, st)

	// Home cannot read a plugin row's face, nor a plugin a home row's.
	if _, err := st.GetTilePreview(ctx, itoa(a)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("home read a plugin row's preview: %v", err)
	}
	if _, err := st.Namespace("other").Preview(a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another namespace read the preview: %v", err)
	}
}

func TestAPluginScreenshotIsRefusedOnAnythingButAURLRow(t *testing.T) {
	_, d := openExt(t)
	text := mintOne(t, d, &pluginv1.Entry{Key: "n.md", Kind: rpc.KindText, Label: "n.md"})
	if err := d.SetURLPreview(text, []byte("shot")); !errors.Is(err, ErrNotURLTile) {
		t.Fatalf("SetURLPreview on a text row = %v, want ErrNotURLTile", err)
	}
	if err := d.SetFrozen(text, true); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("SetFrozen on a text row = %v, want ErrInvalidArgument", err)
	}
}

func TestAPluginURLTileHoldsTheStandingFreeze(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	id := mintOne(t, d, pageEntry("a.html"))
	for _, want := range []bool{true, false} {
		if err := d.SetFrozen(id, want); err != nil {
			t.Fatal(err)
		}
		tiles, err := d.Overlay(gid, []*pluginv1.Entry{pageEntry("a.html")})
		if err != nil {
			t.Fatal(err)
		}
		if got := extByKey(t, tiles, "a.html").UrlFrozen; got != want {
			t.Fatalf("url_frozen = %v, want %v", got, want)
		}
	}
}

// A retired row keeps its id but never shows a face again, so the blob it held
// is released rather than kept alive by a tombstone.
func TestRetiringAPluginRowReleasesItsScreenshot(t *testing.T) {
	st, d := openExt(t)
	id := mintOne(t, d, pageEntry("a.html"))
	if err := d.SetURLPreview(id, []byte("shot")); err != nil {
		t.Fatal(err)
	}
	var blob int64
	if err := st.db.QueryRow(`SELECT preview_blob_id FROM tiles WHERE id = ?`, id).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if err := d.Retire(id); err != nil {
		t.Fatal(err)
	}
	if blobExists(t, st, blob) {
		t.Fatal("the retired row's screenshot is still stored")
	}
	verifyRefcounts(t, st)
	if err := d.SetFrozen(id, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetFrozen on a retired row = %v, want ErrNotFound", err)
	}
}

// A screenshot a moved page retires releases its blob, and the row takes the
// next capture as any row does.
func TestDroppingAPluginScreenshotReleasesItsBlob(t *testing.T) {
	st, d := openExt(t)
	id := mintOne(t, d, pageEntry("a.html"))
	if err := d.DropURLPreview(id); err != nil {
		t.Fatalf("dropping no screenshot = %v, want nothing to do", err)
	}
	if err := d.SetURLPreview(id, []byte("shot")); err != nil {
		t.Fatal(err)
	}
	var blob int64
	if err := st.db.QueryRow(`SELECT preview_blob_id FROM tiles WHERE id = ?`, id).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if err := d.DropURLPreview(id); err != nil {
		t.Fatal(err)
	}
	if blobExists(t, st, blob) {
		t.Fatal("the dropped screenshot is still stored")
	}
	verifyRefcounts(t, st)
	if err := d.SetURLPreview(id, []byte("next")); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Preview(id); string(got) != "next" {
		t.Fatalf("Preview = %q after the next capture, want next", got)
	}
	if err := st.Namespace("other").DropURLPreview(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another namespace dropped the screenshot: %v", err)
	}
}

// A row answers a source that no longer lists it from its snapshot, so the
// snapshot holds every fact the row presents with. A page row without its
// serves_page would answer as a url tile with no address, which the client
// can only ask the user to fill in.
func TestAPageRowPresentsAsAPageWhenItsEntryIsAbsent(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	mintOne(t, d, pageEntry("a.html"))
	other := &pluginv1.Entry{Key: "b.md", Kind: rpc.KindText, Label: "b.md"}
	if err := d.Refresh(gid, []*pluginv1.Entry{other}); err != nil {
		t.Fatal(err)
	}
	tiles, err := d.Overlay(gid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := extByKey(t, tiles, "a.html"); !rpc.PageContent(got.Tile) || got.UrlString != "" {
		t.Fatalf("an absent page row answers %+v, want a page at the plugin's address", got.Tile)
	}

	// The snapshot follows what the source last said, both ways.
	addressed := &pluginv1.Entry{Key: "a.html", Kind: rpc.KindURL, Label: "a.html", UrlString: "https://a"}
	if err := d.Refresh(gid, []*pluginv1.Entry{addressed}); err != nil {
		t.Fatal(err)
	}
	tiles, _ = d.Overlay(gid, nil)
	if got := extByKey(t, tiles, "a.html"); got.ServesPage || got.UrlString != "https://a" {
		t.Fatalf("the snapshot kept a page the source stopped serving: %+v", got.Tile)
	}
	if err := d.Refresh(gid, []*pluginv1.Entry{pageEntry("a.html")}); err != nil {
		t.Fatal(err)
	}
	tiles, _ = d.Overlay(gid, nil)
	if got := extByKey(t, tiles, "a.html"); !rpc.PageContent(got.Tile) || got.UrlString != "" {
		t.Fatalf("the snapshot missed the page the source serves again: %+v", got.Tile)
	}
}
