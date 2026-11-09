package files

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

func read(t *testing.T, ns namespace.Namespace, id string) (data []byte, first *pb.ContentChunk) {
	t.Helper()
	err := ns.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: id}, func(c *pb.ContentChunk) error {
		if first == nil {
			first = c
		}
		data = append(data, c.Data...)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadContent(%s): %v", id, err)
	}
	return data, first
}

func writeAll(ns namespace.Namespace, id, stamp string, data []byte) (*pb.TileResponse, error) {
	sent := false
	return ns.WriteContent(context.Background(), func() (*pb.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &pb.WriteContentRequest{TileId: id, Data: data, ContentStamp: stamp}, nil
	})
}

// Writers claiming the same stamp at once are one write and conflicts: the
// claim and the rename are one step, so no save lands over another unseen.
func TestWritersClaimingOneStampLandOnce(t *testing.T) {
	f := newFixture(t)
	p := f.write("race.txt", "start")
	id := idOf(p)
	for round := range 20 {
		_, first := read(t, f.s, id)
		var landed atomic.Int32
		var wg sync.WaitGroup
		for w := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := bytes.Repeat([]byte{'a' + byte(w)}, 10+round*8+w)
				if _, err := writeAll(f.s, id, first.ContentStamp, body); err == nil {
					landed.Add(1)
				} else if status.Code(err) != codes.FailedPrecondition {
					t.Errorf("a writer: %v, want ok or the conflict", err)
				}
			}()
		}
		wg.Wait()
		if n := landed.Load(); n != 1 {
			t.Fatalf("round %d: %d writes claiming one stamp landed, want 1", round, n)
		}
	}
}

