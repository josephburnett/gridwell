//go:build connections

// Package connections_test is the spawn gate, run by `make
// check-connections`. It runs the separately compiled binaries through a real
// ssh tunnel and asserts one write and read crossing every hop, which the
// in-process seam tests cannot see. It needs the binaries already built at the
// repo root, and the `connections` build tag keeps plain `go test ./...` fast.
package connections_test

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

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	gwrpc "github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/connection/dial/dialtest"
)

// repoRoot walks up to the directory holding go.work. This test is its own
// module, so the nearest go.mod is its own and the binaries land at the
// workspace root.
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

// startServe launches `gridwell serve` for a home and returns its origin and
// the connection door's socket path once the banner announces them. The
// socket lives under the home, so two nodes on one box never collide.
func startServe(t *testing.T, bin, home, bind string) (origin, fedSocket string) {
	origin, fedSocket, _ = startServeProc(t, bin, home, bind)
	return origin, fedSocket
}

// startServeProc is startServe with a stop, for a test that partitions a
// node mid-session and brings it back on the same address. stop is idempotent
// with the registered cleanup.
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

	// The "serving on <addr>" banner is the readiness contract, and the
	// desktop sidecar parses this exact line.
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

// The web door is always password-gated, so startServeProc records each
// origin's auth token from the serve banner and every helper below rides it
// as the cookie a logged-in browser would carry.
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

// httpFor is the authenticated client for an announced origin.
func httpFor(origin string) *http.Client {
	tokensMu.Lock()
	tok := tokens[origin]
	tokensMu.Unlock()
	return &http.Client{Transport: cookieTransport{tok}}
}

// clientFor is httpFor as an api/rpc client, for the foreign-writer calls.
func clientFor(origin string) *gwrpc.Client {
	return gwrpc.NewClient(httpFor(origin), origin, connect.WithProtoJSON())
}

// rpcRaw posts one Connect-JSON call and returns the raw status and body,
// for asserting on a deliberate refusal. rpc fails the test on any non-200.
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

