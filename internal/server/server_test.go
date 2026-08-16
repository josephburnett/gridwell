package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// registerPrimaryLocaldb serves st as a localdb plugin in reg under its stable
// uuid — exactly how production registers the primary DB. Returns the uuid and
// the qualified root grid id the client should open at.
func registerPrimaryLocaldb(t *testing.T, reg *plugin.Registry, st *store.Store) (uuid, qualifiedRoot string) {
	t.Helper()
	uuid, err := st.PluginUUID(context.Background())
	if err != nil {
		t.Fatalf("plugin uuid: %v", err)
	}
	client, closer, err := plugin.ServeInProcess(localdb.New(st, nil))
	if err != nil {
		t.Fatalf("serve primary localdb: %v", err)
	}
	t.Cleanup(closer)
	reg.Register(uuid, "localdb", client, nil)
	bareRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid id: %v", err)
	}
	return uuid, uuid + "/" + bareRoot
}

// newTestServer wires up a Server whose primary DB is a registered localdb
// plugin (the server holds no store of its own for the data plane) and returns
// the httptest server, a typed Connect client, and the qualified root grid id.
func newTestServer(t *testing.T) (*httptest.Server, *rpc.Client, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, st)

	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	return hs, cl, root
}

// errCode extracts the Connect code from an RPC error. Returns 0 for
// nil errors and CodeInternal for non-Connect errors.
func errCode(err error) connect.Code {
	if err == nil {
		return connect.Code(0)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeInternal
}

func TestCreateWell(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: root, X: 1, Y: 2, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well: %v", err)
	}
	if tile.Kind != rpc.KindWell {
		t.Errorf("got kind %q, want %q", tile.Kind, rpc.KindWell)
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscribe + Recv happens in a goroutine because the underlying
	// HTTP/1.1 streaming response only flushes headers once the
	// server sends its first frame — so cl.Subscribe blocks until the
	// CreateWell below fires. Concurrent setup avoids the deadlock.
	doneCh := make(chan rpc.Event, 1)
	errCh := make(chan error, 1)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			errCh <- err
			return
		}
		defer stream.Close()
		ev, ok, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		if !ok {
			errCh <- errors.New("stream ended without an event")
			return
		}
		doneCh <- ev
	}()

	// Give the goroutine a moment to land its subscribe request on the
	// server's subscriber list before triggering the event.
	time.Sleep(100 * time.Millisecond)
	if _, err := cl.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("create well: %v", err)
	}

	select {
	case ev := <-doneCh:
		if ev.Kind != rpc.EventTileChanged && ev.Kind != rpc.EventGridChanged {
			t.Errorf("first event kind = %q", ev.Kind)
		}
	case err := <-errCh:
		t.Errorf("stream error: %v", err)
	case <-ctx.Done():
		t.Error("subscribe produced no event before timeout")
	}
}

func TestSPAFallbackForUnknownPaths(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dir := t.TempDir()
	const indexBody = "<html>index</html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexBody), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	const assetBody = "console.log(\"asset\");"
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte(assetBody), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, st)
	srv := New(reg, Config{StaticFS: os.DirFS(dir)})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	tests := []struct {
		path string
		want string
	}{
		{"/", indexBody},
		{"/3", indexBody},
		{"/27/26/25/24/23/22/21/20/19/16/15/14/12", indexBody},
		{"/wasm_exec.js", assetBody},
	}
	for _, tc := range tests {
		resp, err := http.Get(hs.URL + tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", tc.path, resp.StatusCode)
		}
		if string(body) != tc.want {
			t.Errorf("%s: body = %q, want %q", tc.path, body, tc.want)
		}
	}

	// /rpc/ paths — the pre-Connect RPC namespace, no longer registered at
	// all — must 404 rather than fall through to index.html.
	resp, err := http.Get(hs.URL + "/rpc/Bogus")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/rpc/Bogus status = %d, want 404", resp.StatusCode)
	}
}

// TestSubscribeFansInProxiedPlugin locks the capability rule for event fan-in:
// a plugin declares watch in its Info handshake, and the server must fan in
// its events REGARDLESS of the local kind string. This is the remote shape —
// the ssh plugin serves a transparent proxy around a remote node, so its local
// kind is "ssh" while the proxied Info (forwarded verbatim) says watch=true.
// Before the fix, Subscribe skipped every plugin whose kind wasn't "localdb",
// so a remote localdb's events never reached the client.
func TestSubscribeFansInProxiedPlugin(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uuid, err := st.PluginUUID(context.Background())
	if err != nil {
		t.Fatalf("plugin uuid: %v", err)
	}

	// The "remote node": a localdb served over gRPC.
	inner, innerClose, err := plugin.ServeInProcess(localdb.New(st, nil))
	if err != nil {
		t.Fatalf("serve inner localdb: %v", err)
	}
	t.Cleanup(innerClose)
	// The local ssh plugin: a transparent proxy around the remote client,
	// registered under kind "ssh" exactly as production would.
	proxied, proxClose, err := plugin.ServeInProcess(proxy.New(inner))
	if err != nil {
		t.Fatalf("serve proxy: %v", err)
	}
	t.Cleanup(proxClose)

	reg := plugin.NewRegistry()
	reg.Register(uuid, "ssh", proxied, nil)
	reg.SetTransit(uuid, true) // the declaration the loader reads from Info in production

	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	bareRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid id: %v", err)
	}
	root := uuid + "/" + bareRoot

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	doneCh := make(chan rpc.Event, 1)
	errCh := make(chan error, 1)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			errCh <- err
			return
		}
		defer stream.Close()
		ev, ok, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		if !ok {
			errCh <- errors.New("stream ended without an event")
			return
		}
		doneCh <- ev
	}()

	time.Sleep(200 * time.Millisecond)
	if _, err := cl.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("create well through the proxy: %v", err)
	}

	select {
	case ev := <-doneCh:
		if ev.TileChanged == nil {
			t.Fatalf("got event kind %q, want a tile_changed", ev.Kind)
		}
		// The event must arrive qualified with the ssh plugin's uuid.
		if got := ev.TileChanged.Tile.GridID; got != root {
			t.Errorf("event grid id = %q, want the qualified %q", got, root)
		}
	case err := <-errCh:
		t.Fatalf("subscribe: %v", err)
	case <-ctx.Done():
		t.Fatal("no event arrived from the proxied plugin (fan-in skipped it?)")
	}
}
