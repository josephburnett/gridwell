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

	gwrpc "github.com/josephburnett/gridwell/api/rpc"
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
	renv := []string{"GRIDWELL_HOME=" + remoteHome}
	run(t, renv, bin, "init", "--kind", "local", "--name", "personal")
	remoteOrigin := startServe(t, bin, remoteHome, "127.0.0.1:0")
	remoteAddr := strings.TrimPrefix(remoteOrigin, "http://")

	// Node B: local + the remote plugin. Note --name omitted for the
	// remote plugin: it defaults to the kind (owner decision 2026-08-16).
	localHome := t.TempDir()
	lenv := []string{"GRIDWELL_HOME=" + localHome}
	run(t, lenv, bin, "init", "--kind", "local", "--name", "home")
	run(t, lenv, bin, "init", "--kind", "remote")
	localOrigin := startServe(t, bin, localHome, "127.0.0.1:0")

	lp := rpc(t, localOrigin, "ListPlugins", map[string]any{})
	var instGrid string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["kind"] == "remote" {
			instGrid, _ = pm["instanceGridId"].(string)
			if pm["label"] != "remote" {
				t.Fatalf("label = %v, want the kind as the default name", pm["label"])
			}
		}
	}
	if instGrid == "" {
		t.Fatal("remote plugin declared no instance grid")
	}

	// A DIRECT connection: addr only, host empty — no sshd exists in this
	// entire test. The connection's child is the remote's HOME (remote-menu,
	// 2026-08-16): personal's root grid, exactly where a direct client of
	// that node boots — writable, immediately usable.
	sshRoot := commitConnectionParams(t, localOrigin, instGrid,
		fmt.Sprintf(`{"addr":%q}`, remoteAddr))

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
	if lbl := mp[0].(map[string]any)["label"]; lbl != "personal" {
		t.Fatalf("routed menu plugin = %v, want personal", lbl)
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
	if _, err := gwrpc.NewDefaultClient(localOrigin).WriteContent(ctx,
		txt["id"].(string), num(txt["version"]), []byte("direct, no ssh anywhere")); err != nil {
		t.Fatalf("write through direct chain: %v", err)
	}
	body, _, _, err := gwrpc.NewDefaultClient(localOrigin).ReadContent(ctx, txt["id"].(string))
	if err != nil || string(body) != "direct, no ssh anywhere" {
		t.Fatalf("read through direct chain = %q (%v)", body, err)
	}
}
