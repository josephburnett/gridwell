package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/josephburnett/gridwell/internal/procsource"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// stubHostActor records calls — tests assert against it instead of
// touching the real host filesystem or process table.
type stubHostActor struct {
	mu         sync.Mutex
	Removed    []string
	RemovedAll []string
	Killed     []killCall
	ReturnErr  error
}

type killCall struct {
	PID int64
	Sig syscall.Signal
}

func (s *stubHostActor) Remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Removed = append(s.Removed, path)
	return s.ReturnErr
}
func (s *stubHostActor) RemoveAll(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RemovedAll = append(s.RemovedAll, path)
	return s.ReturnErr
}
func (s *stubHostActor) Kill(pid int64, sig syscall.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Killed = append(s.Killed, killCall{PID: pid, Sig: sig})
	return s.ReturnErr
}

func TestDeleteFSFileTileRemovesFile(t *testing.T) {
	s := newTestStore(t)
	host := &stubHostActor{}
	s.SetHostActor(host)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "doomed.md"), "bye")
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	var fileTile *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].SourceKey == "doomed.md" {
			fileTile = &g.Tiles[i]
		}
	}
	if fileTile == nil {
		t.Fatal("doomed.md tile not found after reconcile")
	}
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: descentPath, TileID: fileTile.ID, Version: fileTile.Version,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := filepath.Join(dir, "doomed.md")
	if len(host.Removed) != 1 || host.Removed[0] != want {
		t.Errorf("Remove calls = %v, want [%s]", host.Removed, want)
	}
	if len(host.RemovedAll) != 0 {
		t.Errorf("file-tile delete should not call RemoveAll, got %v", host.RemovedAll)
	}
}

func TestDeleteFSSubDirTileRemovesAll(t *testing.T) {
	s := newTestStore(t)
	host := &stubHostActor{}
	s.SetHostActor(host)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "stuff"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	var subWell *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].SourceKey == "stuff" {
			subWell = &g.Tiles[i]
		}
	}
	if subWell == nil {
		t.Fatal("subdir tile not found")
	}
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: descentPath, TileID: subWell.ID, Version: subWell.Version,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := filepath.Join(dir, "stuff")
	if len(host.RemovedAll) != 1 || host.RemovedAll[0] != want {
		t.Errorf("RemoveAll calls = %v, want [%s]", host.RemovedAll, want)
	}
}

func TestDeleteFSFileTileFailureKeepsRow(t *testing.T) {
	s := newTestStore(t)
	host := &stubHostActor{ReturnErr: os.ErrPermission}
	s.SetHostActor(host)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "doomed"), "stay")
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	tile := g.Tiles[0]
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: descentPath, TileID: tile.ID, Version: tile.Version,
	}); err == nil {
		t.Fatal("expected error from failed rm")
	}
	g2, _ := s.GetGrid(ctx, w.ChildGridID)
	if len(g2.Tiles) != 1 {
		t.Errorf("tile row should survive failed rm, got %d tiles", len(g2.Tiles))
	}
}

func TestDeleteProcessTileSendsSIGTERM(t *testing.T) {
	s := newTestStore(t)
	host := &stubHostActor{}
	s.SetHostActor(host)
	s.SetSourceReaders(nil, &stubProcReader{
		children: map[int64][]procsource.Info{
			1: {{PID: 42, PPID: 1, Name: "victim"}},
		},
		self: map[int64]procsource.Info{
			1: {PID: 1, PPID: 0, Name: "init"},
		},
	}, "/proc")
	root := rootID(t, s)
	ctx := context.Background()
	w, _ := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 1,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	var victim *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].PID == 42 {
			victim = &g.Tiles[i]
		}
	}
	if victim == nil {
		t.Fatal("victim tile not found")
	}
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: descentPath, TileID: victim.ID, Version: victim.Version,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(host.Killed) != 1 || host.Killed[0].PID != 42 || host.Killed[0].Sig != syscall.SIGTERM {
		t.Errorf("Kill calls = %+v, want one SIGTERM to 42", host.Killed)
	}
	// Tile row should still be there — the process may not have died.
	// On the next reconcile (if the process did die) it would clear.
	if len(host.Removed) != 0 || len(host.RemovedAll) != 0 {
		t.Errorf("process delete must not touch the filesystem, got Remove=%v RemoveAll=%v",
			host.Removed, host.RemovedAll)
	}
}

