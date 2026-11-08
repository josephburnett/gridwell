package files

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A directory lists by name, dotfiles included: a directory is a door onto
// its own grid, an image, video, audio or PDF a url tile whose page the node
// serves, and every other file a text tile, plain or both by its name.
func TestADirectoryListsItsKindsInNameOrder(t *testing.T) {
	f := newFixture(t)
	f.mkdir("sub")
	for _, n := range []string{"notes.md", "main.go", "page.html", "data.bin", ".hidden", "pic.png", "doc.pdf", "song.mp3", "clip.mp4"} {
		f.write(n, "x")
	}
	resp := f.grid(f.root)
	want := []string{".hidden", "clip.mp4", "data.bin", "doc.pdf", "main.go", "notes.md", "page.html", "pic.png", "song.mp3", "sub"}
	if got := names(resp); !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	g := resp.Grid
	if g.Id != idOf(f.root) || g.GetAcceptsTiles() || !g.Writable || !g.GetMoves() || g.Glyph != rpc.GlyphFolder {
		t.Errorf("grid stamps = %+v", g)
	}
	door := tileIn(t, resp, f.path("sub"))
	if door.Kind != rpc.KindDoor || door.ChildGridId != idOf(f.path("sub")) || door.Link {
		t.Errorf("door = %+v", door)
	}
	for name, want := range map[string]string{"notes.md": rpc.TextPresentationBoth, "data.bin": rpc.TextPresentationBoth,
		"main.go": rpc.TextPresentationPlain, "page.html": rpc.TextPresentationPlain, ".hidden": rpc.TextPresentationBoth} {
		tl := tileIn(t, resp, f.path(name))
		if tl.Kind != rpc.KindText || tl.TextPresentation != want || tl.ServesPage || tl.ContentStamp == "" {
			t.Errorf("%s = kind %q presentation %q page %v stamp %q", name, tl.Kind, tl.TextPresentation, tl.ServesPage, tl.ContentStamp)
		}
	}
	for _, name := range []string{"pic.png", "doc.pdf", "song.mp3", "clip.mp4"} {
		if tl := tileIn(t, resp, f.path(name)); tl.Kind != rpc.KindURL || !tl.ServesPage || tl.UrlString != "" {
			t.Errorf("%s = kind %q page %v url %q", name, tl.Kind, tl.ServesPage, tl.UrlString)
		}
	}
	if tl := tileIn(t, resp, f.path("pic.png")); tl.PreviewBlobId >= 0 {
		t.Errorf("an image's own picture is keyed below zero, got %d", tl.PreviewBlobId)
	}
	if tl := tileIn(t, resp, f.path("doc.pdf")); tl.PreviewBlobId != 0 {
		t.Errorf("a pdf has no picture of its own, got %d", tl.PreviewBlobId)
	}
	if tl := tileIn(t, resp, f.path("data.bin")); tl.ReadOnly == "" {
		t.Error("a file shown as its summary is read-only")
	}
}

// Listing mints nothing; a touched tile keeps the placement it was answered
// at, and a relisting with a new name moves no row.
func TestAMintedPlacementSurvivesARelisting(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.write("a.txt", "a"), f.write("b.txt", "b"), f.write("c.txt", "c")
	first := f.grid(f.root)
	if _, ok, _ := f.s.mem.LookupGrid(f.root); ok {
		t.Fatal("a listing minted a grid row")
	}
	bt := tileIn(t, first, b)
	if _, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: bt.Id, X: 7, Y: 7, W: 2, H: 1,
		Claim: &pb.Placement{GridId: bt.GridId, X: bt.X, Y: bt.Y, W: bt.W, H: bt.H}}); err != nil {
		t.Fatal(err)
	}
	f.write("aa.txt", "new")
	again := f.grid(f.root)
	if got := tileIn(t, again, b); got.X != 7 || got.Y != 7 || got.W != 2 {
		t.Errorf("moved tile = %d,%d %dx%d, want 7,7 2x1", got.X, got.Y, got.W, got.H)
	}
	if got, was := tileIn(t, again, a), tileIn(t, first, a); got.X != was.X || got.Y != was.Y {
		t.Errorf("an untouched tile moved: %d,%d then %d,%d", was.X, was.Y, got.X, got.Y)
	}
	ct := tileIn(t, again, c)
	if _, err := f.s.SetTile(context.Background(), &pb.SetTileRequest{TileId: ct.Id,
		Tile: &pb.Tile{Kind: rpc.KindText, TextY: 40}}); err != nil {
		t.Fatal(err)
	}
	if got := tileIn(t, f.grid(f.root), c); got.X != ct.X || got.Y != ct.Y {
		t.Errorf("touching a tile moved it: %d,%d then %d,%d", ct.X, ct.Y, got.X, got.Y)
	}
}

