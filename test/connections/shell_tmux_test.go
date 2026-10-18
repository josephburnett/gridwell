//go:build connections

// Shell tmuxes across a real connection: a copy made on the far node names
// its source's tmux there, and attaching the copy through this node's
// /shell API runs that one tmux; a shell copied onto this node from the far
// one cannot share a tmux that lives over there, so it lands as a link.

package connections_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
)

func TestShellTmuxAcrossAConnection(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	remoteDir := t.TempDir()
	freshDir(t, remoteDir)
	_, remoteAddr := startServe(t, bin, remoteDir, "127.0.0.1:0")
	localDir := t.TempDir()
	freshDir(t, localDir)
	appendConnectionsYAML(t, localDir, fmt.Sprintf("connections:\n    - name: shconn\n      addr: %s\n", remoteAddr))
	localOrigin, _ := startServe(t, bin, localDir, "127.0.0.1:0")
	farRoot := awaitConnRoot(t, localOrigin, "shconn")
	var homeRoot string
	for _, p := range rpc(t, localOrigin, "Handshake", map[string]any{})["plugins"].([]any) {
		if pm := p.(map[string]any); pm["label"] == "home" {
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}

	src := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": farRoot, "tile": map[string]any{"kind": "shell", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	srcID := src["id"].(string)
	cp := rpc(t, localOrigin, "CopyTile", map[string]any{
		"tileId": srcID, "destGridId": farRoot, "x": 2, "y": 0,
	})["tile"].(map[string]any)
	copyID := cp["id"].(string)
	if got, _ := cp["shellTmux"].(string); got != srcID {
		t.Fatalf("a copy on the far node names tmux %q, want its source %q spelled through the hop", got, srcID)
	}

	// Attaching the copy starts the source's tmux on the far node.
	out := make(chan []byte, 64)
	exits := make(chan shellstream.Exit, 4)
	reg := shellstream.New(
		shellws.Dialer(shellws.Options{Origin: localOrigin, HTTPClient: httpFor(localOrigin)}),
		func(_ string, b []byte) { out <- append([]byte(nil), b...) },
		func(e shellstream.Exit) { exits <- e })
	reg.Open(srcID, copyID, 80, 24)
	t.Cleanup(func() { reg.Close(srcID) })
	marker := "gw-far-shared"
	reg.Write(srcID, []byte("printf '%s\\n' "+marker+"\r"))
	var seen strings.Builder
	deadline := time.After(20 * time.Second)
	for strings.Count(seen.String(), marker) < 2 { // the echoed command, then its output
		select {
		case b := <-out:
			seen.Write(b)
		case e := <-exits:
			t.Fatalf("the copy's attachment ended: %+v", e)
		case <-deadline:
			t.Fatalf("no output through the far copy within 20s: %q", seen.String())
		}
	}
	for _, id := range []string{srcID, copyID} {
		if alive, _ := rpc(t, localOrigin, "ShellTmuxAlive", map[string]any{"tileId": id})["alive"].(bool); !alive {
			t.Errorf("ShellTmuxAlive(%s) = false: the copy's attach must have started its source's tmux", id)
		}
	}

	// Across nodes the copy is a link to its source.
	cross := rpc(t, localOrigin, "CopyTile", map[string]any{
		"tileId": srcID, "destGridId": homeRoot, "x": 4, "y": 4,
	})["tile"].(map[string]any)
	if got, _ := cross["linkTargetId"].(string); got != srcID || cross["link"] != true {
		t.Fatalf("a shell copied across nodes = %v, want a link to %q", cross, srcID)
	}

	// End the tmux so the far tmux server does not outlive the test.
	reg.Write(srcID, []byte("exit\r"))
	select {
	case <-exits:
	case <-time.After(20 * time.Second):
		t.Fatal("the far tmux never ended")
	}
}