func TestMoveCrossSourceGridRejected(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f"), "x")
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	if len(g.Tiles) == 0 {
		t.Fatal("expected reconciled tile")
	}
	fileTile := g.Tiles[0]

	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	_, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path:    descentPath,
		TileID:  fileTile.ID,
		Version: fileTile.Version,
		// Move into the root grid (regular) — should be rejected.
		DestGridID: root,
		DestPath:   rpc.Path{},
		X:          5, Y: 5,
	})
	// Must be rejected by the source-grid guard specifically — not by some
	// incidental error. With source grids now in the cache file, this proves
	// gridSourceKinds correctly identifies the cross-file source grid as 'fs'.
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("cross-grid move from fs-grid: err = %v, want ErrInvalidArgument", err)
	}
}

func TestCloneIntoSourceGridRejected(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	// Place a regular text tile in the root grid we'll try to clone in.
	src, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 3, Y: 0, W: 1, H: 1, Data: []byte("hi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	_, err = s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path:    rpc.Path{},
		TileID:  src.ID,
		Version: src.Version,
		// Destination is the fs-grid — that should be refused.
		DestGridID: w.ChildGridID,
		DestPath:   descentPath,
		X:          0, Y: 7,
	})
	if err == nil {
		t.Fatal("expected clone-into-source-grid to be rejected")
	}
}

// TestCloneFileWellOutOfSourceGridAllowed checks the link case: cloning
// a sub-file-well from an fs-grid into a regular grid is the "link into
// Gridwell" gesture and should succeed.
func TestCloneFileWellOutOfSourceGridAllowed(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	var subWell *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].SourceKey == "sub" {
			subWell = &g.Tiles[i]
		}
	}
	if subWell == nil {
		t.Fatal("sub tile not found")
	}
	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	if _, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path:    descentPath,
		TileID:  subWell.ID,
		Version: subWell.Version,
		// Land in the root grid as a linked tile.
		DestGridID: root,
		DestPath:   rpc.Path{},
		X:          5, Y: 5,
	}); err != nil {
		t.Fatalf("clone sub-file-well into regular grid: %v", err)
	}
	// The cloned tile is a red-outlined file-well at the same fs path,
	// pointing at the same backing fs-grid (shared by path identity).
	rg, _ := s.GetGrid(ctx, root)
	var link *rpc.Tile
	for i := range rg.Tiles {
		if rg.Tiles[i].FSPath == subWell.FSPath && rg.Tiles[i].ID != w.ID {
			link = &rg.Tiles[i]
		}
	}
	if link == nil {
		t.Fatal("linked file-well not found in root grid")
	}
	if link.ChildGridID != subWell.ChildGridID {
		t.Errorf("linked tile child grid = %d, want shared %d",
			link.ChildGridID, subWell.ChildGridID)
	}
}

// TestCloneFileTileOutOfSourceGridPreservesFSName covers the link case
// for file (text) tiles inside an fs-grid. The clone must land as a
// red-outlined reference in the destination grid — preserving source_key
// is what lets the client render the basename label and the exit
// border so the link "still feels outside Gridwell."
func TestCloneFileTileOutOfSourceGridPreservesFSName(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	var fileTile *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].SourceKey == "notes.md" {
			fileTile = &g.Tiles[i]
		}
	}
	if fileTile == nil {
		t.Fatal("notes.md tile not found in fs-grid")
	}

	descentPath := rpc.Path{WellIDs: []int64{w.ID}}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path:       descentPath,
		TileID:     fileTile.ID,
		Version:    fileTile.Version,
		DestGridID: root,
		DestPath:   rpc.Path{},
		X:          5, Y: 5,
	})
	if err != nil {
		t.Fatalf("clone file tile out of fs-grid: %v", err)
	}
	if clone.Kind != rpc.KindText {
		t.Errorf("clone kind = %q, want %q", clone.Kind, rpc.KindText)
	}
	if clone.SourceKey != "notes.md" {
		t.Errorf("clone source_key = %q, want %q — client labeling depends on it",
			clone.SourceKey, "notes.md")
	}
}

func TestSourceDeletePath(t *testing.T) {
	parent := &rpc.Grid{SourceKind: rpc.GridSourceFS, SourceID: "/etc"}
	cases := []struct {
		name string
		tile rpc.Tile
		want string
	}{
		{
			name: "sub-file-well",
			tile: rpc.Tile{Kind: rpc.KindFileWell, FSPath: "/etc/passwd.d", SourceKey: "passwd.d"},
			want: "/etc/passwd.d",
		},
		{
			name: "file tile",
			tile: rpc.Tile{Kind: rpc.KindText, SourceKey: "motd"},
			want: "/etc/motd",
		},
		{
			name: "unrelated text tile (no source_key)",
			tile: rpc.Tile{Kind: rpc.KindText},
			want: "",
		},
	}
	for _, c := range cases {
		got := sourceDeletePath(parent, &c.tile)
		if got != c.want {
			t.Errorf("%s: sourceDeletePath = %q, want %q", c.name, got, c.want)
		}
	}
}
