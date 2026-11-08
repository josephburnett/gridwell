package server_test

// The files' behaviours through the router: every call crosses the web API
// to files.Source over a temp root.

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// tile is the row the grid id lists for path, fatal when it lists none.
func (n *filesNode) tile(gridID, path string) *gridwellv1.Tile {
	n.t.Helper()
	want := fileID(path)
	for _, t := range n.grid(gridID).Tiles {
		if t.Id == want {
			return t
		}
	}
	n.t.Fatalf("%s lists no %s", gridID, path)
	return nil
}

func (n *filesNode) move(t *gridwellv1.Tile, grid string, x, y int64) (*gridwellv1.Tile, error) {
	return n.cl.MoveTile(n.ctx, &gridwellv1.MoveTileRequest{TileId: t.Id, GridId: grid, X: x, Y: y, W: t.W, H: t.H,
		Claim: rpc.PlacementOf(t)})
}

// A directory lists its files and subdirectories sorted, a directory a door
// onto its own grid, a markdown file a text tile that renders, a plain file a
// plain one, and none of them minted.
func TestFilesListing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "b.md"), "# b\n")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "sub", "c.txt"), "c")
	n := newFilesNode(t, root)
	g := n.grid(n.roots[0])
	var ids []string
	for _, tl := range g.Tiles {
		ids = append(ids, tl.Id)
	}
	want := []string{fileID(filepath.Join(root, "a.txt")), fileID(filepath.Join(root, "b.md")), fileID(filepath.Join(root, "sub"))}
	if len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("listing = %v, want %v", ids, want)
	}
	sub := g.Tiles[2]
	if !rpc.IsDoorKind(sub.Kind) || sub.Link || sub.ChildGridId != fileID(filepath.Join(root, "sub")) {
		t.Errorf("sub = %v, want a door onto its own grid", sub)
	}
	if g.Tiles[0].Kind != rpc.KindText || g.Tiles[0].TextPresentation != rpc.TextPresentationPlain ||
		g.Tiles[1].TextPresentation != rpc.TextPresentationBoth {
		t.Errorf("presentations = %q %q", g.Tiles[0].TextPresentation, g.Tiles[1].TextPresentation)
	}
	if got := n.grid(sub.ChildGridId); len(got.Tiles) != 1 || got.Tiles[0].Id != fileID(filepath.Join(root, "sub", "c.txt")) {
		t.Errorf("sub's grid = %v", got.Tiles)
	}
}

// A move mints the row at the placement the listing derived and lands where
// it was dropped, and the placement survives the directory listing again with
// a new file in it.
func TestFilesPlacementSurvivesRelisting(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "a")
	n := newFilesNode(t, root)
	moved, err := n.move(n.tile(n.roots[0], f), n.roots[0], 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	if moved.X != 5 || moved.Y != 4 || moved.Id != fileID(f) {
		t.Fatalf("moved = %v", moved)
	}
	writeFile(t, filepath.Join(root, "0first.txt"), "z")
	if got := n.tile(n.roots[0], f); got.X != 5 || got.Y != 4 {
		t.Errorf("after relisting at (%d, %d), want (5, 4)", got.X, got.Y)
	}
}

// A save claims the content stamp it read; a file changed on disk since is
// refused as a conflict, and the file keeps the other writer's bytes.
func TestFilesStaleStampIsRefused(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "one")
	n := newFilesNode(t, root)
	_, _, basis, err := n.cl.ReadContent(n.ctx, fileID(f))
	if err != nil || basis.Stamp == "" {
		t.Fatalf("read: %v, stamp %q", err, basis.Stamp)
	}
	wrote, err := n.cl.WriteContent(n.ctx, fileID(f), basis, []byte("two"))
	if err != nil || wrote.ContentStamp == basis.Stamp {
		t.Fatalf("a clean save: %v, stamp %q", err, wrote.GetContentStamp())
	}
	writeFile(t, f, "someone else's")
	if _, err := n.cl.WriteContent(n.ctx, fileID(f), rpc.BasisOf(wrote), []byte("three")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a save over a changed file = %v, want a conflict", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "someone else's" {
		t.Errorf("file = %q, the refused save landed", b)
	}
}

// A file the node cannot write is read-only with the reason, and a write to
// it is refused before anything lands.
func TestFilesReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes any file")
	}
	root := t.TempDir()
	f := filepath.Join(root, "ro.txt")
	writeFile(t, f, "keep")
	if err := os.Chmod(f, 0o444); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	tl := n.tile(n.roots[0], f)
	if tl.ReadOnly == "" {
		t.Fatalf("a 0444 file is not read-only: %v", tl)
	}
	if _, err := n.cl.WriteContent(n.ctx, fileID(f), rpc.BasisOf(tl), []byte("x")); err == nil {
		t.Fatal("a write to a read-only file landed")
	}
	if b, _ := os.ReadFile(f); string(b) != "keep" {
		t.Errorf("file = %q", b)
	}
}

