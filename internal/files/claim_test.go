package files

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// collect is what the stream delivers in the next short window, health
// events left out.
func collect(ch <-chan *pb.Event) []*pb.Event {
	var out []*pb.Event
	deadline := time.After(3 * debounceWindow)
	for {
		select {
		case ev := <-ch:
			if ev.GetPluginHealth() == nil {
				out = append(out, ev)
			}
		case <-deadline:
			return out
		}
	}
}

// A file's row claims what the writer saw, as home's does: a move claims the
// placement, a delete the placement and version, a freeze the freeze; a
// stale claim is the conflict and the row stands.
func TestAFilesRowClaimsWhatTheWriterSaw(t *testing.T) {
	f := newFixture(t)
	a := f.write("a.txt", "a")
	pdf := f.write("doc.pdf", "%PDF-1.4\n")
	ctx := context.Background()
	at := f.tile(a)
	seen := rpc.PlacementOf(at)
	if _, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: at.Id, X: 9, Y: 9, W: 1, H: 1, Claim: seen}); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: at.Id, X: 11, Y: 9, W: 1, H: 1, Claim: seen})
	wantCode(t, "a stale move", err, codes.FailedPrecondition)
	_, err = f.s.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: at.Id, Claim: seen})
	wantCode(t, "a delete over a move it never saw", err, codes.FailedPrecondition)
	if got := f.tile(a); got.X != 9 || got.Version != 0 {
		t.Errorf("row = x %d version %d, want the first move's 9 at version 0", got.X, got.Version)
	}
	if len(f.trashed) != 0 {
		t.Errorf("a refused delete trashed %v", f.trashed)
	}

	no, yes := false, true
	if _, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: idOf(pdf), UrlFrozen: &yes, ClaimFrozen: &no}); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if _, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: idOf(pdf), UrlFrozen: &no, ClaimFrozen: &yes}); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	_, err = f.s.SetTile(ctx, &pb.SetTileRequest{TileId: idOf(pdf), UrlFrozen: &yes, ClaimFrozen: &yes})
	wantCode(t, "a freeze over an unfreeze it never saw", err, codes.FailedPrecondition)
	if got := f.tile(pdf); got.UrlFrozen || got.Version != 0 {
		t.Errorf("frozen %v version %d, want the unfreeze standing at version 0", got.UrlFrozen, got.Version)
	}
}

// A page file and a directory are renamed on the row, claiming and bumping
// the version, and keep the typed name across a listing; a stale rename is
// the conflict.
func TestARenameIsTheRowsTypedName(t *testing.T) {
	f := newFixture(t)
	pdf := f.write("doc.pdf", "%PDF-1.4\n")
	dir := f.mkdir("dir")
	ctx := context.Background()
	for _, p := range []string{pdf, dir} {
		row := f.tile(p)
		resp, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: row.Id, Version: row.Version, Rename: "mine"})
		if err != nil {
			t.Fatalf("rename %s: %v", p, err)
		}
		if resp.Tile.AltText != "mine" || resp.Tile.Version != row.Version+1 || resp.Tile.Id != row.Id {
			t.Errorf("rename %s answered %q v%d as %q", p, resp.Tile.AltText, resp.Tile.Version, resp.Tile.Id)
		}
		_, err = f.s.SetTile(ctx, &pb.SetTileRequest{TileId: row.Id, Version: row.Version, Rename: "stale"})
		wantCode(t, "a stale rename", err, codes.FailedPrecondition)
		if got := tileIn(t, f.grid(f.root), p); got.AltText != "mine" {
			t.Errorf("%s listed as %q, want the typed name", p, got.AltText)
		}
	}
}

// A text file's scroll is framing: it lands on the row, survives a listing
// and bumps no version.
func TestAScrollBumpsNoVersion(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.md", "# a")
	before := f.tile(p)
	if _, err := f.s.SetTile(context.Background(), &pb.SetTileRequest{TileId: before.Id,
		Tile: &pb.Tile{Kind: rpc.KindText, TextX: 12, TextY: 340, TextMode: rpc.TextModeRendered}}); err != nil {
		t.Fatal(err)
	}
	got := tileIn(t, f.grid(f.root), p)
	if got.TextX != 12 || got.TextY != 340 || got.TextMode != rpc.TextModeRendered || got.Version != before.Version {
		t.Errorf("after the scroll = %+v, version was %d", got, before.Version)
	}
}

