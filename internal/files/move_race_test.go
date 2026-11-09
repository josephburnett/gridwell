package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A move replaces nothing, a file another process makes at the landing name
// after the check included: that move is refused and the file stands.
func TestAMoveNeverReplacesAFileMadeMeanwhile(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.txt", "mine")
	to := f.mkdir("to")
	tl := f.tile(p)
	was := resolved
	t.Cleanup(func() { resolved = was })
	// The second open is the moved file's directory, after the landing name
	// was checked.
	calls := 0
	resolved = func() {
		if calls++; calls == 2 {
			f.write("to/a.txt", "theirs")
		}
	}
	_, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: tl.Id, GridId: idOf(to), W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
	if err == nil {
		t.Error("a move over a file made meanwhile landed")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "a.txt")); string(b) != "theirs" {
		t.Errorf("the file made meanwhile holds %q, want theirs", b)
	}
	if b, _ := os.ReadFile(p); string(b) != "mine" {
		t.Errorf("the moved file holds %q, want it where it was", b)
	}
}