// A symlink is a link: to a file a link tile reading the file's bytes, to a
// directory a door onto the target's grid drawn as a link, and to nothing a
// link that reads dead.
func TestFilesSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real", "t.txt")
	writeFile(t, target, "target bytes")
	for name, to := range map[string]string{"tofile": target, "todir": filepath.Join(root, "real"), "dangling": filepath.Join(root, "nope")} {
		if err := os.Symlink(to, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	n := newFilesNode(t, root)
	file := n.tile(n.roots[0], filepath.Join(root, "tofile"))
	if !file.Link || file.LinkTargetId != fileID(target) {
		t.Fatalf("link to a file = %v, want a link to %s", file, fileID(target))
	}
	if b, _, _, err := n.cl.ReadContent(n.ctx, file.Id); err != nil || string(b) != "target bytes" {
		t.Errorf("reading through the link = %q, %v", b, err)
	}
	dir := n.tile(n.roots[0], filepath.Join(root, "todir"))
	if !rpc.IsDoorKind(dir.Kind) || !dir.Link || dir.ChildGridId != fileID(filepath.Join(root, "real")) {
		t.Errorf("link to a directory = %v, want a link door onto the target's grid", dir)
	}
	dead := n.tile(n.roots[0], filepath.Join(root, "dangling"))
	if !dead.Link {
		t.Fatalf("a dangling symlink = %v, want a link", dead)
	}
	if _, err := n.cl.GetTile(n.ctx, dead.LinkTargetId); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a dangling link's target reads %v, want dead", err)
	}
}

// Delete moves the file out of the tree by the trash and the listing no
// longer names it; its id reads dead.
func TestFilesDeleteGoesToTheTrash(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "a")
	n := newFilesNode(t, root)
	tl := n.tile(n.roots[0], f)
	if _, err := n.cl.DeleteTile(n.ctx, &gridwellv1.DeleteTileRequest{TileId: tl.Id, ClaimVersion: tl.Version, Claim: rpc.PlacementOf(tl)}); err != nil {
		t.Fatal(err)
	}
	if len(n.trashed) != 1 || n.trashed[0] != f {
		t.Fatalf("trashed = %v, want %s", n.trashed, f)
	}
	if g := n.grid(n.roots[0]); len(g.Tiles) != 0 {
		t.Errorf("the directory still lists %v", g.Tiles)
	}
	if _, err := n.cl.GetTile(n.ctx, tl.Id); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a deleted file reads %v, want dead", err)
	}
}

// A move into another directory renames the file and keeps its row: the name
// typed on a page file comes with it, and the old path reads dead.
func TestFilesMoveKeepsTheRowAndRetiresTheOldPath(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.pdf")
	writeFile(t, f, "%PDF-1.4\n")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	named, err := n.cl.RenameTile(n.ctx, fileID(f), n.tile(n.roots[0], f).Version, "my note")
	if err != nil {
		t.Fatal(err)
	}
	sub := fileID(filepath.Join(root, "sub"))
	moved, err := n.move(named, sub, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	landed := filepath.Join(root, "sub", "a.pdf")
	if moved.Id != fileID(landed) || moved.AltText != "my note" {
		t.Fatalf("moved = %v, want %s named my note", moved, fileID(landed))
	}
	if _, err := os.Stat(landed); err != nil {
		t.Fatalf("not on disk at %s: %v", landed, err)
	}
	if _, err := n.cl.GetTile(n.ctx, fileID(f)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("the old path reads %v, want dead", err)
	}
	home := n.home
	if _, err := n.move(n.tile(sub, landed), home, 0, 0); err == nil {
		t.Error("a move from the files into home landed; a move to another source is refused")
	}
}

// A directory some view shows is watched: a file made in it is the
// directory's GridChanged, and bytes written to one are its TileChanged,
// both reaching an open stream.
func TestFilesWatchReachesTheStream(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "one")
	n := newFilesNode(t, root)
	events := n.subscribe()
	n.grid(n.roots[0])
	if err := n.cl.SetView(n.ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{n.roots[0]}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "b.txt"), "new")
	await(t, events, "the directory's GridChanged", func(ev *gridwellv1.Event) bool {
		return ev.GetGridChanged().GetGridId() == n.roots[0]
	})
	writeFile(t, f, "two, longer")
	ev := await(t, events, "the file's TileChanged", func(ev *gridwellv1.Event) bool {
		return ev.GetTileChanged().GetTile().GetId() == fileID(f)
	})
	if !ev.GetTileChanged().GetContentChanged() {
		t.Errorf("TileChanged = %v, want content_changed", ev.GetTileChanged())
	}
}

