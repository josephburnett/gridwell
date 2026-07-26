package proc_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/proc"
	"github.com/josephburnett/gridwell/internal/procsource"
)

// recordKiller records signals without sending them to real processes.
type recordKiller struct {
	killed []struct {
		pid int64
		sig syscall.Signal
	}
}

func (r *recordKiller) Kill(pid int64, sig syscall.Signal) error {
	r.killed = append(r.killed, struct {
		pid int64
		sig syscall.Signal
	}{pid, sig})
	return nil
}

// stubProcRoot creates a minimal fake /proc tree with one child process.
func stubProcRoot(t *testing.T) (root string, parentPID, childPID int64) {
	t.Helper()
	root = t.TempDir()
	parentPID = 100
	childPID = 200

	// Create parent process directory.
	parentDir := filepath.Join(root, strconv.FormatInt(parentPID, 10))
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStatus(t, parentDir, parentPID, parentPID, "parent-proc")

	// Create child process directory.
	childDir := filepath.Join(root, strconv.FormatInt(childPID, 10))
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStatus(t, childDir, childPID, parentPID, "child-proc")

	return root, parentPID, childPID
}

func writeStatus(t *testing.T, dir string, pid, ppid int64, name string) {
	t.Helper()
	content := "Name:\t" + name + "\nPid:\t" + strconv.FormatInt(pid, 10) + "\nPPid:\t" + strconv.FormatInt(ppid, 10) + "\nState:\tS (sleeping)\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openPlugin(t *testing.T, procRoot string, killer proc.Killer) *proc.Plugin {
	t.Helper()
	p, err := proc.Open(":memory:", procRoot, killer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestOpen_InMemory(t *testing.T) {
	p := openPlugin(t, "", nil)
	if p == nil {
		t.Fatal("expected non-nil plugin")
	}
}

func TestInfo(t *testing.T) {
	p := openPlugin(t, "", nil)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "proc" {
		t.Errorf("Kind = %q, want %q", resp.Kind, "proc")
	}
}

// attachAt sets the plugin's configured root pid and reads Info — the whole
// handshake (there is no Attach). Info carries the default root grid id and a
// label ("processes" for pid 1, "pid N" otherwise).
func attachAt(p *proc.Plugin, pid int64) (*gridwellv1.InfoResponse, error) {
	p.SetRootPID(pid)
	return p.Info(context.Background(), &gridwellv1.InfoRequest{})
}

func TestInfo_DefaultPID(t *testing.T) {
	root, _, _ := stubProcRoot(t)
	p := openPlugin(t, root, nil)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
	if resp.DisplayName != "processes" {
		t.Errorf("DisplayName = %q, want %q", resp.DisplayName, "processes")
	}
}

func TestInfo_SpecificPID(t *testing.T) {
	root, parentPID, _ := stubProcRoot(t)
	p := openPlugin(t, root, nil)
	resp, err := attachAt(p, parentPID)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
	if resp.DisplayName == "processes" {
		t.Errorf("DisplayName should not be 'processes' for non-1 pid: %q", resp.DisplayName)
	}
}

func TestGetGrid_ListsChildren(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	p := openPlugin(t, root, nil)

	ar, _ := attachAt(p, parentPID)
	resp, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}

	byKey := map[string]*gridwellv1.Tile{}
	for _, t2 := range resp.Tiles {
		byKey[t2.AltText] = t2
	}

	// Should have @info + child process tile.
	if _, ok := byKey["@info"]; !ok {
		t.Error("missing @info tile")
	}
	childKey := strconv.FormatInt(childPID, 10)
	if _, ok := byKey[childKey]; !ok {
		t.Errorf("missing child PID tile (key=%s), got: %v", childKey, byKey)
	}
}

// TestGetTile_ReturnsRow (issue #171): same class as fs — cloneAcrossPlugins
// calls GetTile on the SOURCE plugin first; proc never implemented it, so a
// right-drag of a process well into another plugin failed "not implemented".
func TestGetTile_ReturnsRow(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	p := openPlugin(t, root, nil)
	ar, _ := attachAt(p, parentPID)
	resp, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	childKey := strconv.FormatInt(childPID, 10)
	var child *gridwellv1.Tile
	for _, t2 := range resp.Tiles {
		if t2.AltText == childKey {
			child = t2
		}
	}
	if child == nil {
		t.Fatal("child PID tile missing")
	}
	got, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: child.Id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if got.Tile.Id != child.Id || got.Tile.Kind != child.Kind || got.Tile.AltText != childKey {
		t.Errorf("GetTile = %+v, want the GetGrid row %+v", got.Tile, child)
	}
	if _, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: "999999"}); err == nil {
		t.Error("GetTile for a missing id must error, not fabricate a tile")
	}
}

