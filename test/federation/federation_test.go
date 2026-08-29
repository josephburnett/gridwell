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
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	gwrpc "github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/remote/dial/dialtest"
)

// repoRoot walks up from the test binary's source dir to the REPO root —
// the directory holding go.work (this test lives in its own module now,
// so the nearest go.mod is its own; the binaries land at the workspace
// root).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above the test dir")
		}
		dir = parent
	}
}

// startServe launches the real `gridwell serve` for a home and returns its
// origin and the federation door's socket path once the banner announces
// them (the socket lives under the home, so two nodes on one box never
// collide).
func startServe(t *testing.T, bin, home, bind string) (origin, fedSocket string) {
	origin, fedSocket, _ = startServeProc(t, bin, home, bind)
	return origin, fedSocket
}

// startServeProc is startServe returning a stop() as well, for tests that
// PARTITION a node mid-session (kill it hard) and bring it back on the
// same address. stop is idempotent with the registered cleanup.
func startServeProc(t *testing.T, bin, home, bind string) (origin, fedSocket string, stop func()) {
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
	stop = func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(stop)

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
				fi := strings.Index(line, "federation=")
				if fi < 0 {
					t.Fatalf("banner lacks federation=: %q", line)
				}
				fedSocket = strings.TrimSuffix(line[fi+len("federation="):], ")")
				if ai := strings.Index(line, "auth="); ai >= 0 {
					tokensMu.Lock()
					tokens["http://"+addr] = strings.Fields(line[ai+len("auth="):])[0]
					tokensMu.Unlock()
				}
				go func() { // keep draining so the child never blocks on stderr
					for l := range lines {
						if os.Getenv("GW_FED_DEBUG") != "" {
							fmt.Fprintln(os.Stderr, "[serve]", l)
						}
					}
				}()
				return "http://" + addr, fedSocket, stop
			}
		case <-deadline:
			t.Fatalf("serve for %s never announced", home)
		}
	}
}

// The web door is always password-gated (2026-08-26): startServeProc
// records each origin's auth token from the serve banner, and every
// helper below rides it as the cookie a logged-in browser would carry.
var (
	tokensMu sync.Mutex
	tokens   = map[string]string{} // origin → server.AuthToken
)

type cookieTransport struct{ token string }

func (c cookieTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Cookie", "gridwell_auth="+c.token)
	return http.DefaultTransport.RoundTrip(r)
}

// httpFor is the authenticated client for an origin startServeProc announced.
func httpFor(origin string) *http.Client {
	tokensMu.Lock()
	tok := tokens[origin]
	tokensMu.Unlock()
	return &http.Client{Transport: cookieTransport{tok}}
}

// clientFor is httpFor as an api/rpc client (the foreign-writer calls).
func clientFor(origin string) *gwrpc.Client {
	return gwrpc.NewClient(httpFor(origin), origin, connect.WithProtoJSON())
}

// rpcRaw posts one Connect-JSON call and returns the raw status + body —
// for asserting on DESIGNED refusals (rpc t.Fatals on any non-200).
func rpcRaw(t *testing.T, origin, method string, req any) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, err := httpFor(origin).Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
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
	hr, err := httpFor(origin).Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
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