// A path no root holds, or one reached through a symlinked directory, is
// dead; so is a file or directory that is gone.
func TestWhatTheTreeDoesNotHoldIsDead(t *testing.T) {
	f := newFixture(t)
	f.write("dir/inner.txt", "x")
	if err := os.Symlink(f.path("dir"), f.path("dirlink")); err != nil {
		t.Skip("symlinks unsupported")
	}
	ctx := context.Background()
	for _, p := range []string{f.base, filepath.Join(f.base, "elsewhere.txt"), f.path("gone.txt"), f.path("dirlink/inner.txt")} {
		if _, err := f.s.GetTile(ctx, &pb.GetTileRequest{TileId: idOf(p)}); !gwerr.IsDeadLink(err) {
			t.Errorf("GetTile(%s) = %v, want dead", p, err)
		}
	}
	for _, p := range []string{f.base, f.path("gone"), f.path("dirlink")} {
		if _, err := f.s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(p)}); !gwerr.IsDeadLink(err) {
			t.Errorf("GetGrid(%s) = %v, want dead", p, err)
		}
	}
	if _, err := f.s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.path("dir/inner.txt"))}); !gwerr.IsDeadLink(err) {
		t.Errorf("a file read as a grid = %v, want dead", err)
	}
	_, err := f.s.GetGrid(ctx, &pb.GetGridRequest{GridId: "12"})
	wantCode(t, "a row id", err, codes.InvalidArgument)
}

