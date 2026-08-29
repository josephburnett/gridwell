//go:build federation

// The DIRECT-CONNECT gate (owner decision 2026-08-16): the remote plugin
// reaches another node's export with NO ssh anywhere — the exact case
// that motivated it: two gridwell nodes on one machine, different ports
// (and identically, a tailnet address). Params carry ONLY an addr; the
// empty host is the transport selector. Trust is the network's
// (loopback/tailnet); the ssh bridge remains the authenticated transport.

package federation_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectConnectSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	ctx := context.Background()

	// Node A: the "other server on this box".
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	// remoteAddr is the FEDERATION door — its unix socket path, the only
	// thing a connection can dial since 2026-08-26; the web origin is not it.
	_, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")

	// Node B: local + the builtin transport. A DIRECT connection — addr
	// only, host empty; no sshd exists in this entire test — declared in
	// server.yaml (v2 #269) before first serve. Its root is the remote's
	// HOME (remote-menu, 2026-08-16): personal's root grid, exactly where
	// a direct client of that node boots — writable, immediately usable.
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

	// The ROUTED MENU: asking with the home grid's node_ns answers the
	// REMOTE node's plugins — the + menu a pane inside this node shows.
	menu := rpc(t, localOrigin, "ListPlugins", map[string]any{"namespace": nodeNS})
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
		txt["id"].(string), num(txt["version"]), []byte("direct, no ssh anywhere")); err != nil {
		t.Fatalf("write through direct chain: %v", err)
	}
	body, _, _, err := clientFor(localOrigin).ReadContent(ctx, txt["id"].(string))
	if err != nil || string(body) != "direct, no ssh anywhere" {
		t.Fatalf("read through direct chain = %q (%v)", body, err)
	}
}