// One file through the namespace's own verbs: listed, touched, written under
// its stamp, and listed again with the new stamp and the row's scroll.
func TestAFileThroughTheNamespace(t *testing.T) {
	f := newFixture(t)
	p := f.write("notes.md", "# one")
	var ns namespace.Namespace = f.s
	ctx := context.Background()
	g, err := ns.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.root)})
	if err != nil {
		t.Fatal(err)
	}
	tl := tileIn(t, g, p)
	if _, err := ns.SetTile(ctx, &pb.SetTileRequest{TileId: tl.Id, Tile: &pb.Tile{Kind: rpc.KindText, TextY: 120, TextMode: "rendered"}}); err != nil {
		t.Fatal(err)
	}
	data, first := read(t, ns, tl.Id)
	if string(data) != "# one" || first.MediaType != "text/markdown" || first.ContentStamp != tl.ContentStamp {
		t.Fatalf("read %q %+v, listed stamp %q", data, first, tl.ContentStamp)
	}
	time.Sleep(10 * time.Millisecond)
	resp, err := writeAll(ns, tl.Id, first.ContentStamp, []byte("# two"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "# two" {
		t.Fatalf("on disk %q", b)
	}
	g, err = ns.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.root)})
	if err != nil {
		t.Fatal(err)
	}
	after := tileIn(t, g, p)
	if after.ContentStamp != resp.Tile.ContentStamp || after.ContentStamp == tl.ContentStamp {
		t.Errorf("stamps: listed %q, written %q, before %q", after.ContentStamp, resp.Tile.ContentStamp, tl.ContentStamp)
	}
	if after.TextY != 120 || after.TextMode != "rendered" || after.X != tl.X || after.Y != tl.Y {
		t.Errorf("the row after the write = %+v", after)
	}
	if left, _ := filepath.Glob(filepath.Join(f.root, ".notes.md.gridwell-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

// A write claims the stamp its bytes were read under; one the disk moved past
// is the version conflict, and the file keeps its bytes.
func TestAStaleStampIsAConflict(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.txt", "one")
	_, first := read(t, f.s, idOf(p))
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := writeAll(f.s, idOf(p), first.ContentStamp, []byte("mine"))
	wantCode(t, "stale write", err, codes.FailedPrecondition)
	if b, _ := os.ReadFile(p); string(b) != "other" {
		t.Errorf("on disk %q", b)
	}
	_, err = writeAll(f.s, idOf(p), "", []byte("mine"))
	wantCode(t, "unclaimed write", err, codes.FailedPrecondition)
}

// A file a write would refuse says so before it is typed, and the write says
// why.
func TestWhatAWriteRefusesIsReadOnly(t *testing.T) {
	f := newFixture(t)
	f.write("sum.bin", "x")
	big := f.write("big.txt", string(bytes.Repeat([]byte("x"), bodyCap+1)))
	locked := f.write("locked.txt", "x")
	if err := os.Chmod(locked, 0o444); err != nil {
		t.Fatal(err)
	}
	resp := f.grid(f.root)
	for _, n := range []string{"sum.bin", "big.txt"} {
		if tileIn(t, resp, f.path(n)).ReadOnly == "" {
			t.Errorf("%s is not read-only", n)
		}
	}
	_, first := read(t, f.s, idOf(big))
	_, err := writeAll(f.s, idOf(big), first.ContentStamp, []byte("x"))
	wantCode(t, "a body past the cap", err, codes.InvalidArgument)
	if os.Geteuid() == 0 {
		return
	}
	lt := tileIn(t, resp, locked)
	if lt.ReadOnly == "" {
		t.Error("a file its mode refuses is not read-only")
	}
	_, err = writeAll(f.s, lt.Id, lt.ContentStamp, []byte("y"))
	wantCode(t, "a file its mode refuses", err, codes.PermissionDenied)
}

// A move to another directory renames the file and the row follows under the
// new path with its id; the old path reads dead.
func TestAMoveRenamesAndKeepsTheRow(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.txt", "a")
	dir := f.mkdir("dir")
	f.write("dir/taken.txt", "t")
	taken := f.write("taken.txt", "t")
	ctx := context.Background()
	tl := tileIn(t, f.grid(f.root), p)
	if _, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: tl.Id, Tile: &pb.Tile{Kind: rpc.KindText, TextY: 9}}); err != nil {
		t.Fatal(err)
	}
	gid, _, _ := f.s.mem.LookupGrid(f.root)
	id, _, _ := f.s.mem.LiveTileID(gid, p)
	ev := f.events()
	resp, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: tl.Id, GridId: idOf(dir), X: 2, Y: 0, W: 1, H: 1,
		Claim: &pb.Placement{GridId: tl.GridId, X: tl.X, Y: tl.Y, W: tl.W, H: tl.H}})
	if err != nil {
		t.Fatal(err)
	}
	landed := filepath.Join(dir, "a.txt")
	if resp.Tile.Id != idOf(landed) || resp.Tile.TextY != 9 || resp.Tile.X != 2 {
		t.Errorf("moved tile = %+v", resp.Tile)
	}
	dgid, _, _ := f.s.mem.LookupGrid(dir)
	if moved, ok, _ := f.s.mem.LiveTileID(dgid, landed); !ok || moved != id {
		t.Errorf("row %d now %d (%v)", id, moved, ok)
	}
	if _, err := f.s.GetTile(ctx, &pb.GetTileRequest{TileId: tl.Id}); !gwerr.IsDeadLink(err) {
		t.Errorf("the old path reads %v, want dead", err)
	}
	await(t, ev, "the old directory's change", gridChanged(f.root))
	await(t, ev, "the new directory's change", gridChanged(dir))

	// Only a stale claim is the conflict, which the client reconciles; a
	// refusal with a reason snaps the move back.
	tt, dt := f.tile(taken), f.tile(dir)
	_, err = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(taken), GridId: idOf(dir), X: 0, Y: 1, W: 1, H: 1, Claim: rpc.PlacementOf(tt)})
	wantCode(t, "a taken name", err, codes.InvalidArgument)
	_, err = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(taken), GridId: idOf(f.path("gone")), X: 0, Y: 1, W: 1, H: 1, Claim: rpc.PlacementOf(tt)})
	wantCode(t, "a gone directory", err, codes.InvalidArgument)
	_, err = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(dir), GridId: idOf(dir), X: 0, Y: 1, W: 1, H: 1, Claim: rpc.PlacementOf(dt)})
	wantCode(t, "a directory into itself", err, codes.InvalidArgument)
	_, err = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(taken), GridId: "12", X: 0, Y: 1, W: 1, H: 1, Claim: rpc.PlacementOf(tt)})
	wantCode(t, "another source", err, codes.InvalidArgument)
	stale := tileIn(t, f.grid(f.root), taken)
	_, err = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: stale.Id, GridId: idOf(dir), X: 0, Y: 1, W: 1, H: 1,
		Claim: &pb.Placement{GridId: stale.GridId, X: stale.X + 5, Y: stale.Y, W: 1, H: 1}})
	wantCode(t, "a stale claim", err, codes.FailedPrecondition)
	if _, err := os.Stat(taken); err != nil {
		t.Errorf("a refused move touched the file: %v", err)
	}
}