// freshHome seeds a home with an empty server.yaml, so the first serve mints
// the node's id and creates its store through node.BuildConfig.
func freshHome(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "server.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// The remote node gets a fresh home.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")

	// A real ssh server fronts it, the same sshd the seam test uses, with
	// the production binary dialing it.
	creds := dialtest.Server(t, t.TempDir())

	// The local node's connection is server.yaml config, declared before
	// first serve and reconciled at boot.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "fedconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")

	// 1. The connection presents as its own menu row and gains its root,
	//    the remote's home through the declared segment.
	lp := rpc(t, localOrigin, "Handshake", map[string]any{})
	var homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] == "home" {
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}
	sshRoot := awaitConnRoot(t, localOrigin, "fedconn1")

	// 2. The landing is the remote's home, where a direct client of that
	//    node boots, and its + menu is the routed plugin list.
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

	// 3. Create a named well with content on the remote, through the
	//    chain.
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
	// Creation carries metadata only, and the body follows through the
	// content write, routed by the qualified id.
	txtRow, err := clientFor(localOrigin).WriteContent(context.Background(),
		txt["id"].(string), num(txt["version"]), []byte("# across the spawn gate"))
	if err != nil {
		t.Fatalf("WriteContent through the chain: %v", err)
	}

	// 4. Link the remote well into the local home grid, the left-drag
	//    gesture, and read the content back through the link. A right-drag
	//    deep-copies through the chain, so the local home gains an
	//    independent solid well whose text body matches the remote's.
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
			"childGridId": wellChild, "altText": "remote grid"},
	})["tile"].(map[string]any)
	if link["childGridId"] != wellChild {
		t.Fatalf("link child = %v, want the shared remote grid %s", link["childGridId"], wellChild)
	}
	if link["reference"] != true {
		t.Fatal("the link must arrive as a dashed reference")
	}
	got, _, _, err := clientFor(localOrigin).ReadContent(context.Background(), txt["id"].(string))
	if err != nil {
		t.Fatalf("ReadContent through the chain: %v", err)
	}
	if string(got) != "# across the spawn gate" {
		t.Fatalf("content through the chain = %q", got)
	}

	// 5. Live events cross the mount: a write on the remote node arrives on
	//    the local Subscribe stream as a TileChanged carrying the fully
	//    chained tile id, over the seam no in-process test can see.
	//
	// Connect holds response headers until the first Send, so the open and
	// the receive loop both live in the goroutine while the main loop makes
	// remote edits until one of their events arrives.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	gotEvents := make(chan *gridwellv1.Event, 64)
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

	// A remote-direct id is the chained id with the node and connection
	// segments peeled.
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	// protojson omits zero fields, so a fresh tile has no "version" key.
	txtID := txt["id"].(string)
	version := txtRow.Version

	// The local fan-in dials the remote stream asynchronously, so the first
	// write can race stream establishment. Each write is a real remote edit,
	// so any one of them arriving proves the whole path.
	writeTick := time.NewTicker(500 * time.Millisecond)
	defer writeTick.Stop()
	deadline2 := time.After(25 * time.Second)
	wrote := 0
	for {
		var arrived *gridwellv1.TileChanged
		select {
		case <-writeTick.C:
			body := fmt.Sprintf("# edited on the remote, take %d", wrote)
			// The foreign writer speaks the content write directly
			// against the remote node, as another device would.
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
			if c := ev.GetTileChanged(); c != nil && c.GetTile().GetId() == txtID {
				arrived = c
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
		if arrived.Tile.GridId != wellChild {
			t.Fatalf("event grid id = %q, want the chained %q", arrived.Tile.GridId, wellChild)
		}
		break
	}

	fmt.Println("connections spawn gate: production binaries, real tunnel, chained write/read + session + live events OK")
}

// Connections are server.yaml config, here through real binaries: declared
// before first serve, presenting as a menu row, refusing mutation on the wire.
// Retiring one means naming it in retired_names and restarting, the
// declaration going away not being enough.
func TestConnectionsModeSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// The remote node, with a real sshd fronting it.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())

	// The local node declares the connection in server.yaml before first
	// serve.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "cmconn1", creds, remoteAddr))
	localOrigin, _, stopLocal := startServeProc(t, bin, localHome, "127.0.0.1:0")

	// 1. The connection is a menu row of its own, the transport's row is
	//    hidden behind it, and the learned root is the remote's home.
	child := awaitConnRoot(t, localOrigin, "cmconn1")
	if strings.Count(child, "/") != 3 {
		t.Fatalf("root = %q, want the four-segment <ssh>/<conn>/<rplugin>/<grid>", child)
	}

	// 2. A connection has no well row on the wire, so "<id>/0" names a
	//    grid that does not exist.

	// 3. Real bytes through all three peels and back.
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

	// 4. Retirement is a config edit plus a restart. The name goes into
	//    retired_names, since the declaration going away alone would only
	//    make the connection dead until it came back. The row goes, the
	//    namespace stops resolving forever, and the remote keeps its tile.
	stopLocal()
	// The node's minted id, written by the first serve, is kept.
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

	fmt.Println("connections spawn gate: connections mode — yaml-declared, real tunnel, chained bytes, config-refused edits, clean retirement OK")
}

// appendConnectionsYAML writes the connections section into a home's
// server.yaml, before its first serve.
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

