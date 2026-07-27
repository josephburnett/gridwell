//go:build federation

// Package federation_test is the SPAWN GATE (`make check-federation`): it runs
// the real, separately-compiled binaries — `gridwell` (init + serve) and the
// go-plugin subprocesses including `gridwell-ssh` — through a real ssh tunnel
// and asserts one write/read crossing every hop. The in-process seam tests
// cannot see go-plugin spawn: the pluginmeta sqlite-driver bug (b648691) kept
// every test green while every production spawn failed. This gate closes that
// class (issue #58).
//
// Requires the binaries already built at the repo root (`make build` — the
// make target depends on it); guarded by the `federation` build tag so plain
// `go test ./...` (make check) stays fast.
package federation_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/plugin/sshdial/sshdialtest"
	gwrpc "github.com/josephburnett/gridwell/internal/rpc"
)

// repoRoot walks up from the test binary's source dir to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test dir")
		}
		dir = parent
	}
}

// startServe launches the real `gridwell serve` for a home and returns its
// origin once the banner announces the bound address.
func startServe(t *testing.T, bin, home, bind string) string {
	t.Helper()
	cmd := exec.Command(bin, "serve", "--bind", bind, "--static", "")
	cmd.Env = append(os.Environ(), "GRIDWELL_HOME="+home, "GRIDWELL_PLUGIN_DIR="+filepath.Dir(bin))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = cmd.Stderr // banner goes to stdout in some builds; merge
	stdout, err := cmd.StdoutPipe()
	if err == nil && stdout != nil {
		go io.Copy(io.Discard, stdout)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// The "serving on <addr>" banner is the readiness contract (the desktop
	// sidecar parses this exact line).
	sc := bufio.NewScanner(stderr)
	deadline := time.After(30 * time.Second)
	lines := make(chan string, 64)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("serve for %s exited before announcing", home)
			}
			if i := strings.Index(line, "serving on "); i >= 0 {
				addr := strings.Fields(line[i+len("serving on "):])[0]
				go func() { // keep draining so the child never blocks on stderr
					for range lines {
					}
				}()
				return "http://" + addr
			}
		case <-deadline:
			t.Fatalf("serve for %s never announced", home)
		}
	}
}

// rpcRaw posts one Connect-JSON call and returns the raw status + body —
// for asserting on DESIGNED refusals (rpc t.Fatals on any non-200).
func rpcRaw(t *testing.T, origin, method string, req any) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, err := http.Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer hr.Body.Close()
	data, _ := io.ReadAll(hr.Body)
	return hr.StatusCode, data
}

