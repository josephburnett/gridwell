package server

import (
	"bytes"
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The unload-beacon seam (the transport-loss class): the
// beacon helpers hand-build wire bodies that navigator.sendBeacon posts
// after the page is gone — nothing else ever exercises those bytes, so
// they MUST be pinned against the real Connect handler. The WriteContent
// beacon is the risky one: it hand-rolls the Connect client-streaming
// envelope (flags byte + big-endian length + proto-JSON message), and a
// silent protocol mismatch would discard the user's last paragraph on
// every tab close while returning 200.
func beaconTestServer(t *testing.T) (cl *rpc.Client, hs *httptest.Server, root string) {
	t.Helper()
	reg := plugin.NewRegistry()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, root = registerPrimaryLocaldb(t, reg, st)
	srv := mustNew(t, reg, Config{})
	hs = serveWeb(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), hs, root
}

// postBeacon posts through hs's client — navigator.sendBeacon is
// same-origin and carries the auth cookie, so the test must too.
func postBeacon(t *testing.T, hs *httptest.Server, path, contentType string, body []byte) *http.Response {
	t.Helper()
	res, err := hs.Client().Post(hs.URL+path, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("beacon POST %s: %v", path, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	io.Copy(io.Discard, res.Body)
	return res
}

func TestWriteContentBeaconSeam(t *testing.T) {
	cl, hs, root := beaconTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 2, H: 2}}, []byte("before"))
	if err != nil {
		t.Fatal(err)
	}

	path, body := rpc.WriteContentBeacon(tile.Id, tile.Version, []byte("survived the tab close"))
	if path == "" || body == nil {
		t.Fatal("WriteContentBeacon returned empty")
	}
	res := postBeacon(t, hs, path, rpc.BeaconStreamType, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("beacon POST = %d, want 200", res.StatusCode)
	}

	data, _, _, err := cl.ReadContent(ctx, tile.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "survived the tab close" {
		t.Fatalf("content after beacon = %q — the streaming envelope did not land", data)
	}

	// A STALE claim must conflict exactly like the ordinary client call —
	// a beacon must never force-write over a foreign edit. (The store's
	// answer, not the transport's: the POST itself still returns 200 with
	// the error in the stream, which is why the pin asserts CONTENT.)
	path, body = rpc.WriteContentBeacon(tile.Id, tile.Version, []byte("stale stomp"))
	postBeacon(t, hs, path, rpc.BeaconStreamType, body)
	data, _, _, err = cl.ReadContent(ctx, tile.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "survived the tab close" {
		t.Fatalf("stale beacon overwrote content: %q", data)
	}

	// Oversized data refuses at build time (the browser would truncate or
	// reject it) so the caller falls back to the async path.
	if p, b := rpc.WriteContentBeacon(tile.Id, 1, bytes.Repeat([]byte("x"), 128*1024)); p != "" || b != nil {
		t.Error("oversized WriteContentBeacon should return empty for async fallback")
	}
}

func TestSetURLStateBeaconSeam(t *testing.T) {
	cl, hs, root := beaconTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 3, Y: 0, W: 2, H: 2}})
	if err != nil {
		t.Fatal(err)
	}
	tile, err = cl.WriteContent(ctx, tile.Id, tile.Version, []byte("https://start.example"))
	if err != nil {
		t.Fatal(err)
	}

	// The landed address is content, so it beacons as a claimed write; the
	// title and trail are captures and beacon as SetTile.
	apath, abody := rpc.WriteContentBeacon(tile.Id, tile.Version, []byte("https://deep.example/page/40"))
	if res := postBeacon(t, hs, apath, rpc.BeaconStreamType, abody); res.StatusCode != http.StatusOK {
		t.Fatalf("url-address beacon = %d, want 200", res.StatusCode)
	}
	path, body := rpc.SetTileBeacon(&gridwellv1.SetTileRequest{
		TileId: tile.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL,
			AltText:    "page 40",
			UrlHistory: `["https://start.example","https://deep.example/page/40"]`,
		},
	})
	res := postBeacon(t, hs, path, rpc.BeaconJSONType, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("url-state beacon = %d, want 200", res.StatusCode)
	}

	after, err := cl.GetTile(ctx, tile.Id)
	if err != nil {
		t.Fatal(err)
	}
	if after.UrlString != "https://deep.example/page/40" || after.UrlHistory == "" {
		t.Fatalf("url state after beacon = (%q, %q) — the trail did not land",
			after.UrlString, after.UrlHistory)
	}
	if after.Version <= tile.Version {
		t.Errorf("landed address left version at %d; an address is content and bumps", after.Version)
	}
}
