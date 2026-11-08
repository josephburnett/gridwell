package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func (f *fixture) find(req *pb.FindRequest) []string {
	f.t.Helper()
	resp, err := f.s.Find(context.Background(), req)
	if err != nil {
		f.t.Fatalf("Find(%+v): %v", req, err)
	}
	var out []string
	for _, r := range resp.Results {
		out = append(out, r.Tile.AltText)
	}
	return out
}

// Find on the files: id: locates on its chain of doors, an empty query with
// a filter lists, and a text query answers empty.
func TestFindLocatesAndFilters(t *testing.T) {
	f := newFixture(t)
	dir := f.mkdir("dir")
	deep := f.write("dir/deep.md", "x")
	f.write("top.png", "x")
	f.write("top.md", "needle")
	ctx := context.Background()
	if got := f.find(&pb.FindRequest{Query: "needle"}); len(got) != 0 {
		t.Errorf("a text query found %v", got)
	}
	if _, err := f.s.SetFraming(ctx, &pb.SetFramingRequest{TileId: idOf(dir), Cx: 0, Cy: 0, Zoom: 1}); err != nil {
		t.Fatal(err)
	}
	f.grid(dir)
	if _, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(deep), X: 4, Y: 0, W: 1, H: 1, Claim: rpc.PlacementOf(f.tile(deep))}); err != nil {
		t.Fatal(err)
	}
	resp, err := f.s.Find(ctx, &pb.FindRequest{Query: "id:" + idOf(deep)})
	if err != nil || len(resp.Results) != 1 || len(resp.Results[0].Path) != 1 || resp.Results[0].Path[0].Id != idOf(dir) {
		t.Fatalf("locate = %v, %v", resp, err)
	}
	if got := f.find(&pb.FindRequest{Query: "id:" + idOf(f.path("gone"))}); len(got) != 0 {
		t.Errorf("a gone path located: %v", got)
	}
	if got := f.find(&pb.FindRequest{GridId: idOf(f.root), Recurse: true, Kind: rpc.KindText}); len(got) != 2 {
		t.Errorf("text under the root = %v", got)
	}
	if got := f.find(&pb.FindRequest{GridId: idOf(f.root), Kind: rpc.KindURL}); len(got) != 1 || got[0] != "top.png" {
		t.Errorf("urls on the root = %v", got)
	}
	if _, err := f.s.SetSelection(ctx, &pb.SetSelectionRequest{GridId: idOf(dir), Add: []string{idOf(deep)}}); err != nil {
		t.Fatal(err)
	}
	if got := f.find(&pb.FindRequest{Selected: true}); len(got) != 1 || got[0] != "deep.md" {
		t.Errorf("the selected rows = %v", got)
	}
	if got := f.find(&pb.FindRequest{}); len(got) != 0 {
		t.Errorf("an empty query with no filter = %v", got)
	}
}

// A find on a directory that is gone finds nothing, as on one outside every
// root: a filter over no rows.
func TestFindOnAGoneDirectoryIsEmpty(t *testing.T) {
	f := newFixture(t)
	for _, dir := range []string{f.path("gone"), f.base} {
		for _, recurse := range []bool{false, true} {
			if got := f.find(&pb.FindRequest{GridId: idOf(dir), Recurse: recurse, Kind: rpc.KindText}); len(got) != 0 {
				t.Errorf("Find(%s, recurse %v) = %v", dir, recurse, got)
			}
		}
	}
}

// A time filter reads the node's row: a file nobody touched has no row and
// no time, so it passes none, and a touched one passes on its row's times.
// An id: locate a filter fails answers nothing.
func TestFindTimesAreTheRows(t *testing.T) {
	f := newFixture(t)
	touched := f.write("touched.md", "t")
	f.write("untouched.md", "u")
	ctx := context.Background()
	tt := f.tile(touched)
	if _, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: tt.Id, X: 5, Y: 5, W: 1, H: 1, Claim: rpc.PlacementOf(tt)}); err != nil {
		t.Fatal(err)
	}
	since := &pb.TimeRange{From: time.Now().Add(-time.Hour).Unix()}
	for name, req := range map[string]*pb.FindRequest{
		"created":          {GridId: idOf(f.root), Created: since},
		"modified":         {GridId: idOf(f.root), Modified: since},
		"content_changed":  {GridId: idOf(f.root), ContentChanged: since},
		"created anywhere": {Created: since},
	} {
		if got := f.find(req); len(got) != 1 || got[0] != "touched.md" {
			t.Errorf("%s = %v, want the touched file alone", name, got)
		}
	}
	if got := f.find(&pb.FindRequest{Query: "id:" + idOf(touched), Kind: rpc.KindURL}); len(got) != 0 {
		t.Errorf("a locate the kind filter fails = %v", got)
	}
	if got := f.find(&pb.FindRequest{Query: "id:" + idOf(touched), Created: &pb.TimeRange{To: 1}}); len(got) != 0 {
		t.Errorf("a locate the time filter fails = %v", got)
	}
}

// A recursive find goes through a directory's own doors, never a link: a
// symlink to a directory elsewhere opens that directory's grid, which the
// directory does not hold.
func TestARecursiveFindNeverFollowsALink(t *testing.T) {
	f := newFixture(t)
	f.write("a/x.md", "x")
	other := filepath.Join(f.base, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "y.md"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, f.path("link")); err != nil {
		t.Skip("symlinks unsupported")
	}
	s, err := New(f.st.Files(), []Root{{Path: f.root}, {Path: other}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	resp, err := s.Find(context.Background(), &pb.FindRequest{GridId: idOf(f.root), Recurse: true, Kind: rpc.KindText})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		if r.Tile.AltText == "y.md" {
			t.Errorf("a recursive find went through the link to %s", other)
		}
	}
}

// A find's reads say nothing of the files' health, as an abandoned read says
// nothing of a source's: only the view's reads and the watch do.
func TestAFindsReadsSayNothingOfHealth(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	locked := f.mkdir("locked")
	f.write("locked/a.md", "a")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	f.show(f.root, locked)
	if !f.s.health().dark {
		t.Fatal("a shown directory that will not read is not dark")
	}
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := f.find(&pb.FindRequest{GridId: idOf(f.root), Recurse: true, Kind: rpc.KindText}); len(got) != 1 {
		t.Errorf("a find under the root = %v, want the file the directory now shows", got)
	}
	if !f.s.health().dark {
		t.Error("a find's read changed the files' health")
	}
}
