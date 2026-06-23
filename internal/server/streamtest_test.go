package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// streamTestServer wires up a fresh in-memory Server for HTTP-level tests
// (shell stream, previews). Shared by the *_test.go files in this package.
func streamTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	uuid, root := registerPrimaryLocaldb(t, reg, st)
	srv := New(reg, uuid, st, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs, root
}

// createURLTileViaRPC creates a URL tile through the RPC surface and returns
// its id. Used by tests that need a URL tile to exist (e.g. preview reads).
func createURLTileViaRPC(t *testing.T, hs *httptest.Server, root string, url string) string {
	t.Helper()
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	tile, err := cl.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: url,
	})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	return tile.ID
}
