//go:build connections

// The direct-connect gate: two nodes on one machine, the transport reaching
// another node's export with no ssh anywhere. The connection carries only an
// addr and an empty host selects the transport; trust is the socket's mode,
// and the ssh bridge stays the authenticated transport across machines.

package connections_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gwrpc "github.com/josephburnett/gridwell/api/rpc"
)

func TestDirectConnectSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	ctx := context.Background()

	// Node A is the other server on this box.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	// remoteAddr is the connection door's unix socket path, the only thing
	// a connection can dial.
	_, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")

	// Node B declares a direct connection in server.yaml before first
	// serve: addr only, host empty, no sshd. Its root is the remote's home,
	// where a direct client of that node boots, writable and usable at
	// once.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, fmt.Sprintf("connections:\n    - name: dconn1\n      addr: %s\n", remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")
	sshRoot := awaitConnRoot(t, localOrigin, "dconn1")

	hg := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	gm := hg["grid"].(map[string]any)
	if w, _ := gm["writable"].(bool); !w {
		t.Fatalf("the landing must be the remote HOME (writable), got %v", gm)
	}
	nodeNS, _ := gm["nodeNs"].(string)
	if strings.Count(nodeNS, "/") != 1 {
		t.Fatalf("home grid nodeNs = %q, want the two-segment <remote>/<conn> chain", nodeNS)
	}

	// Asking with the home grid's node_ns answers the remote node's
	// plugins, which is the + menu a pane inside this node shows.
	menu := rpc(t, localOrigin, "Handshake", map[string]any{"namespace": nodeNS})
	mp := menu["plugins"].([]any)
	if len(mp) != 1 {
		t.Fatalf("routed menu = %d plugins, want the remote's one", len(mp))
	}
	if lbl := mp[0].(map[string]any)["label"]; lbl != "home" {
		t.Fatalf("routed menu plugin = %v, want the remote's home", lbl)
	}
	if root, _ := mp[0].(map[string]any)["rootGridId"].(string); root != sshRoot {
		t.Fatalf("routed menu root = %q, want the landing %q", root, sshRoot)
	}
	if tok, _ := menu["contentToken"].(string); tok != "" {
		t.Fatal("node-local fields must be zeroed on a routed plugin list")
	}

	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": sshRoot,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }
	if _, err := clientFor(localOrigin).WriteContent(ctx,
		txt["id"].(string), gwrpc.ContentBasis{Version: num(txt["version"])}, []byte("direct, no ssh anywhere")); err != nil {
		t.Fatalf("write through direct chain: %v", err)
	}
	body, _, _, err := clientFor(localOrigin).ReadContent(ctx, txt["id"].(string))
	if err != nil || string(body) != "direct, no ssh anywhere" {
		t.Fatalf("read through direct chain = %q (%v)", body, err)
	}
}
