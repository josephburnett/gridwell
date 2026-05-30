package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// newTestServer wires up a Server backed by an in-memory store.
// Returns the server and the bootstrapped root grid id.
func newTestServer(t *testing.T) (*httptest.Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	root, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid id: %v", err)
	}

	srv := New(st, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs, root
}

// callRPC: encode req as JSON, POST to /rpc/<method>, decode resp.
func callRPC(t *testing.T, hs *httptest.Server, method string, req any, resp any) (int, string) {
	t.Helper()
	var body bytes.Buffer
	if req != nil {
		if err := json.NewEncoder(&body).Encode(req); err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(http.MethodPost, hs.URL+"/rpc/"+method, &body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	got, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	b, _ := io.ReadAll(got.Body)
	if resp != nil && got.StatusCode == 200 {
		if err := json.Unmarshal(b, resp); err != nil {
			t.Fatalf("decode: %v body=%s", err, b)
		}
	}
	return got.StatusCode, string(b)
}

func TestBootstrapReturnsRoot(t *testing.T) {
	hs, root := newTestServer(t)
	var resp rpc.BootstrapResponse
	st, body := callRPC(t, hs, "Bootstrap", &rpc.BootstrapRequest{}, &resp)
	if st != 200 {
		t.Fatalf("bootstrap status %d: %s", st, body)
	}
	if resp.RootGridID != root {
		t.Errorf("root_grid_id = %d, want %d", resp.RootGridID, root)
	}
}

func TestCreateWell(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	st, body := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path:   rpc.Path{},
		GridID: root, X: 1, Y: 2, W: 1, H: 1,
	}, &nr)
	if st != 200 {
		t.Fatalf("create well: %d %s", st, body)
	}
	if nr.Tile.Kind != rpc.KindWell {
		t.Errorf("got %+v", nr.Tile)
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	hs, root := newTestServer(t)

	r, _ := http.NewRequest(http.MethodGet, hs.URL+"/rpc/Subscribe", nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("subscribe status %d", resp.StatusCode)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = callRPCAsync(hs, "CreateWell", &rpc.CreateWellRequest{
			Path:   rpc.Path{},
			GridID: root, X: 0, Y: 0, W: 1, H: 1,
		})
	}()

	buf := make([]byte, 4096)
	doneCh := make(chan int, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		doneCh <- n
	}()
	select {
	case n := <-doneCh:
		if n == 0 {
			t.Error("got 0 bytes from SSE stream")
		}
		s := string(buf[:n])
		if !strings.HasPrefix(s, "data: ") {
			t.Errorf("expected SSE data line, got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Error("SSE stream produced no event")
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

	srv := New(st, Config{StaticDir: dir})
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

	resp, err := http.Get(hs.URL + "/rpc/Bogus")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/rpc/Bogus status = %d, want 404", resp.StatusCode)
	}
}

// callRPCAsync is like callRPC but does not run inside a *testing.T.
func callRPCAsync(hs *httptest.Server, method string, req any) (int, string) {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(req)
	r, _ := http.NewRequest(http.MethodPost, hs.URL+"/rpc/"+method, &body)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