// awaitConnRoot polls the plugin list until the named connection's menu row
// carries its learned root, which means the tunnel answered. A connection is
// a plugins row of kind connection, rpc.ConnectionRow's shape.
func awaitConnRoot(t *testing.T, origin, name string) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		lp := rpc(t, origin, "Handshake", map[string]any{})
		rows, _ := lp["plugins"].([]any)
		for _, p := range rows {
			pm := p.(map[string]any)
			if pm["kind"] != "connection" {
				continue
			}
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

// A key-form id must survive both directions of the chain: the remote derives
// it, the transport passes it through without reading it as a namespace hop,
// and a read routed back on it lands on the same entry. Only the real tunnel
// catches that: the transport's peel is where a segment shape is classified.
func TestKeyFormIdsCrossTheTunnel(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	// The remote serves a directory through the fs plugin. Nothing there is
	// touched, so every tile it answers is named by its key.
	files := t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "far.txt"), []byte("hello from the far side"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	appendConnectionsYAML(t, remoteHome, fmt.Sprintf(`plugins:
    - kind: fs
      label: files
      binary: %s
      config:
        root: %s
`, filepath.Join(root, "gridwell-plugin-fs"), files))
	_, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")

	creds := dialtest.Server(t, t.TempDir())
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "fedkey1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")

	sshRoot := awaitConnRoot(t, localOrigin, "fedkey1")
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	nodeNS, _ := ng["grid"].(map[string]any)["nodeNs"].(string)
	menu := rpc(t, localOrigin, "Handshake", map[string]any{"namespace": nodeNS})
	// The fs plugin's declared collection is the doorway, since a plugin
	// names no grid of its own, and the entry's grid id chains through the
	// hop like every other id.
	var fsRoot string
	for _, p := range menu["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] != "files" {
			continue
		}
		entries, _ := pm["menuEntries"].([]any)
		if len(entries) == 1 {
			fsRoot, _ = entries[0].(map[string]any)["gridId"].(string)
		}
	}
	if fsRoot == "" {
		t.Fatalf("the remote's fs collection is not on the routed menu: %v", menu["plugins"])
	}

	g := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": fsRoot})
	var farID string
	for _, ti := range g["tiles"].([]any) {
		tm := ti.(map[string]any)
		if tm["altText"] == "far.txt" {
			farID, _ = tm["id"].(string)
		}
	}
	if farID == "" {
		t.Fatalf("far.txt not listed through the tunnel: %v", g["tiles"])
	}
	if !strings.Contains(farID, "/~") {
		t.Fatalf("far.txt came back as %q, want an untouched entry's key form", farID)
	}
	// The id routes back to the same tile and its bytes.
	back := rpc(t, localOrigin, "GetTile", map[string]any{"tileId": farID})["tile"].(map[string]any)
	if back["altText"] != "far.txt" {
		t.Fatalf("GetTile on a key-form id through the chain = %v", back)
	}
	body, _, _, err := clientFor(localOrigin).ReadContent(context.Background(), farID)
	if err != nil || string(body) != "hello from the far side" {
		t.Fatalf("ReadContent on a key-form id through the chain = %q (%v)", body, err)
	}

	// The id also survives a touch made from this side: a durable fact mints
	// a row on the far node and the entry keeps the id its listing answers
	// under. A rename there is invisible on either node alone, so only a
	// re-list through the tunnel tells the two apart.
	placed := rpc(t, localOrigin, "PlaceTile", map[string]any{
		"tileId": farID, "gridId": fsRoot, "x": 6, "y": 3, "w": 1, "h": 1,
	})["tile"].(map[string]any)
	if placed["id"] != farID {
		t.Fatalf("the touch renamed the far entry: %v, was %q", placed["id"], farID)
	}
	g = rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": fsRoot})
	var again map[string]any
	for _, ti := range g["tiles"].([]any) {
		tm := ti.(map[string]any)
		if tm["altText"] == "far.txt" {
			again = tm
		}
	}
	if again == nil || again["id"] != farID {
		t.Fatalf("the far listing renamed far.txt after the touch: %v, was %q", again, farID)
	}
	// proto-JSON renders int64 as a string, so the comparison renders
	// too.
	if fmt.Sprint(again["x"]) != "6" || fmt.Sprint(again["y"]) != "3" {
		t.Fatalf("the placement did not cross the tunnel: %v", again)
	}
	body, _, _, err = clientFor(localOrigin).ReadContent(context.Background(), farID)
	if err != nil || string(body) != "hello from the far side" {
		t.Fatalf("ReadContent after the touch = %q (%v)", body, err)
	}
	fmt.Println("connections spawn gate: key-form ids cross the tunnel, survive a touch, and route back OK")
}
