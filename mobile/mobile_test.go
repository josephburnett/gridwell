package mobile

import (
	"bytes"
	"encoding/json"
	"github.com/josephburnett/gridwell/internal/node"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded-node contract (offline-plan phase 2), exercised as plain
// Go — the same code gomobile binds, minus the platform packaging (which
// only the real-hardware pass can prove): first Start auto-inits a home,
// serves the embedded web client and the RPC surface on loopback with
// in-process plugins, refuses shells node-wide; a restart reopens the
// SAME durable home (ids stable — the phone's tiles are as permanent as
// any node's).

// webview plays the phone's webview: it loads the URL Start returned (the
// token login — the web door is always gated, 2026-08-26), keeps the
// cookie it was issued, and returns the origin it landed on plus the
// client that holds the session.
func webview(t *testing.T, loginURL string) (string, *http.Client) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Get(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	// The embedded web client serves — the webview's first load lands home.
	if res.StatusCode != 200 || !bytes.Contains(page, []byte("gridwell.wasm")) {
		t.Fatalf("GET %s = %d (%d bytes), want the embedded index after the token login", loginURL, res.StatusCode, len(page))
	}
	origin, _, ok := strings.Cut(loginURL, "/auth/login?token=")
	if !ok {
		t.Fatalf("Start returned %q, want the token-login URL", loginURL)
	}
	return origin, client
}

func post(t *testing.T, client *http.Client, origin, method string, req any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, err := client.Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
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
		t.Fatalf("%s: bad json: %v", method, err)
	}
	return out
}

func TestEmbeddedNodeLifecycle(t *testing.T) {
	home := filepath.Join(t.TempDir(), "gwhome")

	loginURL, err := Start(home)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Idempotent while running.
	if again, err := Start(home); err != nil || again != loginURL {
		t.Fatalf("second Start = (%q, %v), want the same URL", again, err)
	}
	origin, client := webview(t, loginURL)
	// Unauthenticated, the door is shut: init minted the password.
	if res, err := http.Get(origin + "/"); err != nil || res.StatusCode != 401 {
		t.Fatalf("GET / without the cookie = %v %v, want 401", res, err)
	}

	// The handshake: one auto-inited localdb named "home", shells off.
	lp := post(t, client, origin, "ListPlugins", map[string]any{})
	plugins := lp["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("plugins = %d, want the one auto-inited localdb", len(plugins))
	}
	pm := plugins[0].(map[string]any)
	if pm["label"] != "home" || pm["kind"] != "home" {
		t.Fatalf("auto-init plugin = %v", pm)
	}
	if lp["shellsDisabled"] != true {
		t.Fatal("shells must be OFF node-wide on mobile (no PTY exists)")
	}
	firstUUID := pm["uuid"].(string)
	rootGrid := pm["rootGridId"].(string)

	// A real write through the in-process plugin.
	tile := post(t, client, origin, "CreateTile", map[string]any{
		"gridId": rootGrid,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)

	// Restart: the SAME home, the same identity, the tile still there —
	// the phone's tiles are as durable as any node's.
	Stop()
	loginURL2, err := Start(home)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer Stop()
	origin2, client2 := webview(t, loginURL2)
	lp2 := post(t, client2, origin2, "ListPlugins", map[string]any{})
	pm2 := lp2["plugins"].([]any)[0].(map[string]any)
	if pm2["uuid"] != firstUUID {
		t.Fatalf("plugin id changed across restart: %v → %v", firstUUID, pm2["uuid"])
	}
	g := post(t, client2, origin2, "GetGrid", map[string]any{"gridId": rootGrid})
	found := false
	for _, ti := range g["tiles"].([]any) {
		if ti.(map[string]any)["id"] == tile["id"] {
			found = true
		}
	}
	if !found {
		t.Fatal("the tile did not survive the restart")
	}

	// Stop is idempotent.
	Stop()
	Stop()
}

// The phone honors `connections:` config exactly as the desktop does
// (2026-08-27): mobile used to compose its OWN remote factory, which
// skipped the config-mode reconcile — the same server.yaml declared a
// connection on a laptop and nothing on a phone. The native factories
// are the node's now; this pins the seam from the yaml to the menu.
func TestEmbeddedNodeHonorsConnectionsConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "gwhome")
	loginURL, err := Start(home)
	if err != nil {
		t.Fatal(err)
	}
	Stop()
	// The desktop's init door registers the remote kind; the phone's
	// first run only seeds local. Declare both, as the CLI would.
	if _, err := node.Init(home, "remote", "connections", nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(home, "server.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// addr points at a closed socket path: the boot-time connect fails
	// fast and the row still exists, pending.
	if _, err := f.WriteString("connections:\n    - name: phoneconn\n      label: Laptop\n      addr: /nonexistent/federation.sock\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	loginURL, err = Start(home)
	if err != nil {
		t.Fatalf("restart with connections: %v", err)
	}
	defer Stop()
	origin, client := webview(t, loginURL)
	lp := post(t, client, origin, "ListPlugins", map[string]any{})
	var labels []string
	for _, p := range lp["plugins"].([]any) {
		labels = append(labels, p.(map[string]any)["label"].(string))
	}
	if !strings.Contains(strings.Join(labels, ","), "Laptop") {
		t.Fatalf("the declared connection is not a menu row on the phone: %v", labels)
	}
}