// Every SetTile arm is a fact on one row that moves no listing, so it
// announces exactly that tile, as the write answered it, and never its
// directory; a write landing where the row already is announces nothing.
// A placement announces its directory, and framing announces the framing.
func TestEachWriteAnnouncesItsOwnFactOnce(t *testing.T) {
	f := newFixture(t)
	text := f.write("a.md", "# a")
	pdf := f.write("doc.pdf", "%PDF-1.4\n")
	dir := f.mkdir("dir")
	ctx := context.Background()
	ev := f.events()
	zoom, frozen := 1.5, true
	setTile := func(req *pb.SetTileRequest) func() (*pb.Tile, error) {
		return func() (*pb.Tile, error) {
			resp, err := f.s.SetTile(ctx, req)
			return resp.GetTile(), err
		}
	}
	tileEvent := func(path string) func(*pb.Event, *pb.Tile) bool {
		return func(e *pb.Event, answer *pb.Tile) bool {
			got := e.GetTileChanged().GetTile()
			return got.GetId() == idOf(path) && got.GetGridId() == idOf(f.root) && proto.Equal(got, answer)
		}
	}
	for _, tc := range []struct {
		name  string
		write func() (*pb.Tile, error)
		want  func(*pb.Event, *pb.Tile) bool
	}{
		{"content zoom", setTile(&pb.SetTileRequest{TileId: idOf(text), ContentZoom: &zoom}), tileEvent(text)},
		{"text scroll", setTile(&pb.SetTileRequest{TileId: idOf(text),
			Tile: &pb.Tile{Kind: rpc.KindText, TextX: 1, TextY: 2, TextMode: rpc.TextModeRendered}}), tileEvent(text)},
		{"url frozen", setTile(&pb.SetTileRequest{TileId: idOf(pdf), UrlFrozen: &frozen}), tileEvent(pdf)},
		{"url screenshot", setTile(&pb.SetTileRequest{TileId: idOf(pdf),
			Tile: &pb.Tile{Kind: rpc.KindURL}, Preview: []byte("\xff\xd8screenshot")}), tileEvent(pdf)},
		{"placement", func() (*pb.Tile, error) {
			resp, err := f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: idOf(text), X: 7, Y: 7, W: 1, H: 1})
			return resp.GetTile(), err
		}, func(e *pb.Event, _ *pb.Tile) bool { return gridChanged(f.root)(e) }},
		{"root framing", func() (*pb.Tile, error) {
			_, err := f.s.SetFraming(ctx, &pb.SetFramingRequest{RootGridId: idOf(f.root), Cx: 3.5, Cy: -2.25, Zoom: 1.75})
			return nil, err
		}, func(e *pb.Event, _ *pb.Tile) bool { return e.GetGridFramingChanged().GetGridId() == idOf(f.root) }},
		{"door framing", func() (*pb.Tile, error) {
			resp, err := f.s.SetFraming(ctx, &pb.SetFramingRequest{TileId: idOf(dir), Cx: 1, Cy: 2, Zoom: 0.5})
			return resp.GetTile(), err
		}, tileEvent(dir)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := tc.write()
			if err != nil {
				t.Fatalf("first: %v", err)
			}
			if evs := collect(ev); len(evs) != 1 || !tc.want(evs[0], answer) {
				t.Fatalf("the first write announced %v, want one event of its own fact", evs)
			}
			if _, err := tc.write(); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if evs := collect(ev); len(evs) != 0 {
				t.Errorf("the repeat announced %v, want nothing", evs)
			}
		})
	}
}

// Each root keeps its own framing, and Info answers it on that root alone.
func TestEachRootKeepsItsOwnFraming(t *testing.T) {
	f := newFixture(t)
	other := f.mkdir("../other")
	s, err := New(f.st.Namespace("~"), []Root{{Path: f.root}, {Path: other}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.SetFraming(ctx, &pb.SetFramingRequest{RootGridId: idOf(other), Cx: 3, Cy: 4, Zoom: 2}); err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(ctx, &pb.InfoRequest{})
	if err != nil || len(info.PluginGrids) != 2 {
		t.Fatalf("Info = %v, %v", info, err)
	}
	if g := info.PluginGrids[0]; g.ViewCx != 0 || g.ViewZoom != 0 {
		t.Errorf("the unframed root = %+v", g)
	}
	if g := info.PluginGrids[1]; g.ViewCx != 3 || g.ViewCy != 4 || g.ViewZoom != 2 {
		t.Errorf("the framed root = %+v", g)
	}
}

// A store Info cannot read is an error, never a root reopening at no
// framing with nothing said.
func TestAnUnreadableStoreFailsInfo(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.Info(context.Background(), &pb.InfoRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Info(context.Background(), &pb.InfoRequest{}); err == nil {
		t.Fatal("Info over a closed store answered")
	}
}