// freshHome seeds a home with an EMPTY server.yaml — the first serve mints
// the node's id and creates its store (the one door, node.BuildConfig).
func freshHome(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "server.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFederationSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node: a fresh home.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")

	// A real ssh server fronting it (shared helper — the same sshd the seam
	// test uses, here with the PRODUCTION gridwell-ssh dialing it).
	creds := dialtest.Server(t, t.TempDir())

	// Local node: one localdb + the builtin transport; the connection is
	// server.yaml CONFIG (v2 #269), declared before first serve and
	// reconciled at boot.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "fedconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")

	// 1. The connection presents as its own menu row and gains its root
	//    — the remote's HOME through the declared segment: the chained
	//    <ssh>/<conn>/<rplugin>/<grid> mount root the rest drives.
	lp := rpc(t, localOrigin, "Handshake", map[string]any{})
	var homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] == "home" {
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}
	sshRoot := awaitConnRoot(t, localOrigin, "fedconn1")

	// 2. The landing is the remote's HOME, where a direct client of that
	//    node boots (remote-menu, 2026-08-16). The remote's own + menu is
	//    the ROUTED plugin list for the landing's node. (No network
	//    context rides the grid anymore — 2026-07-26.)
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	if pe, ok := ng["grid"].(map[string]any)["proxyEndpoint"]; ok && pe != "" {
		t.Fatalf("transit grid still carries a proxyEndpoint %v — the network-context surface should be gone", pe)
	}
	nodeNS, _ := ng["grid"].(map[string]any)["nodeNs"].(string)
	if nodeNS == "" {
		t.Fatal("the landing grid must carry its serving node's namespace (node_ns)")
	}
	menu := rpc(t, localOrigin, "Handshake", map[string]any{"namespace": nodeNS})
	mp := menu["plugins"].([]any)
	if len(mp) != 1 {
		t.Fatalf("routed menu has %d plugins through the tunnel, want the remote's home alone", len(mp))
	}
	workChild, _ := mp[0].(map[string]any)["rootGridId"].(string)
	if workChild != sshRoot {
		t.Fatalf("routed menu root = %q, want the landing %q", workChild, sshRoot)
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
	txtRow, err := clientFor(localOrigin).WriteContent(context.Background(),
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
	copiedBody, _, _, err := clientFor(localOrigin).ReadContent(context.Background(), copiedTextID)
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
	got, _, _, err := clientFor(localOrigin).ReadContent(context.Background(), txt["id"].(string))
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
		sub, err := clientFor(localOrigin).Subscribe(ctx)
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

	// The remote-direct ids are the chained ids with the ssh plugin AND
	// connection hops peeled (two segments since the #251 migration turned
	// the mount into a connection).
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
			wt, werr := clientFor(remoteOrigin).WriteContent(
				context.Background(), peel(peel(txtID)), version, []byte(body))
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

// v2 (#269): connections are server.yaml CONFIG through REAL binaries —
// declared before first serve, presented as a menu row of their own,
// mutation-refused on the wire, bytes flowing through the real tunnel,
// and RETIRED by removing the declaration and restarting: the row
// disappears, the namespace stops resolving, the remote is untouched.
func TestConnectionsModeSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node: one localdb, served for real; a real sshd fronting it.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())

	// Local node: localdb + the builtin transport, the connection declared
	// in server.yaml before first serve.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "cmconn1", creds, remoteAddr))
	localOrigin, _, stopLocal := startServeProc(t, bin, localHome, "127.0.0.1:0")

	// 1. The connection is a menu row of its own; the transport's row is
	//    hidden behind it; the learned root is the chained
	//    <ssh>/<conn>/<rplugin>/<grid> mount root (the remote's home).
	child := awaitConnRoot(t, localOrigin, "cmconn1")
	if strings.Count(child, "/") != 3 {
		t.Fatalf("root = %q, want the four-segment <ssh>/<conn>/<rplugin>/<grid>", child)
	}

	// (2. The picker door is gone from the wire: a connection's well row is
	//    not addressable — "<id>/0" is the home store's grid 0, which does
	//    not exist.)

	// 3. Real bytes through all three peels: local server → connection
	//    segment → remote node → back.
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
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": child,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	body := "# through a declared connection"
	if _, err := clientFor(localOrigin).WriteContent(context.Background(),
		txt["id"].(string), num(txt["version"]), []byte(body)); err != nil {
		t.Fatalf("WriteContent through the connection chain: %v", err)
	}
	if got, _, _, err := clientFor(localOrigin).ReadContent(context.Background(), txt["id"].(string)); err != nil || string(got) != body {
		t.Fatalf("ReadContent through the connection chain = %q (%v)", got, err)
	}

	// 4. RETIREMENT is a config edit + restart: the declaration goes, the
	//    row goes, the namespace stops resolving forever — and the REMOTE
	//    keeps its tile (verified on its own front door).
	stopLocal()
	// Keep the node's minted id (the first serve wrote it); replace the
	// connection list with the retirement.
	cur, err := os.ReadFile(filepath.Join(localHome, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	base := string(cur)
	if i := strings.Index(base, "connections:"); i >= 0 {
		base = base[:i]
	}
	if err := os.WriteFile(filepath.Join(localHome, "server.yaml"),
		[]byte(base+"connections: []\nretired_names:\n    - cmconn1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localOrigin2, _ := startServe(t, bin, localHome, "127.0.0.1:0")
	lp := rpc(t, localOrigin2, "Handshake", map[string]any{})
	for _, p := range lp["plugins"].([]any) {
		if uuid, _ := p.(map[string]any)["uuid"].(string); strings.HasSuffix(uuid, "/cmconn1") {
			t.Fatal("a retired connection must not row in")
		}
	}
	if code, _ := rpcRaw(t, localOrigin2, "GetGrid", map[string]any{"gridId": child}); code == 200 {
		t.Fatal("a retired connection's namespace must stop resolving")
	}
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	remoteTxt := peel(peel(txt["id"].(string)))
	if rbody, _, _, err := clientFor(remoteOrigin).ReadContent(context.Background(), remoteTxt); err != nil || string(rbody) != body {
		t.Fatalf("the remote must be untouched by retirement: %q (%v)", rbody, err)
	}

	fmt.Println("federation spawn gate: connections mode — yaml-declared, real tunnel, chained bytes, config-refused edits, clean retirement OK")
}

// appendConnectionsYAML writes the v2 connections section into a home's
// server.yaml BEFORE first serve (v2 #269: connections are config — the
// picker flow this suite used to wire is deleted).
func appendConnectionsYAML(t *testing.T, home, section string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "server.yaml"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(section); err != nil {
		t.Fatal(err)
	}
}

// sshConnectionYAML renders one ssh-bridged connection declaration.
func sshConnectionYAML(t *testing.T, name string, creds dialtest.Creds, remoteAddr string) string {
	t.Helper()
	sshHost, sshPort, ok := strings.Cut(creds.Addr, ":")
	if !ok {
		t.Fatalf("bad sshd addr %q", creds.Addr)
	}
	return fmt.Sprintf(`connections:
    - name: %s
      host: %s
      port: %s
      user: joe
      key: %s
      known_hosts: %s
      addr: %s
`, name, sshHost, sshPort, creds.KeyPath, creds.KnownHostsPath, remoteAddr)
}

// awaitConnRoot polls the plugin list until the named connection's menu
// row (v2 #269: instances present as rows of their own) carries its
// learned root — the real tunnel answered.
func awaitConnRoot(t *testing.T, origin, name string) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		lp := rpc(t, origin, "Handshake", map[string]any{})
		conns, _ := lp["connections"].([]any)
		for _, p := range conns {
			pm := p.(map[string]any)
			if uuid, _ := pm["uuid"].(string); strings.HasSuffix(uuid, "/"+name) {
				if root, _ := pm["rootGridId"].(string); root != "" {
					return root
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("connection %q never gained its root through the real tunnel", name)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
