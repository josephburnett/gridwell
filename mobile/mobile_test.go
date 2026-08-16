package mobile

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
)

// The embedded-node contract (offline-plan phase 2), exercised as plain
// Go — the same code gomobile binds, minus the platform packaging (which
// only the real-hardware pass can prove): first Start auto-inits a home,
// serves the embedded web client and the RPC surface on loopback with
// in-process plugins, refuses shells node-wide; a restart reopens the
// SAME durable home (ids stable — the phone's tiles are as permanent as
// any node's).

func post(t *testing.T, origin, method string, req any) map[string]any {
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
		t.Fatalf("%s: bad json: %v", method, err)
	}
	return out
}

func TestEmbeddedNodeLifecycle(t *testing.T) {
	home := filepath.Join(t.TempDir(), "gwhome")

	origin, err := Start(home)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Idempotent while running.
	if again, err := Start(home); err != nil || again != origin {
		t.Fatalf("second Start = (%q, %v), want the same origin", again, err)
	}

	// The embedded web client serves — the webview's first load.
	res, err := http.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Contains(page, []byte("gridwell.wasm")) {
		t.Fatalf("GET / = %d (%d bytes), want the embedded index", res.StatusCode, len(page))
	}

	// The handshake: one auto-inited localdb named "home", shells off.
	lp := post(t, origin, "ListPlugins", map[string]any{})
	plugins := lp["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("plugins = %d, want the one auto-inited localdb", len(plugins))
	}
	pm := plugins[0].(map[string]any)
	if pm["label"] != "home" || pm["kind"] != "local" {
		t.Fatalf("auto-init plugin = %v", pm)
	}
	if lp["shellsDisabled"] != true {
		t.Fatal("shells must be OFF node-wide on mobile (no PTY exists)")
	}
	firstUUID := pm["uuid"].(string)
	rootGrid := pm["rootGridId"].(string)

	// A real write through the in-process plugin.
	tile := post(t, origin, "CreateTile", map[string]any{
		"gridId": rootGrid,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)

	// Restart: the SAME home, the same identity, the tile still there —
	// the phone's tiles are as durable as any node's.
	Stop()
	origin2, err := Start(home)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer Stop()
	lp2 := post(t, origin2, "ListPlugins", map[string]any{})
	pm2 := lp2["plugins"].([]any)[0].(map[string]any)
	if pm2["uuid"] != firstUUID {
		t.Fatalf("plugin id changed across restart: %v → %v", firstUUID, pm2["uuid"])
	}
	g := post(t, origin2, "GetGrid", map[string]any{"gridId": rootGrid})
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
