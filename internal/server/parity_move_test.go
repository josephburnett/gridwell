package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/events"
)

// wantMoves fails unless grid carries the stamp the client's left-drag
// verdict reads (dragdrop.MoveForbidden).
func (f *parityFix) wantMoves(grid string, want bool) {
	f.t.Helper()
	g, err := f.cl.GetGrid(f.ctx, grid)
	if err != nil {
		f.t.Fatal(err)
	}
	if g.Grid.Moves == nil || g.Grid.GetMoves() != want {
		f.t.Fatalf("%s: moves = %v, want %v", grid, g.Grid.Moves, want)
	}
}

// A file moved to another directory is renamed there, here and through a
// connection: the row keeps what the node holds on it under the new path's
// id, the old id reads dead, every open view of either directory is told,
// and a move on to another source is refused with the reason.
func TestAFileMovesBetweenDirectories(t *testing.T) {
	f := newParityFix(t)
	f.wantMoves(f.away[onHome], true)
	f.wantMoves(f.away[onFar], true)
	for _, prefix := range []string{"", parityConn} {
		d := f.disk[prefix]
		main, away := f.fileID(prefix, d.main), f.fileID(prefix, d.away)
		f.wantMoves(main, true)
		_, tl, err := f.body(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.cl.SetContentZoom(f.ctx, tl.Id, 1.5); err != nil {
			t.Fatal(err)
		}
		f.settle(onFiles)
		f.settle(onFar)
		for len(f.events) > 0 {
			<-f.events
		}
		moved, err := f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: tl.Id, GridId: away, X: 3, Y: 3, W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
		if err != nil {
			t.Fatalf("%q: move: %v", prefix, err)
		}
		landed := filepath.Join(d.away, "Body.md")
		if moved.Id != f.fileID(prefix, landed) || moved.GridId != away || moved.X != 3 || moved.Y != 3 || moved.ContentZoom != 1.5 {
			t.Fatalf("%q: moved to %s on %s at %d,%d zoom %v; want %s at 3,3 keeping zoom 1.5", prefix, moved.Id, moved.GridId, moved.X, moved.Y, moved.ContentZoom, landed)
		}
		if _, err := os.Stat(landed); err != nil {
			t.Fatalf("%q: the file is not at %s: %v", prefix, landed, err)
		}
		if _, err := f.cl.GetTile(f.ctx, tl.Id); clientsync.Of(err) != clientsync.OutcomeDead {
			t.Fatalf("%q: the old path reads %v, want dead", prefix, err)
		}
		pending := map[string]bool{main: true, away: true}
		for deadline := time.After(10 * time.Second); len(pending) > 0; {
			select {
			case ev := <-f.events:
				delete(pending, events.Route(ev).Fetch)
			case <-deadline:
				t.Fatalf("%q: no view was told of %v", prefix, pending)
			}
		}
		_, err = f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: moved.Id, GridId: f.root[onHome], X: 3, Y: 3, W: 1, H: 1, Claim: rpc.PlacementOf(moved)})
		if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(err.Error(), "a move to another source is refused") {
			t.Fatalf("%q: a move to home = %v, want refused with the reason", prefix, err)
		}
	}
}