func TestGetGrid_StableIDs(t *testing.T) {
	root, parentPID, _ := stubProcRoot(t)
	p := openPlugin(t, root, nil)

	ar, _ := attachAt(p, parentPID)

	r1, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})
	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})

	ids1 := map[string]string{}
	for _, t2 := range r1.Tiles {
		ids1[t2.AltText] = t2.Id
	}
	for _, t2 := range r2.Tiles {
		if ids1[t2.AltText] != t2.Id {
			t.Errorf("tile %q: id changed %s→%s", t2.AltText, ids1[t2.AltText], t2.Id)
		}
	}
}

func TestGetGrid_SwepsDefinitelyGonePID(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	p := openPlugin(t, root, nil)

	ar, _ := attachAt(p, parentPID)

	// First call: child appears.
	r1, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})
	childKey := strconv.FormatInt(childPID, 10)
	var found bool
	for _, t2 := range r1.Tiles {
		if t2.AltText == childKey {
			found = true
		}
	}
	if !found {
		t.Fatal("child not found in first GetGrid")
	}

	// Remove the child PID from the fake /proc.
	os.RemoveAll(filepath.Join(root, childKey))

	// Second call: child should be swept.
	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})
	for _, t2 := range r2.Tiles {
		if t2.AltText == childKey {
			t.Error("child still present after process exit")
		}
	}
}

func TestProbe_Present(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	p := openPlugin(t, root, nil)

	ar, _ := attachAt(p, parentPID)
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})

	childKey := strconv.FormatInt(childPID, 10)
	var tileID string
	for _, t2 := range r.Tiles {
		if t2.AltText == childKey {
			tileID = t2.Id
		}
	}
	if tileID == "" {
		t.Fatal("child tile not found")
	}

	pr, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: tileID})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("Presence = %v, want PRESENT", pr.Presence)
	}
}

func TestProbe_Gone(t *testing.T) {
	p := openPlugin(t, "", nil)
	pr, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: "999999"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Errorf("Presence = %v, want GONE for unknown tile", pr.Presence)
	}
}

func TestDeleteTile_SignalsProcess(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	killer := &recordKiller{}
	p := openPlugin(t, root, killer)

	ar, _ := attachAt(p, parentPID)
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: ar.RootGridId})

	childKey := strconv.FormatInt(childPID, 10)
	var tileID string
	for _, t2 := range r.Tiles {
		if t2.AltText == childKey {
			tileID = t2.Id
		}
	}
	if tileID == "" {
		t.Fatal("child tile not found")
	}

	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if len(killer.killed) != 1 || killer.killed[0].pid != childPID {
		t.Errorf("expected SIGTERM to pid %d, got: %v", childPID, killer.killed)
	}
}

func TestDeleteTile_UnknownIsNoOp(t *testing.T) {
	p := openPlugin(t, "", nil)
	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: "999999"})
	if err != nil {
		t.Errorf("DeleteTile of unknown tile should be no-op: %v", err)
	}
}

// Verify that procsource.DefaultRoot and Children are accessible (smoke test
// that the procsource package is wired correctly).
func TestProcsourceIntegration(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skipping live /proc test in CI")
	}
	_, err := procsource.Children(procsource.DefaultRoot, 1)
	if err != nil {
		t.Logf("procsource.Children(1): %v (non-fatal — PID 1 might be unreadable)", err)
	}
}

// TestMoveTile_PersistsAndSurvivesReopen: a moved process tile keeps its
// position across GetGrid and across a DB reopen — placement persists for the
// proc plugin too.
func TestMoveTile_PersistsAndSurvivesReopen(t *testing.T) {
	root, parentPID, childPID := stubProcRoot(t)
	dbPath := filepath.Join(t.TempDir(), "proc.db")
	p, err := proc.Open(dbPath, root, &recordKiller{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	att, err := attachAt(p, parentPID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	gridID := att.RootGridId
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	childKey := strconv.FormatInt(childPID, 10)
	var childTile *gridwellv1.Tile
	for _, tile := range r.Tiles {
		if tile.AltText == childKey {
			childTile = tile
		}
	}
	if childTile == nil {
		t.Fatalf("child tile %q not found in %+v", childKey, r.Tiles)
	}

	if _, err := p.PlaceTile(context.Background(), &gridwellv1.PlaceTileRequest{TileId: childTile.Id, X: 4, Y: 6, W: 1, H: 1}); err != nil {
		t.Fatalf("MoveTile: %v", err)
	}
	p.Close()

	p2, err := proc.Open(dbPath, root, &recordKiller{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p2.Close()
	r2, err := p2.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatalf("GetGrid after reopen: %v", err)
	}
	for _, tile := range r2.Tiles {
		if tile.AltText == childKey {
			if tile.X != 4 || tile.Y != 6 {
				t.Errorf("after reopen child at (%d,%d), want (4,6)", tile.X, tile.Y)
			}
			return
		}
	}
	t.Fatalf("child tile %q missing after reopen", childKey)
}