// A delete moves the file to the trash, a link itself and never its target,
// and retires the row.
func TestADeleteTrashesThePathItself(t *testing.T) {
	f := newFixture(t)
	real := f.write("real.md", "# real")
	link := f.path("link")
	if err := os.Symlink("real.md", link); err != nil {
		t.Skip("symlinks unsupported")
	}
	ctx := context.Background()
	lt := tileIn(t, f.grid(f.root), link)
	if _, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: lt.Id, X: 5, Y: 5, W: 1, H: 1, Claim: rpc.PlacementOf(lt)}); err != nil {
		t.Fatal(err)
	}
	moved := f.tile(link)
	if _, err := f.s.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: lt.Id, ClaimVersion: moved.Version,
		Claim: &pb.Placement{GridId: moved.GridId, X: 5, Y: 5, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.trashed, []string{link}) {
		t.Errorf("trashed %v", f.trashed)
	}
	if _, err := os.Stat(real); err != nil {
		t.Errorf("the target went too: %v", err)
	}
	gid, _, _ := f.s.mem.LookupGrid(f.root)
	if _, ok, _ := f.s.mem.LiveTileID(gid, link); ok {
		t.Error("the row was not retired")
	}
	if _, err := f.s.DeleteTile(ctx, rpc.DeleteOf(moved)); err != nil {
		t.Errorf("deleting what is gone = %v", err)
	}
	_, err := f.s.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: idOf(f.root), Claim: &pb.Placement{}})
	wantCode(t, "a root", err, codes.InvalidArgument)
	if len(f.trashed) != 1 {
		t.Errorf("trashed %v", f.trashed)
	}
}