// A symlink is a link: to a file a link tile naming the file's path, to a
// directory a door onto its grid drawn as a link, and to nothing or to
// outside every root a link that reads dead.
func TestASymlinkIsALink(t *testing.T) {
	f := newFixture(t)
	f.write("real.md", "# real")
	f.write("dir/inner.txt", "inner")
	outside := filepath.Join(f.base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, dest := range map[string]string{"file": "real.md", "dirlink": "dir", "cycle": ".", "broken": "missing.md",
		"out": "../outside/secret.txt", "outdir": "../outside"} {
		if err := os.Symlink(dest, f.path(name)); err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	resp := f.grid(f.root)
	if tl := tileIn(t, resp, f.path("file")); tl.LinkTargetId != idOf(f.path("real.md")) || tl.Kind != rpc.KindText {
		t.Errorf("file link = %+v", tl)
	}
	for name, child := range map[string]string{"dirlink": f.path("dir"), "cycle": f.root} {
		if tl := tileIn(t, resp, f.path(name)); tl.Kind != rpc.KindDoor || tl.ChildGridId != idOf(child) || !tl.Link {
			t.Errorf("%s = kind %q child %q link %v", name, tl.Kind, tl.ChildGridId, tl.Link)
		}
	}
	ctx := context.Background()
	for name, target := range map[string]string{"broken": f.path("missing.md"), "out": filepath.Join(outside, "secret.txt"), "outdir": outside} {
		tl := tileIn(t, resp, f.path(name))
		if tl.LinkTargetId != idOf(target) {
			t.Errorf("%s targets %q, want %s", name, tl.LinkTargetId, target)
			continue
		}
		if _, err := f.s.GetTile(ctx, &pb.GetTileRequest{TileId: tl.LinkTargetId}); !gwerr.IsDeadLink(err) {
			t.Errorf("%s's target reads %v, want dead", name, err)
		}
	}
	var got []byte
	err := f.s.ReadContent(ctx, &pb.ReadContentRequest{TileId: idOf(f.path("out"))}, func(c *pb.ContentChunk) error {
		got = append(got, c.Data...)
		return nil
	})
	wantCode(t, "a link out of every root", err, codes.PermissionDenied)
	if len(got) != 0 {
		t.Errorf("a link out of every root read %q", got)
	}
}

// A root reached through a symlink is served under the path files: names.
func TestARootBehindASymlinkServes(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "a")
	link := filepath.Join(f.base, "rootlink")
	if err := os.Symlink(f.root, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	s, err := New(f.st.Namespace("~"), []Root{{Path: link}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, err := s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(link)})
	if err != nil {
		t.Fatal(err)
	}
	if tileIn(t, resp, filepath.Join(link, "a.txt")) == nil {
		t.Fatal("no tile")
	}
	info, err := s.Info(context.Background(), &pb.InfoRequest{})
	if err != nil || len(info.PluginGrids) != 1 || info.PluginGrids[0].Label != "rootlink" {
		t.Fatalf("Info = %v, %v", info, err)
	}
}

// Probe reads the disk.
func TestProbeReadsTheDisk(t *testing.T) {
	f := newFixture(t)
	there := f.write("a.txt", "a")
	ctx := context.Background()
	for p, want := range map[string]pb.ProbeResponse_Presence{
		there: pb.ProbeResponse_PRESENCE_PRESENT, f.path("b.txt"): pb.ProbeResponse_PRESENCE_GONE,
		filepath.Join(f.base, "x"): pb.ProbeResponse_PRESENCE_GONE,
	} {
		resp, err := f.s.Probe(ctx, &pb.ProbeRequest{TileId: idOf(p)})
		if err != nil || resp.Presence != want {
			t.Errorf("Probe(%s) = %v, %v; want %v", p, resp.GetPresence(), err, want)
		}
	}
}

// A root that will not read is dark: its rows answer, the health event names
// it, and it comes back by itself.
func TestARootThatWillNotReadIsDark(t *testing.T) {
	f := newFixture(t)
	ev := f.events()
	a := f.write("a.txt", "a")
	at := tileIn(t, f.grid(f.root), a)
	if _, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: at.Id, X: 3, Y: 3, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	moved := f.root + ".away"
	if err := os.Rename(f.root, moved); err != nil {
		t.Fatal(err)
	}
	resp := f.grid(f.root)
	if got := tileIn(t, resp, a); got.X != 3 {
		t.Errorf("the dark root's row = %+v", got)
	}
	h := await(t, ev, "dark", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth()
	if h.Healthy || h.Detail == "" || !slices.Contains([]bool{true}, h.GetTold()) {
		t.Errorf("health = %+v", h)
	}
	if _, err := f.s.GetTile(context.Background(), &pb.GetTileRequest{TileId: idOf(f.path("b.txt"))}); grpcCode(err) != codes.Unavailable {
		t.Errorf("a file the dark root holds no row for = %v, want Unavailable", err)
	}
	if err := os.Rename(moved, f.root); err != nil {
		t.Fatal(err)
	}
	f.grid(f.root)
	if h := await(t, ev, "light", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth(); !h.Healthy {
		t.Errorf("health = %+v", h)
	}
	if got := tileIn(t, f.grid(f.root), a); got.X != 3 {
		t.Errorf("the row after the root came back = %+v", got)
	}
}

// A directory under a root that will not read is dark too.
func TestADirectoryThatWillNotReadIsDark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	locked := f.mkdir("locked")
	f.write("locked/a.txt", "a")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if resp := f.grid(locked); len(resp.Tiles) != 0 {
		t.Errorf("tiles = %v", resp.Tiles)
	}
	if h := f.s.health(); !h.dark || h.detail == "" {
		t.Errorf("health = %+v", h)
	}
}

// A dark directory that is then gone is dead, and no longer dark.
func TestADarkDirectoryThatGoesIsNoLongerDark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	locked := f.mkdir("locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	f.grid(locked)
	if !f.s.health().dark {
		t.Fatal("not dark")
	}
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(locked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(locked)}); !gwerr.IsDeadLink(err) {
		t.Fatalf("GetGrid = %v, want dead", err)
	}
	if h := f.s.health(); h.dark {
		t.Errorf("still dark: %+v", h)
	}
}

// A root inside another is a door in the outer one like any directory: it is
// read, moved, framed and selected there. Being config, it is never renamed
// away nor deleted.
func TestARootInsideARootIsADoorThere(t *testing.T) {
	f := newFixture(t)
	inner := f.mkdir("inner")
	f.write("inner/a.txt", "a")
	other := f.mkdir("other")
	s, err := New(f.st.Namespace("~"), []Root{{Path: f.root}, {Path: inner}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	g, err := s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.root)})
	if err != nil {
		t.Fatal(err)
	}
	door := tileIn(t, g, inner)
	if got, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: door.Id}); err != nil || got.Tile.ChildGridId != idOf(inner) {
		t.Fatalf("GetTile(inner) = %v, %v", got, err)
	}
	moved, err := s.MoveTile(ctx, &pb.MoveTileRequest{TileId: door.Id, X: 6, Y: 6, W: 1, H: 1, Claim: rpc.PlacementOf(door)})
	if err != nil || moved.Tile.X != 6 {
		t.Fatalf("MoveTile(inner) = %v, %v", moved, err)
	}
	if _, err := s.SetFraming(ctx, &pb.SetFramingRequest{TileId: door.Id, Cx: 1, Cy: 1, Zoom: 1}); err != nil {
		t.Errorf("SetFraming(inner) = %v", err)
	}
	if _, err := s.SetSelection(ctx, &pb.SetSelectionRequest{GridId: idOf(f.root), Add: []string{door.Id}}); err != nil {
		t.Errorf("SetSelection(inner) = %v", err)
	}
	if g, _ := s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.root)}); !slices.Equal(g.Grid.Selection, []string{door.Id}) {
		t.Errorf("selection = %v", g.Grid.Selection)
	}
	_, err = s.MoveTile(ctx, &pb.MoveTileRequest{TileId: door.Id, GridId: idOf(other), X: 0, Y: 0, W: 1, H: 1, Claim: rpc.PlacementOf(moved.Tile)})
	wantCode(t, "a root renamed away", err, codes.InvalidArgument)
	_, err = s.DeleteTile(ctx, rpc.DeleteOf(moved.Tile))
	wantCode(t, "a root deleted", err, codes.InvalidArgument)
	if _, err := os.Stat(filepath.Join(inner, "a.txt")); err != nil {
		t.Errorf("the inner root moved: %v", err)
	}
	if _, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: idOf(f.root)}); !gwerr.IsDeadLink(err) {
		t.Errorf("the outer root as a tile = %v, want dead", err)
	}
}

// A root is an absolute path, since every id the files answer is one.
func TestARelativeRootIsRefused(t *testing.T) {
	f := newFixture(t)
	if s, err := New(f.st.Namespace("~"), []Root{{Path: f.root}, {Path: "notes"}}, nil); err == nil {
		s.Close()
		t.Fatal("a relative root was taken")
	}
}
