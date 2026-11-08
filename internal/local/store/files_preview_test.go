package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A page file's url tile keeps a screenshot and a standing freeze through the same
// writers home's rows take: one blob swap, one frozen write.

func mintOne(t *testing.T, d *FileRows, e Entry) int64 {
	t.Helper()
	gid, err := d.GridOf("root")
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Mint(gid, e, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pageEntry(key string) Entry {
	return Entry{Key: key, Kind: rpc.KindURL, Label: key, ServesPage: true}
}

func TestAFileURLTileKeepsAScreenshotThroughTheSharedBlobSwap(t *testing.T) {
	st, d := openExt(t)
	ctx := context.Background()
	a := mintOne(t, d, pageEntry("a.html"))
	b := mintOne(t, d, pageEntry("b.html"))

	if _, err := d.SetURLPreview(a, []byte("shot-1")); err != nil {
		t.Fatal(err)
	}
	got, err := d.Preview(a)
	if err != nil || string(got) != "shot-1" {
		t.Fatalf("Preview = %q (%v), want shot-1", got, err)
	}
	// The same bytes on another row share the one blob.
	if _, err := d.SetURLPreview(b, []byte("shot-1")); err != nil {
		t.Fatal(err)
	}
	verifyRefcounts(t, st)
	// A new capture replaces the preview, and an empty one keeps it.
	if _, err := d.SetURLPreview(a, []byte("shot-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetURLPreview(a, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Preview(a); string(got) != "shot-2" {
		t.Fatalf("Preview = %q, want shot-2", got)
	}
	verifyRefcounts(t, st)

	// Home cannot read a file row's preview.
	if _, err := st.GetTilePreview(ctx, itoa(a)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("home read a file row's preview: %v", err)
	}
}

func TestAFileScreenshotIsRefusedOnAnythingButAURLRow(t *testing.T) {
	_, d := openExt(t)
	text := mintOne(t, d, Entry{Key: "n.md", Kind: rpc.KindText, Label: "n.md"})
	if _, err := d.SetURLPreview(text, []byte("shot")); !errors.Is(err, ErrNotURLTile) {
		t.Fatalf("SetURLPreview on a text row = %v, want ErrNotURLTile", err)
	}
	if _, err := d.SetFrozen(text, true, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("SetFrozen on a text row = %v, want ErrInvalidArgument", err)
	}
}

func TestAFileURLTileHoldsTheStandingFreeze(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	id := mintOne(t, d, pageEntry("a.html"))
	for _, want := range []bool{true, false} {
		if _, err := d.SetFrozen(id, want, nil); err != nil {
			t.Fatal(err)
		}
		tiles, err := d.Overlay(gid, []Entry{pageEntry("a.html")})
		if err != nil {
			t.Fatal(err)
		}
		if got := fileByKey(t, tiles, "a.html").UrlFrozen; got != want {
			t.Fatalf("url_frozen = %v, want %v", got, want)
		}
	}
}

// A retired row keeps its id but never shows a preview again, so the blob it held
// is released rather than kept alive by a tombstone.
func TestRetiringAFileRowReleasesItsScreenshot(t *testing.T) {
	st, d := openExt(t)
	id := mintOne(t, d, pageEntry("a.html"))
	if _, err := d.SetURLPreview(id, []byte("shot")); err != nil {
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
	if _, err := d.SetFrozen(id, true, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetFrozen on a retired row = %v, want ErrNotFound", err)
	}
}

// A screenshot a moved page retires releases its blob, and the row takes the
// next capture as any row does.
func TestDroppingAFileScreenshotReleasesItsBlob(t *testing.T) {
	st, d := openExt(t)
	id := mintOne(t, d, pageEntry("a.html"))
	if err := d.DropURLPreview(id); err != nil {
		t.Fatalf("dropping no screenshot = %v, want nothing to do", err)
	}
	if _, err := d.SetURLPreview(id, []byte("shot")); err != nil {
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
	if _, err := d.SetURLPreview(id, []byte("next")); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Preview(id); string(got) != "next" {
		t.Fatalf("Preview = %q after the next capture, want next", got)
	}
}

// A row answers a source that no longer lists it from its snapshot, so the
// snapshot holds every fact the row presents with. A page row without its
// serves_page would answer as a url tile with no address, which the client
// can only ask the user to fill in.
func TestAPageRowPresentsAsAPageWhenItsEntryIsAbsent(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	mintOne(t, d, pageEntry("a.html"))
	other := Entry{Key: "b.md", Kind: rpc.KindText, Label: "b.md"}
	if err := d.Refresh(gid, []Entry{other}); err != nil {
		t.Fatal(err)
	}
	tiles, err := d.Overlay(gid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, tiles, "a.html"); !rpc.PageContent(got.Tile) || got.UrlString != "" {
		t.Fatalf("an absent page row answers %+v, want a page at the file's address", got.Tile)
	}

	// The snapshot follows what the source last said, both ways.
	addressed := Entry{Key: "a.html", Kind: rpc.KindURL, Label: "a.html"}
	if err := d.Refresh(gid, []Entry{addressed}); err != nil {
		t.Fatal(err)
	}
	tiles, _ = d.Overlay(gid, nil)
	if got := fileByKey(t, tiles, "a.html"); got.ServesPage {
		t.Fatalf("the snapshot kept a page the source stopped serving: %+v", got.Tile)
	}
	if err := d.Refresh(gid, []Entry{pageEntry("a.html")}); err != nil {
		t.Fatal(err)
	}
	tiles, _ = d.Overlay(gid, nil)
	if got := fileByKey(t, tiles, "a.html"); !rpc.PageContent(got.Tile) || got.UrlString != "" {
		t.Fatalf("the snapshot missed the page the source serves again: %+v", got.Tile)
	}
}
