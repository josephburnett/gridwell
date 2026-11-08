package server_test

// What the files keep on a file's row, through the router and the web API:
// the arrangement while a root is dark, a page file's screenshot and freeze,
// and a text file's scroll.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A root that will not read keeps what the user arranged: a move of a
// touched file lands and reads back, a file nobody touched is not listed
// and cannot be moved, a subscriber arriving meanwhile is told the outage
// under the files' owner id with told carried through, and the root back,
// every file lists again with the move standing.
func TestFilesDarkRootKeepsTheArrangement(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	root := t.TempDir()
	notes, other := filepath.Join(root, "notes.md"), filepath.Join(root, "other.txt")
	writeFile(t, notes, "# n")
	writeFile(t, other, "o")
	n := newFilesNode(t, root)
	untouched := n.tile(n.roots[0], other)
	first, err := n.move(n.tile(n.roots[0], notes), n.roots[0], 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	moved, err := n.move(first, n.roots[0], 9, 9)
	if err != nil || moved.X != 9 {
		t.Fatalf("a move while dark = %v, %v", moved, err)
	}
	g := n.grid(n.roots[0])
	if len(g.Tiles) != 1 || g.Tiles[0].Id != fileID(notes) || g.Tiles[0].X != 9 || g.Tiles[0].AltText != "notes.md" {
		t.Fatalf("the dark listing = %v, want the touched file where it was put", g.Tiles)
	}
	if _, err := n.move(untouched, n.roots[0], 1, 1); err == nil {
		t.Error("a file nobody touched was moved under a dark root")
	}
	events := n.subscribe()
	h := await(t, events, "the outage", func(ev *gridwellv1.Event) bool { return ev.GetSourceHealth() != nil }).GetSourceHealth()
	if h.Healthy || h.Uuid != rpc.FilesOwner(localNodeID) || !h.GetTold() {
		t.Errorf("health = %v, want dark and still told", h)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	healed := n.grid(n.roots[0])
	if len(healed.Tiles) != 2 {
		t.Fatalf("the healed listing = %v", healed.Tiles)
	}
	if got := n.tile(n.roots[0], notes); got.X != 9 || got.Y != 9 {
		t.Errorf("the healed listing moved the tile: %v", got)
	}
}

// A page file keeps the screenshot it was left with, which its preview then
// is, and the standing freeze; a writeback naming an address, a title or a
// history is refused, its screenshot not stored, since a page file has none
// of its own.
func TestFilesPageKeepsItsScreenshotAndFreeze(t *testing.T) {
	root := t.TempDir()
	pdf := filepath.Join(root, "doc.pdf")
	writeFile(t, pdf, "%PDF-1.4\n")
	n := newFilesNode(t, root)
	page := n.tile(n.roots[0], pdf)
	if !rpc.PageContent(page) || page.PreviewBlobId != 0 {
		t.Fatalf("a pdf = %v, want a page with no preview yet", page)
	}
	for name, tile := range map[string]*gridwellv1.Tile{
		"url_string":  {Kind: rpc.KindURL, UrlString: "http://elsewhere.test/"},
		"alt_text":    {Kind: rpc.KindURL, AltText: "a page title"},
		"url_history": {Kind: rpc.KindURL, UrlHistory: `{"entries":[]}`},
	} {
		_, err := n.cl.SetTile(n.ctx, &gridwellv1.SetTileRequest{TileId: page.Id, Tile: tile, Preview: []byte("x")})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("a writeback carrying %s = %v, want refused", name, err)
		}
	}
	if k := n.tile(n.roots[0], pdf).PreviewBlobId; k != 0 {
		t.Fatalf("a refused writeback stored its screenshot: %d", k)
	}
	shot := []byte("\xff\xd8screenshot")
	if _, err := n.cl.SetTile(n.ctx, &gridwellv1.SetTileRequest{TileId: page.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: shot}); err != nil {
		t.Fatal(err)
	}
	if got, err := n.cl.GetTilePreview(n.ctx, page.Id); err != nil || !bytes.Equal(got, shot) {
		t.Fatalf("GetTilePreview = %q, %v; want the screenshot", got, err)
	}
	if k := n.tile(n.roots[0], pdf).PreviewBlobId; k <= 0 {
		t.Errorf("the listing does not key the screenshot: %d", k)
	}
	for _, want := range []bool{true, false} {
		if _, err := n.cl.SetFrozen(n.ctx, page.Id, want, !want); err != nil {
			t.Fatalf("SetFrozen(%v): %v", want, err)
		}
		if got := n.tile(n.roots[0], pdf).UrlFrozen; got != want {
			t.Fatalf("url_frozen = %v, want %v", got, want)
		}
	}
}

// A text file's scroll and mode are the row's, kept across a listing, and
// bump no version: framing claims no bytes.
func TestFilesTextScrollKeepsTheVersion(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "notes.md")
	writeFile(t, f, "# notes")
	n := newFilesNode(t, root)
	before := n.tile(n.roots[0], f)
	if _, err := n.cl.SetTile(n.ctx, &gridwellv1.SetTileRequest{TileId: before.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 12, TextY: 340, TextMode: rpc.TextModeRendered}}); err != nil {
		t.Fatal(err)
	}
	got := n.tile(n.roots[0], f)
	if got.TextX != 12 || got.TextY != 340 || got.TextMode != rpc.TextModeRendered || got.Version != before.Version {
		t.Errorf("after the scroll = %v, version was %d", got, before.Version)
	}
}
