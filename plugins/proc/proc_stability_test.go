package proc_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/proc"
)

// childTileFor finds the well tile whose label (AltText) is the child pid.
func childTileFor(t *testing.T, tiles []*gridwellv1.Tile, childPID int64) *gridwellv1.Tile {
	t.Helper()
	key := strconv.FormatInt(childPID, 10)
	for _, tile := range tiles {
		if tile.AltText == key {
			return tile
		}
	}
	t.Fatalf("child tile %q not found in %+v", key, tiles)
	return nil
}

// TestChildGridStableAcrossReopen: a process's grid id is a stable handle keyed
// by PID. Re-opening the same DB and re-listing the same parent yields the same
// child-process grid id — so a saved descent into a process subtree keeps
// resolving as long as that PID is present. The proc analogue of fs's path
// stability and the primary rule for source-backed plugins.
func TestChildGridStableAcrossReopen(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	dbPath := filepath.Join(t.TempDir(), "proc.db")

	p, err := proc.Open(dbPath, root, &recordKiller{})
	if err != nil {
		t.Fatal(err)
	}
	att, err := attachAt(p, parentPID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	child := childTileFor(t, r.Tiles, childPID)
	if child.ChildGridId == "" {
		t.Fatal("child process well has no child grid id")
	}
	childGrid := child.ChildGridId
	p.Close()

	p2, err := proc.Open(dbPath, root, &recordKiller{})
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	att2, err := attachAt(p2, parentPID)
	if err != nil {
		t.Fatal(err)
	}
	if att2.RootGridId != att.RootGridId {
		t.Errorf("root grid id changed across reopen: %s -> %s", att.RootGridId, att2.RootGridId)
	}
	r2, err := p2.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att2.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	child2 := childTileFor(t, r2.Tiles, childPID)
	if child2.ChildGridId != childGrid {
		t.Errorf("child grid id changed across reopen: %s -> %s", childGrid, child2.ChildGridId)
	}
}
