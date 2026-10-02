//go:build connections

// Shell sessions across a real connection: a clone made on the far node names
// its source's session there, and attaching the clone through this node's
// /shell door runs that one session; a shell cloned onto this node from the far
// one cannot share a session that lives over there, so it lands as a link.

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

func TestShellSessionsAcrossAConnection(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	_, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, fmt.Sprintf("connections:\n    - name: shconn\n      addr: %s\n", remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")
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
	clone := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": srcID, "destGridId": farRoot, "x": 2, "y": 0,
	})["tile"].(map[string]any)
	cloneID := clone["id"].(string)
	if got, _ := clone["shellSession"].(string); got != srcID {
		t.Fatalf("a clone on the far node names session %q, want its source %q spelled through the hop", got, srcID)
	}

	// Attaching the clone starts the source's session on the far node.
	out := make(chan []byte, 64)
	exits := make(chan shellstream.Exit, 4)
	reg := shellstream.New(
		shellws.Dialer(shellws.Options{Origin: localOrigin, HTTPClient: httpFor(localOrigin)}),
		func(_ string, b []byte) { out <- append([]byte(nil), b...) },
		func(e shellstream.Exit) { exits <- e })
	reg.Open(srcID, cloneID, 80, 24)
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
			t.Fatalf("the clone's attachment ended: %+v", e)
		case <-deadline:
			t.Fatalf("no output through the far clone within 20s: %q", seen.String())
		}
	}
	for _, id := range []string{srcID, cloneID} {
		if alive, _ := rpc(t, localOrigin, "ShellSessionAlive", map[string]any{"tileId": id})["alive"].(bool); !alive {
			t.Errorf("ShellSessionAlive(%s) = false: the clone's attach must have started its source's session", id)
		}
	}

	// Across nodes the copy is a link to its source.
	cross := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": srcID, "destGridId": homeRoot, "x": 4, "y": 4,
	})["tile"].(map[string]any)
	if got, _ := cross["linkTargetId"].(string); got != srcID || cross["reference"] != true {
		t.Fatalf("a shell cloned across nodes = %v, want a link to %q", cross, srcID)
	}

	// End the session so the far tmux server does not outlive the test.
	reg.Write(srcID, []byte("exit\r"))
	select {
	case <-exits:
	case <-time.After(20 * time.Second):
		t.Fatal("the far session never ended")
	}
}
