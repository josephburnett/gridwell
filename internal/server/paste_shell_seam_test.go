package server_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// A drop onto a shell, through the web API, types the file's path and the
// text tile's reference into the shell's real tmux, attached or not, and a
// shell whose tmux never started is refused with nothing started.
func TestAPasteTypesIntoARealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	socket := fmt.Sprintf("gw-paste-%d", os.Getpid())
	ctrl, cleanup, err := tmux.New(socket, "", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		_ = cleanup()
	})
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "my notes.txt"), "hello")
	reg := &server.Sources{Home: local.New(st, shellsvc.NewManager(shellsvc.NewLive(ctrl)))}
	src, err := files.New(st.Files(), []files.Root{{Path: root}}, byPath(os.RemoveAll))
	if err != nil {
		t.Fatal(err)
	}
	reg.Files = src
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: lp.HomeGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: lp.HomeGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, Y: 0, W: 1, H: 1}}, []byte("Notes"))
	if err != nil {
		t.Fatal(err)
	}
	file := rpc.QualifyID(localNodeID, rpc.KeyTileID(filepath.Join(root, "my notes.txt")))
	const origin = "https://rtb.example.ts.net:10010"

	err = cl.PasteShell(ctx, shell.Id, []string{file}, origin)
	if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(err.Error(), "starts none") {
		t.Fatalf("a paste into a tmux never started answered %v, want refused with nothing started", err)
	}
	if alive, _ := cl.ShellTmuxAlive(ctx, shell.Id); alive {
		t.Fatal("the refused paste started the tmux")
	}

	name := tmux.Name(rpc.LocalOf(shell.Id))
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name, "-x", "200", "-y", "10", "sh").CombinedOutput(); err != nil {
		t.Fatalf("start the tmux: %v: %s", err, out)
	}
	if err := cl.PasteShell(ctx, shell.Id, []string{file, text.Id}, origin); err != nil {
		t.Fatalf("paste: %v", err)
	}
	want := "'" + filepath.Join(root, "my notes.txt") + "' " + origin + "/" + lp.HomeGridId + "/" + rpc.LocalOf(text.Id)
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-J", "-t", name).CombinedOutput()
		if err == nil && strings.Contains(string(out), want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pane never showed %q:\n%s", want, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A shell on a far node takes its own node's tiles, spelled by that node
// under its own web address, whatever origin the near client named; a tile
// of another node is refused with the reason.
func TestAFarShellPastesItsOwnNodesTiles(t *testing.T) {
	far := newFarNodeWith(t, server.Config{ID: "rnode1", WebAddr: "0.0.0.0:7777"})
	localShell := shellsvctest.New()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := &server.Sources{Home: local.New(st, shellsvc.NewManager(localShell))}
	connectTo(t, "geneva", far, nil)(reg, st)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	farRoot := connectionRows(lp)[0].RootGridId
	shell, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, Y: 0, W: 1, H: 1}}, []byte("Far notes"))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: lp.HomeGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("Mine"))
	if err != nil {
		t.Fatal(err)
	}
	key := rpc.LocalOf(shell.Id)
	far.shell.SetAlive(key, true)

	if err := cl.PasteShell(ctx, shell.Id, []string{text.Id}, "https://near.example:10010"); err != nil {
		t.Fatalf("paste onto the far shell: %v", err)
	}
	want := "http://localhost:7777/rnode1/" + rpc.LocalOf(farRoot) + "/" + rpc.LocalOf(text.Id)
	if got := far.shell.Pasted(key); len(got) != 1 || got[0] != want {
		t.Errorf("the far tmux was typed %q, want %q", got, want)
	}
	err = cl.PasteShell(ctx, shell.Id, []string{mine.Id}, "https://near.example:10010")
	if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(err.Error(), "is not on the shell's node") {
		t.Errorf("a near tile onto the far shell answered %v, want refused: not on the shell's node", err)
	}
	if got := far.shell.Pasted(key); len(got) != 1 {
		t.Errorf("a refused paste typed into the far tmux: %q", got)
	}
}

// checkPaste drops a text file of the shell's own node onto the shell, as a
// user types `cat ` and drags the file over: the tmux takes its path.
func checkPaste(f *parityFix, o parityOwner, k parityKind) observable {
	shell := f.tile(o, k)
	prefix := map[parityOwner]string{onHome: "", onFar: parityConn}[o]
	path := filepath.Join(f.disk[prefix].main, parityFileNames[kText])
	key := rpc.LocalOf(shell.Id)
	pty := f.nodes[prefix].shell
	pty.SetAlive(key, true)
	err := f.cl.PasteShell(f.ctx, shell.Id, []string{f.fileID(prefix, path)}, "https://client.example")
	obs := observable{Outcome: clientsync.Of(err).String(), Read: fmt.Sprint(pty.Pasted(key))}
	if got := pty.Pasted(key); len(got) == 1 && got[0] == path {
		obs.Read = "the path of " + parityFileNames[kText]
	}
	return obs
}
