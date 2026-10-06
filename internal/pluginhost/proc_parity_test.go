package pluginhost_test

// The proc stack through a full server: a dead child is probed and swept while
// the survivors keep their id and placement, and a retired key never re-mints
// on reads of an unchanged grid. The plugin is the real gridwell-plugin-proc
// binary over the real /proc, rooted at this test process, so killing a child
// this test owns is exactly the disappearance the sweep arbitrates.

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const procUUID = "procuux"

// sleeper starts a real child of this test process and returns its pid and a
// reaper. The reaper kills AND waits: an unreaped child stays in /proc as a
// zombie, so the plugin would still see it and the sweep would be right not
// to remove it.
func sleeper(t *testing.T) (pid string, reap func()) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a child process: %v", err)
	}
	done := false
	reap = func() {
		if done {
			return
		}
		done = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(reap)
	return strconv.Itoa(cmd.Process.Pid), reap
}

func pluginProcNode(t *testing.T) *rpc.Client {
	t.Helper()
	return pluginProcNodeAt(t, filepath.Join(t.TempDir(), "mem.db"))
}

// pluginProcNodeAt stands the shipped proc binary up over an existing store
// path, rooted at this test process, configured exactly as a server.yaml
// plugins: entry would be.
func pluginProcNodeAt(t *testing.T, memPath string) *rpc.Client {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "proc", map[string]string{"pid": strconv.Itoa(os.Getpid())})
	client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

// tileNamed returns the tile whose label is the given pid, or the zero tile.
func tileNamed(tiles []*gridwellv1.Tile, label string) *gridwellv1.Tile {
	for _, tile := range tiles {
		if tile.AltText == label {
			return tile
		}
	}
	return &gridwellv1.Tile{}
}

// TestProcPluginSweepAndPlacement: place a child, kill another; the
// next read sweeps only the dead one and the placement persists.
func TestProcPluginSweepAndPlacement(t *testing.T) {
	dying, reapDying := sleeper(t)
	surviving, _ := sleeper(t)
	v2 := pluginProcNode(t)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	child := tileNamed(g.Tiles, surviving)
	if child.Id == "" {
		t.Fatalf("child %s not found among %v", surviving, g.Tiles)
	}
	if tileNamed(g.Tiles, dying).Id == "" {
		t.Fatalf("child %s not found among %v", dying, g.Tiles)
	}
	if _, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: child.Id, GridId: rootGrid, X: 5, Y: 5, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// The other child dies: the next read probes and sweeps it.
	reapDying()
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if tileNamed(g.Tiles, dying).Id != "" {
		t.Fatal("dead child still listed")
	}
	back := tileNamed(g.Tiles, surviving)
	if back.Id == "" {
		t.Fatal("living child swept")
	}
	if back.X != 5 || back.Y != 5 {
		t.Fatalf("survivor drifted: %+v", back)
	}
	// The placement minted the row, so the tile is named by it now; the
	// address the client held before still resolves to the same tile.
	if held, err := v2.GetTile(ctx, child.Id); err != nil || held.Id != back.Id {
		t.Fatalf("the pre-mint address stopped resolving: %+v (%v), want %s", held, err, back.Id)
	}
}

// TestRetiredKeyStaysRetiredWithoutIdBurn pins "a retired key stays retired"
// against the cache. If a non-authoritative listing's cached union kept a
// swept key, every later read would re-mint a fresh id for it and immediately
// re-retire it: the rows would grow without bound and the AUTOINCREMENT
// sequence would advance on every read of an unchanged grid. Reading never
// mutates.
func TestRetiredKeyStaysRetiredWithoutIdBurn(t *testing.T) {
	_, reap := sleeper(t)
	memPath := filepath.Join(t.TempDir(), "mem.db")
	v2 := pluginProcNodeAt(t, memPath)
	ctx := context.Background()

	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := plugintest.LandingOf(t, pl.Plugins[0])
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 1: mint the live rows
	}
	reap()
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 2: probe + sweep the dead child
	}

	count := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", memPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM tiles WHERE ns = 'p1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for i := 0; i < 3; i++ {
		if _, err := v2.GetGrid(ctx, root); err != nil {
			t.Fatal(err)
		}
	}
	if after := count(); after != before {
		t.Fatalf("idmap grew %d → %d across reads of an UNCHANGED grid: the cached union resurrects the retired key and every read mints-and-retires a fresh id", before, after)
	}
}