// A root's framing is on its grid row and comes back in Info; a door's is on
// its row.
func TestFramingOnARootAndOnADoor(t *testing.T) {
	f := newFixture(t)
	dir := f.mkdir("dir")
	ctx := context.Background()
	ev := f.events()
	if _, err := f.s.SetFraming(ctx, &pb.SetFramingRequest{RootGridId: idOf(f.root), Cx: 3, Cy: 4, Zoom: 2}); err != nil {
		t.Fatal(err)
	}
	await(t, ev, "the framing event", func(e *pb.Event) bool { return e.GetGridFramingChanged().GetGridId() == idOf(f.root) })
	info, err := f.s.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	g := info.SourceGrids[0]
	if g.GridId != idOf(f.root) || g.Label != "notes" || g.Glyph != rpc.GlyphFolder || g.ViewCx != 3 || g.ViewCy != 4 || g.ViewZoom != 2 {
		t.Errorf("Info grid = %+v", g)
	}
	resp, err := f.s.SetFraming(ctx, &pb.SetFramingRequest{TileId: idOf(dir), Cx: 1, Cy: 2, Zoom: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tile.ViewCx != 1 || resp.Tile.ViewZoom != 0.5 || resp.Tile.Id != idOf(dir) {
		t.Errorf("door = %+v", resp.Tile)
	}
	if got := tileIn(t, f.grid(f.root), dir); got.ViewCy != 2 {
		t.Errorf("listed door = %+v", got)
	}
}

// A directory's selection is its grid row's, named by path.
func TestADirectoryKeepsItsSelection(t *testing.T) {
	f := newFixture(t)
	a, b := f.write("a.txt", "a"), f.write("b.txt", "b")
	other := f.write("dir/c.txt", "c")
	ctx := context.Background()
	ev := f.events()
	if _, err := f.s.SetSelection(ctx, &pb.SetSelectionRequest{GridId: idOf(f.root), Add: []string{idOf(a), idOf(b), idOf(other), idOf(f.path("gone"))}}); err != nil {
		t.Fatal(err)
	}
	sel := await(t, ev, "the selection", func(e *pb.Event) bool { return e.GetSelectionChanged() != nil }).GetSelectionChanged()
	if want := []string{idOf(a), idOf(b)}; sel.GridId != idOf(f.root) || !slices.Equal(sel.TileIds, want) {
		t.Errorf("selection event = %+v, want %v", sel, want)
	}
	if _, err := f.s.SetSelection(ctx, &pb.SetSelectionRequest{GridId: idOf(f.root), Remove: []string{idOf(a)}}); err != nil {
		t.Fatal(err)
	}
	if got := f.grid(f.root).Grid.Selection; !slices.Equal(got, []string{idOf(b)}) {
		t.Errorf("grid selection = %v", got)
	}
	if _, err := f.s.SetSelection(ctx, &pb.SetSelectionRequest{All: true, Clear: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.grid(f.root).Grid.Selection; len(got) != 0 {
		t.Errorf("after clearing all = %v", got)
	}
}

// A page file's page is its whole bytes and media type; every other file's
// bytes are octet-stream, since they could be a page that runs a script: an
// html file, an svg, or a link named like a picture. A directory serves none.
func TestAFileServesItsBytes(t *testing.T) {
	f := newFixture(t)
	png := f.write("pic.png", "\x89PNG fake")
	bin := f.write("blob", "plain words")
	html := f.write("page.html", "<script>alert(1)</script>")
	svg := f.write("pic.svg", "<svg><script>alert(1)</script></svg>")
	disguised := f.path("disguised.png")
	if err := os.Symlink(html, disguised); err != nil {
		t.Skip("symlinks unsupported")
	}
	ctx := context.Background()
	serve := func(p string) ([]byte, string, error) {
		var data []byte
		media := ""
		err := f.s.ServeContent(ctx, &pb.ServeContentRequest{TileId: idOf(p)}, func(c *pb.ServeContentChunk) error {
			if media == "" {
				media = c.MediaType
			}
			data = append(data, c.Data...)
			return nil
		})
		return data, media, err
	}
	if data, media, err := serve(png); err != nil || media != "image/png" || string(data) != "\x89PNG fake" {
		t.Errorf("png = %q %q %v", data, media, err)
	}
	// Any other file's bytes are served as bytes no browser runs, a link's
	// by where it lands.
	for p, want := range map[string]string{bin: "plain words", html: "<script>alert(1)</script>",
		svg: "<svg><script>alert(1)</script></svg>", disguised: "<script>alert(1)</script>"} {
		if data, media, err := serve(p); err != nil || media != bytesMediaType || string(data) != want {
			t.Errorf("%s = %q %q %v, want its bytes as %s", filepath.Base(p), data, media, err, bytesMediaType)
		}
	}
	data, _, err := serve(f.mkdir("dir"))
	wantCode(t, "dir", err, codes.NotFound)
	if len(data) != 0 {
		t.Errorf("a directory served %q", data)
	}
	if tl := tileIn(t, f.grid(f.root), svg); tl.Kind != rpc.KindText || tl.ServesPage || tl.TextPresentation != rpc.TextPresentationPlain {
		t.Errorf("an svg = kind %q page %v presentation %q, want a plain text tile", tl.Kind, tl.ServesPage, tl.TextPresentation)
	}
	if _, _, err := serve(f.path("gone.png")); !gwerr.IsDeadLink(err) {
		t.Errorf("a gone page = %v", err)
	}
	if t2 := tileIn(t, f.grid(f.root), png); t2.Kind != rpc.KindURL {
		t.Fatal(t2)
	}
	if _, err := writeAll(f.s, idOf(png), "", []byte("https://example.com")); err == nil {
		t.Error("a page file took an address")
	}
}

// A page is read and sent a chunk at a time, never held whole.
func TestAPageIsSentAsItIsRead(t *testing.T) {
	f := newFixture(t)
	page := bytes.Repeat([]byte("0123456789abcdef"), 2<<20)
	p := f.write("clip.mp4", string(page))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	sent, chunks := 0, 0
	err := f.s.ServeContent(context.Background(), &pb.ServeContentRequest{TileId: idOf(p)}, func(c *pb.ServeContentChunk) error {
		sent, chunks = sent+len(c.Data), chunks+1
		return nil
	})
	runtime.ReadMemStats(&after)
	if err != nil || sent != len(page) || chunks < 2 {
		t.Fatalf("served %d bytes in %d chunks (%v), want %d", sent, chunks, err, len(page))
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Errorf("serving a %d MiB page allocated %d MiB", len(page)>>20, n>>20)
	}
}

// A large body reads whole in chunks.
func TestALargeBodyReadsInChunks(t *testing.T) {
	f := newFixture(t)
	body := bytes.Repeat([]byte("abcdefgh"), rpc.ContentChunkBytes/4)
	p := f.write("big.txt", string(body))
	n := 0
	var got []byte
	if err := f.s.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: idOf(p)}, func(c *pb.ContentChunk) error {
		n++
		got = append(got, c.Data...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n < 2 || !bytes.Equal(got, body) {
		t.Errorf("%d chunks, %d bytes, want %d", n, len(got), len(body))
	}
}

// A select ignores an id that names no file of the directory, a row's among
// them, and still lands the rest.
func TestASelectIgnoresWhatIsNotOnTheGrid(t *testing.T) {
	f := newFixture(t)
	a := f.write("a.txt", "a")
	ctx := context.Background()
	if _, err := f.s.SetSelection(ctx, &pb.SetSelectionRequest{GridId: idOf(f.root), Add: []string{"12", idOf(a)}, Remove: []string{"13"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.grid(f.root).Grid.Selection; !slices.Equal(got, []string{idOf(a)}) {
		t.Errorf("selection = %v", got)
	}
}

// A link owns no bytes: a write to it is refused with the reason, and its
// target keeps its own.
func TestAWriteToALinkIsRefused(t *testing.T) {
	f := newFixture(t)
	real := f.write("real.md", "# real")
	link := f.path("link.md")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	_, first := read(t, f.s, idOf(link))
	_, err := writeAll(f.s, idOf(link), first.ContentStamp, []byte("# through"))
	wantCode(t, "a write to a link", err, codes.InvalidArgument)
	if b, _ := os.ReadFile(real); string(b) != "# real" {
		t.Errorf("the target = %q", b)
	}
}