// An image file is a page the node serves: a url tile whose page is the
// content API's, answering the file's bytes with its media type.
func TestFilesImagePageThroughTheContentAPI(t *testing.T) {
	root := t.TempDir()
	img := filepath.Join(root, "p.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	tl := n.tile(n.roots[0], img)
	if !rpc.PageContent(tl) {
		t.Fatalf("an image = %v, want a url tile whose page the node serves", tl)
	}
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(rpc.PageURL(n.url, lp.ContentToken, tl.Id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, buf.Bytes()) {
		t.Fatalf("page = %d %q, %d bytes; want 200 image/png and the file", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
}

// A page is streamed from the file to the response a chunk at a time, so a
// video plays while it arrives and no layer holds the whole file; a HEAD
// answers the page's media type and no bytes.
func TestFilesPageStreamsThroughTheContentAPI(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "clip.mp4")
	page := bytes.Repeat([]byte("0123456789abcdef"), 2<<20)
	if err := os.WriteFile(video, page, 0o644); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	tl := n.tile(n.roots[0], video)
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	url := rpc.PageURL(n.url, lp.ContentToken, tl.Id)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	runtime.ReadMemStats(&after)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp4" || got != int64(len(page)) {
		t.Fatalf("page = %d %q, %d bytes; want 200 video/mp4 and %d", resp.StatusCode, resp.Header.Get("Content-Type"), got, len(page))
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Errorf("serving a %d MiB page allocated %d MiB", len(page)>>20, n>>20)
	}
	head, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(head.Body)
	head.Body.Close()
	if head.StatusCode != http.StatusOK || head.Header.Get("Content-Type") != "video/mp4" || len(body) != 0 {
		t.Errorf("HEAD = %d %q, %d bytes", head.StatusCode, head.Header.Get("Content-Type"), len(body))
	}
}

// The selection is the directory's row: a select names a file, the grid
// answers it, and a find of the selection with no text finds it, the node's
// filter over the files' rows.
func TestFilesSelectionAndFindFilters(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	writeFile(t, a, "a")
	writeFile(t, b, "b")
	n := newFilesNode(t, root)
	if err := n.cl.SetSelection(n.ctx, &gridwellv1.SetSelectionRequest{GridId: n.roots[0], Add: []string{fileID(a)}}); err != nil {
		t.Fatal(err)
	}
	if sel := n.grid(n.roots[0]).Grid.Selection; len(sel) != 1 || sel[0] != fileID(a) {
		t.Fatalf("selection = %v, want %s", sel, fileID(a))
	}
	resp, err := n.raw.Find(n.ctx, connect.NewRequest(&gridwellv1.FindRequest{GridId: n.roots[0], Selected: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Results) != 1 || resp.Msg.Results[0].Tile.GetId() != fileID(a) {
		t.Fatalf("find selected = %v, want %s", resp.Msg.Results, fileID(a))
	}
	if err := n.cl.SetSelection(n.ctx, &gridwellv1.SetSelectionRequest{All: true, Clear: true}); err != nil {
		t.Fatal(err)
	}
	if sel := n.grid(n.roots[0]).Grid.Selection; len(sel) != 0 {
		t.Errorf("the all-clear left %v", sel)
	}
}

// A root keeps its framing on its grid row, which the roster carries, and a
// directory's door keeps its own.
func TestFilesFramingOnRootAndDoor(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	if _, err := n.cl.SetFraming(n.ctx, &gridwellv1.SetFramingRequest{RootGridId: n.roots[0], Cx: 3, Cy: 2, Zoom: 0.5}); err != nil {
		t.Fatal(err)
	}
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := lp.Sources[1].SourceGrids[0]; g.ViewCx != 3 || g.ViewCy != 2 || g.ViewZoom != 0.5 {
		t.Errorf("root framing on the roster = %v", g)
	}
	sub := filepath.Join(root, "sub")
	door, err := n.cl.SetFraming(n.ctx, &gridwellv1.SetFramingRequest{TileId: fileID(sub), Cx: -1, Cy: 4, Zoom: 2})
	if err != nil {
		t.Fatal(err)
	}
	if door.Id != fileID(sub) || door.ViewCx != -1 || door.ViewZoom != 2 {
		t.Fatalf("door after framing = %v", door)
	}
	if got := n.tile(n.roots[0], sub); got.ViewCx != -1 || got.ViewCy != 4 || got.ViewZoom != 2 {
		t.Errorf("door read back = %v", got)
	}
}

// A directory's door never copies: into home it lands as the link a
// left-drag makes, and a home door into a directory copies as another link.
// A file's text copies its bytes.
func TestFilesCopyIntoHome(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "the bytes")
	sub := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(sub, "c.txt"), "c")
	n := newFilesNode(t, root)
	n.grid(n.roots[0])
	link, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: fileID(sub), DestGridId: n.home, X: 0, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Link || link.ChildGridId != fileID(sub) || !rpc.IsDoorToOtherNamespace(link) {
		t.Fatalf("a directory copied into home = %v, want a link door onto it", link)
	}
	again, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: link.Id, DestGridId: n.home, X: 1, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Link || again.ChildGridId != fileID(sub) {
		t.Fatalf("a home door into a directory copied = %v, want another link onto it", again)
	}
	text, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: fileID(f), DestGridId: n.home, X: 2, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if text.Link || text.Kind != rpc.KindText || rpc.IsFilesID(text.Id) {
		t.Fatalf("a file's text copied = %v, want a home text tile", text)
	}
	if b, _, _, err := n.cl.ReadContent(n.ctx, text.Id); err != nil || string(b) != "the bytes" {
		t.Errorf("the copy's bytes = %q, %v", b, err)
	}
}