// shellWithChild starts `sh`, a child of this test process, which starts a
// `sleep` of its own and prints its pid. The sleep outlives the sh, and is
// killed at cleanup.
func shellWithChild(t *testing.T) (sh *exec.Cmd, child string) {
	t.Helper()
	sh = exec.Command("sh", "-c", "sleep 600 & echo $!; wait")
	out, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatalf("start sh: %v", err)
	}
	t.Cleanup(func() { _ = sh.Process.Kill(); _, _ = sh.Process.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read the child's pid: %v", err)
	}
	child = strings.TrimSpace(line)
	pid, err := strconv.Atoi(child)
	if err != nil {
		t.Fatalf("child pid %q: %v", child, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return sh, child
}

// A process is under its parent's grid now. Kill an intermediate sh and its
// child reparents: the next read of the sh's grid sweeps the child's placed
// row, and the @info row of the dead sh, rather than keeping both for as long
// as the child lives.
func TestProcReparentedChildAndDeadInfoSweep(t *testing.T) {
	sh, child := shellWithChild(t)
	v2 := pluginProcNode(t)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := v2.GetGrid(ctx, plugintest.LandingOf(t, pl.Plugins[0]))
	if err != nil {
		t.Fatal(err)
	}
	well := tileNamed(root.Tiles, strconv.Itoa(sh.Process.Pid))
	if well.ChildGridId == "" {
		t.Fatalf("sh %d has no well among %v", sh.Process.Pid, root.Tiles)
	}
	g, err := v2.GetGrid(ctx, well.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	info := tileNamed(g.Tiles, "@info")
	if info.Id == "" || tileNamed(g.Tiles, child).Id == "" {
		t.Fatalf("sh's grid lacks @info or child %s: %v", child, g.Tiles)
	}
	if info.TextPresentation != rpc.TextPresentationBoth {
		t.Errorf("@info presents %q; want %q", info.TextPresentation, rpc.TextPresentationBoth)
	}
	for i, tile := range []*gridwellv1.Tile{info, tileNamed(g.Tiles, child)} {
		if _, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
			TileId: tile.Id, GridId: well.ChildGridId, X: int64(4 + 2*i), Y: 4, W: 1, H: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	_ = sh.Process.Kill()
	_, _ = sh.Process.Wait()
	g, err = v2.GetGrid(ctx, well.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{child, "@info"} {
		if tile := tileNamed(g.Tiles, label); tile.Id != "" {
			t.Errorf("%s is still in the dead sh's grid: %+v", label, tile)
		}
	}
}

// Deleting a process already gone is done: the plugin answers OK and the
// node's probe retires the row at once, without waiting for a read. Deleting
// an @info tile is refused and signals nothing.
func TestProcDeleteOfAGoneProcessRetiresItsRow(t *testing.T) {
	gone, reap := sleeper(t)
	alive, _ := sleeper(t)
	memPath := filepath.Join(t.TempDir(), "mem.db")
	v2 := pluginProcNodeAt(t, memPath)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := v2.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: tileNamed(g.Tiles, gone).Id, GridId: rootGrid, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	reap()
	if _, err := v2.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: placed.Id}); err != nil {
		t.Fatalf("delete of a gone process: %v", err)
	}
	db, err := sql.Open("sqlite", memPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tombstoned bool
	if err := db.QueryRow(`SELECT tombstoned FROM tiles WHERE ns = 'p1' AND key = ?`, gone).Scan(&tombstoned); err != nil || !tombstoned {
		t.Errorf("row for gone pid %s: tombstoned=%v (%v); want retired by the delete", gone, tombstoned, err)
	}

	sg, err := v2.GetGrid(ctx, tileNamed(g.Tiles, alive).ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tileNamed(sg.Tiles, "@info").Id}); err == nil {
		t.Error("deleting @info succeeded; want a refusal")
	}
	pid, _ := strconv.Atoi(alive)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("deleting @info signalled its process %s: %v", alive, err)
	}
}