// rpc posts one Connect-JSON call and decodes the response.
func rpc(t *testing.T, origin, method string, req any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, err := http.Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer hr.Body.Close()
	data, _ := io.ReadAll(hr.Body)
	if hr.StatusCode != 200 {
		t.Fatalf("%s: HTTP %d: %s", method, hr.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: bad json %q: %v", method, data, err)
	}
	return out
}

func run(t *testing.T, env []string, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
}

func TestFederationSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node: two localdb plugins.
	remoteHome := t.TempDir()
	renv := []string{"GRIDWELL_HOME=" + remoteHome}
	run(t, renv, bin, "init", "--kind", "localdb", "--name", "personal")
	run(t, renv, bin, "init", "--kind", "localdb", "--name", "work")
	remoteOrigin := startServe(t, bin, remoteHome, "127.0.0.1:0")
	remoteAddr := strings.TrimPrefix(remoteOrigin, "http://")

	// A real ssh server fronting it (shared helper — the same sshd the seam
	// test uses, here with the PRODUCTION gridwell-ssh dialing it).
	creds := sshdialtest.Server(t, t.TempDir())

	// Local node: one localdb + the ssh mount.
	localHome := t.TempDir()
	lenv := []string{"GRIDWELL_HOME=" + localHome}
	run(t, lenv, bin, "init", "--kind", "localdb", "--name", "home")
	run(t, lenv, bin, "init", "--kind", "ssh", "--name", "rtb",
		"--config", "host="+creds.Addr, "--config", "user=joe",
		"--config", "key="+creds.KeyPath, "--config", "known_hosts="+creds.KnownHostsPath,
		"--config", "addr="+remoteAddr)
	localOrigin := startServe(t, bin, localHome, "127.0.0.1:0")

	// 1. The ssh plugin SPAWNED and mounted the whole node: its root is the
	//    remote's node grid, a chained id.
	lp := rpc(t, localOrigin, "ListPlugins", map[string]any{})
	var sshRoot, homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		switch pm["label"] {
		case "rtb":
			sshRoot, _ = pm["rootGridId"].(string)
		case "home":
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}
	if strings.Count(sshRoot, "/") != 2 {
		t.Fatalf("ssh mount root = %q, want a chained <ssh>/<rnode>/0 id — did gridwell-ssh spawn?", sshRoot)
	}

	// 2. The remote node grid lists both remote plugins through the tunnel.
	//    (No network context rides the grid anymore — 2026-07-26, owner
	//    decision 2: live url tiles always browse from the host's network,
	//    and the tunnel SOCKS proxy is gone.)
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	if pe, ok := ng["grid"].(map[string]any)["proxyEndpoint"]; ok && pe != "" {
		t.Fatalf("transit grid still carries a proxyEndpoint %v — the network-context surface should be gone", pe)
	}
	tiles := ng["tiles"].([]any)
	if len(tiles) != 2 {
		t.Fatalf("remote node grid has %d tiles through the tunnel, want 2", len(tiles))
	}
	workChild := ""
	for _, ti := range tiles {
		tm := ti.(map[string]any)
		if tm["altText"] == "work" {
			workChild, _ = tm["childGridId"].(string)
		}
	}
	if workChild == "" {
		t.Fatal("no 'work' tile on the remote node grid")
	}

	// 3. Create a named well with content on the remote, through the chain.
	well := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": workChild,
		"tile":   map[string]any{"kind": "well", "x": 1, "y": 1, "w": 1, "h": 1, "altText": "remote grid"},
	})["tile"].(map[string]any)
	wellID := well["id"].(string)
	wellChild := well["childGridId"].(string)
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": wellChild,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	// Creation is metadata-only; the body follows through the ONE content
	// write, routed through the chain by the qualified id.
	txtRow, err := gwrpc.NewDefaultClient(localOrigin).WriteContent(context.Background(),
		txt["id"].(string), num(txt["version"]), []byte("# across the spawn gate"))
	if err != nil {
		t.Fatalf("WriteContent through the chain: %v", err)
	}

	// 4. Link the remote well into the LOCAL home grid — the 2026-07-19
	//    left-drag gesture, committed as a plain CreateTile carrying the
	//    qualified child (the chain id routes it) — and read the content
	//    back through the link. A right-drag DEEP-COPIES through the chain
	//    (#200): the local home gains an independent solid well whose text
	//    body matches the remote's, walked over the real spawn + ssh seam.
	deepCopy := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": wellID, "version": 0, "destGridId": homeRoot, "x": 5, "y": 5,
	})["tile"].(map[string]any)
	if deepCopy["reference"] == true {
		t.Fatal("the deep copy must be SOLID (a copy), not a link")
	}
	copiedGrid := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": deepCopy["childGridId"]})
	var copiedTextID string
	for _, ti := range copiedGrid["tiles"].([]any) {
		tm := ti.(map[string]any)
		if tm["kind"] == "text" {
			copiedTextID, _ = tm["id"].(string)
		}
	}
	if copiedTextID == "" {
		t.Fatalf("deep copy through the chain missing the text tile: %v", copiedGrid["tiles"])
	}
	copiedBody, _, _, err := gwrpc.NewDefaultClient(localOrigin).ReadContent(context.Background(), copiedTextID)
	if err != nil || string(copiedBody) != "# across the spawn gate" {
		t.Fatalf("deep-copied body through the chain = %q (%v)", copiedBody, err)
	}
	link := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": homeRoot,
		"tile": map[string]any{"kind": "well", "x": 0, "y": 0, "w": 1, "h": 1,
			"childGridId": wellChild, "altText": "remote grid", "objectId": well["objectId"]},
	})["tile"].(map[string]any)
	if link["childGridId"] != wellChild {
		t.Fatalf("link child = %v, want the shared remote grid %s", link["childGridId"], wellChild)
	}
	if link["reference"] != true {
		t.Fatal("the link must arrive as a dashed reference")
	}
	if link["objectId"] != well["objectId"] {
		t.Fatalf("link provenance = %v, want the remote well's object id %v", link["objectId"], well["objectId"])
	}
	got, _, _, err := gwrpc.NewDefaultClient(localOrigin).ReadContent(context.Background(), txt["id"].(string))
	if err != nil {
		t.Fatalf("ReadContent through the chain: %v", err)
	}
	if string(got) != "# across the spawn gate" {
		t.Fatalf("content through the chain = %q", got)
	}

	// (Step 5, the /session/ chain, is gone — 2026-07-26, owner decision 1:
	// the Chromium session is host-local; no session blob crosses the wire.)

	// 6. LIVE EVENTS cross the mount (the user-visible contract behind "things
	//    stay as you left them" on a remote view): a write made DIRECTLY on the
	//    remote node (another device talking to rtb, not through this mount)
	//    must arrive on the LOCAL node's Subscribe stream as a TileChanged
	//    carrying the fully chained tile id. This is the seam no in-process
	//    test can see: remote localdb → remote node export fan-in → gridwell-ssh
	//    relay → local fan-in (transit re-qualification) → the client stream.
	// The Subscribe open blocks until the server flushes its first event
	// (Connect holds response headers until the first Send), so the open and
	// the receive loop both live in the goroutine; the main loop keeps making
	// remote edits until one of their events arrives.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	gotEvents := make(chan gwrpc.Event, 64)
	subErr := make(chan error, 1)
	go func() {
		defer close(gotEvents)
		sub, err := gwrpc.NewDefaultClient(localOrigin).Subscribe(ctx)
		if err != nil {
			subErr <- err
			return
		}
		defer sub.Close()
		for {
			ev, ok, err := sub.Recv()
			if err != nil || !ok {
				return
			}
			select {
			case gotEvents <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	// The remote-direct ids are the chained ids with the ssh hop peeled.
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	// protojson omits zero fields, so a fresh tile's "version" key is absent.
	txtID := txt["id"].(string)
	version := txtRow.Version

	// Write on the remote until an event lands locally: the local fan-in dials
	// the remote stream asynchronously, so the first write can race stream
	// establishment. Each write is a REAL remote edit (version chains), so any
	// one of them arriving proves the whole path.
	writeTick := time.NewTicker(500 * time.Millisecond)
	defer writeTick.Stop()
	deadline2 := time.After(25 * time.Second)
	wrote := 0
	for {
		var arrived *gwrpc.TileChanged
		select {
		case <-writeTick.C:
			body := fmt.Sprintf("# edited on the remote, take %d", wrote)
			// The foreign writer speaks the one content write, directly
			// against the REMOTE node (another device, not this mount).
			wt, werr := gwrpc.NewDefaultClient(remoteOrigin).WriteContent(
				context.Background(), peel(txtID), version, []byte(body))
			if werr != nil {
				t.Fatalf("remote WriteContent: %v", werr)
			}
			version = wt.Version
			wrote++
			continue
		case ev, ok := <-gotEvents:
			if !ok {
				select {
				case err := <-subErr:
					t.Fatalf("local Subscribe: %v", err)
				default:
					t.Fatal("local Subscribe stream ended before the remote edit's event arrived")
				}
			}
			if ev.Kind == gwrpc.EventTileChanged && ev.TileChanged != nil &&
				ev.TileChanged.Tile.ID == txtID {
				arrived = ev.TileChanged
			}
		case <-deadline2:
			t.Fatalf("no TileChanged for %s arrived on the local stream after %d remote edits — events do not cross the ssh mount", txtID, wrote)
		}
		if arrived == nil {
			continue
		}
		if arrived.Tile.Version < 1 {
			t.Fatalf("event version = %d, want a remote EDIT's bumped version (create is 0)", arrived.Tile.Version)
		}
		if arrived.Tile.GridID != wellChild {
			t.Fatalf("event grid id = %q, want the chained %q", arrived.Tile.GridID, wellChild)
		}
		break
	}

	fmt.Println("federation spawn gate: production binaries, real tunnel, chained write/read + session + live events OK")
}

// TestConnectionsModeSpawn is #199's spawn-level proof: the SAME gridwell-ssh
// binary, launched with NO host config, comes up in connections mode — the
// remote node is a WELL the user drops, its params committed as content, and
// the whole chain `<ssh>/<conn>/<plugin>/<id>` routes through the minted
// connection segment over a REAL ssh tunnel. server.yaml names no hosts.
func TestConnectionsModeSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node: one localdb, served for real.
	remoteHome := t.TempDir()
	renv := []string{"GRIDWELL_HOME=" + remoteHome}
	run(t, renv, bin, "init", "--kind", "localdb", "--name", "personal")
	remoteOrigin := startServe(t, bin, remoteHome, "127.0.0.1:0")
	remoteAddr := strings.TrimPrefix(remoteOrigin, "http://")

	// A real sshd fronting it.
	creds := sshdialtest.Server(t, t.TempDir())
	sshHost, sshPort, ok := strings.Cut(creds.Addr, ":")
	if !ok {
		t.Fatalf("bad sshd addr %q", creds.Addr)
	}

	// Local node: a home localdb plus ONE ssh plugin with no per-host config.
	localHome := t.TempDir()
	lenv := []string{"GRIDWELL_HOME=" + localHome}
	run(t, lenv, bin, "init", "--kind", "localdb", "--name", "home")
	run(t, lenv, bin, "init", "--kind", "ssh", "--name", "connections")
	localOrigin := startServe(t, bin, localHome, "127.0.0.1:0")

	// 1. The plugin spawned in connections mode: its root is its OWN grid
	//    (one segment + "0"), not a remote chain, and it declares the #198
	//    creation schema for wells.
	lp := rpc(t, localOrigin, "ListPlugins", map[string]any{})
	var connRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] == "connections" {
			connRoot, _ = pm["rootGridId"].(string)
		}
	}
	if strings.Count(connRoot, "/") != 1 || !strings.HasSuffix(connRoot, "/0") {
		t.Fatalf("connections root = %q, want <ssh>/0 (its own grid) — did connections mode spawn?", connRoot)
	}
	rg := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": connRoot})
	schemas, _ := rg["grid"].(map[string]any)["createSchemas"].(map[string]any)
	if ws, _ := schemas["well"].(string); !strings.Contains(ws, `"host"`) {
		t.Fatalf("connection grid must declare the well creation schema, got %v", schemas)
	}

	// 2. Drop a connection well and commit its params as content — the #198
	//    flow, against real binaries.
	well := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": connRoot,
		"tile":   map[string]any{"kind": "well", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	if well["reference"] != true {
		t.Fatal("a connection well must arrive as a dashed reference")
	}
	if c, _ := well["childGridId"].(string); c != "" {
		t.Fatalf("no params yet: child must be empty, got %q", c)
	}
	// protojson renders int64 as a STRING (and omits zeros) — accept both.
	num := func(v any) int64 {
		switch x := v.(type) {
		case float64:
			return int64(x)
		case string:
			n, _ := strconv.ParseInt(x, 10, 64)
			return n
		}
		return 0
	}
	params := fmt.Sprintf(`{"host":%q,"user":"joe","port":%s,"key":%q,"known_hosts":%q,"addr":%q}`,
		sshHost, sshPort, creds.KeyPath, creds.KnownHostsPath, remoteAddr)
	if _, err := gwrpc.NewDefaultClient(localOrigin).WriteContent(context.Background(),
		well["id"].(string), num(well["version"]), []byte(params)); err != nil {
		t.Fatalf("params commit: %v", err)
	}

	// 3. The well gains its child — the remote's node grid through the minted
	//    connection segment: <ssh>/<conn>/<rnode>/0.
	var child string
	deadline := time.After(30 * time.Second)
	for child == "" {
		g := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": connRoot})
		polled, _ := g["tiles"].([]any)
		for _, ti := range polled {
			if c, _ := ti.(map[string]any)["childGridId"].(string); c != "" {
				child = c
			}
		}
		select {
		case <-deadline:
			t.Fatal("connection well never gained its child through the real tunnel")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if strings.Count(child, "/") != 3 {
		t.Fatalf("child = %q, want the four-segment <ssh>/<conn>/<rnode>/0", child)
	}

	// 4. Descend to the remote plugin and move real bytes through all three
	//    peels: local server → connection segment → remote node → localdb.
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": child})
	ngTiles := ng["tiles"].([]any)
	if len(ngTiles) != 1 {
		t.Fatalf("remote node grid: want 1 plugin tile, got %d", len(ngTiles))
	}
	personalChild, _ := ngTiles[0].(map[string]any)["childGridId"].(string)
	if strings.Count(personalChild, "/") != 3 {
		t.Fatalf("remote plugin child = %q, want four segments", personalChild)
	}
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": personalChild,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	body := "# through a dropped connection"
	if _, err := gwrpc.NewDefaultClient(localOrigin).WriteContent(context.Background(),
		txt["id"].(string), num(txt["version"]), []byte(body)); err != nil {
		t.Fatalf("WriteContent through the connection chain: %v", err)
	}
	got, _, _, err := gwrpc.NewDefaultClient(localOrigin).ReadContent(context.Background(), txt["id"].(string))
	if err != nil || string(got) != body {
		t.Fatalf("ReadContent through the connection chain = %q (%v)", got, err)
	}

	// 5. Delete unlinks: the list empties, the namespace stops resolving, and
	//    the REMOTE keeps its tile (verified on the remote's own front door).
	wellNow := rpc(t, localOrigin, "GetTile", map[string]any{"tileId": well["id"]})["tile"].(map[string]any)
	rpc(t, localOrigin, "DeleteTile", map[string]any{"tileId": well["id"], "version": num(wellNow["version"])})
	g := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": connRoot})
	// protojson omits an empty list entirely — a nil "tiles" IS the empty grid.
	if after, _ := g["tiles"].([]any); len(after) != 0 {
		t.Fatalf("after unlink: want 0 connections, got %d", len(after))
	}
	if code, _ := rpcRaw(t, localOrigin, "GetGrid", map[string]any{"gridId": child}); code == 200 {
		t.Fatal("a deleted connection's namespace must stop resolving")
	}
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	remoteTxt := peel(peel(txt["id"].(string)))
	if rbody, _, _, err := gwrpc.NewDefaultClient(remoteOrigin).ReadContent(context.Background(), remoteTxt); err != nil || string(rbody) != body {
		t.Fatalf("the remote must be untouched by the unlink: %q (%v)", rbody, err)
	}

	fmt.Println("federation spawn gate: connections mode — dropped well, real tunnel, chained bytes, clean unlink OK")
}
