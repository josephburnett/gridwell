package files

import (
	"context"
	"testing"

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
	if _, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(deep), X: 4, Y: 0, W: 1, H: 1}); err != nil {
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
