package proxytest_test

import (
	"context"
	"io"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/proxytest"
)

// proxied stands up an in-process "remote" localdb (with a fake shell host),
// wraps a transparent proxy around a client to it, and returns a client to the
// proxy — exactly the topology the ssh plugin uses, minus the SSH transport.
func proxied(t *testing.T) gridwellv1.GridwellClient {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	remote := local.New(st, shellsvc.NewManager(shellsvctest.New()))
	remoteClient, remoteCloser, err := plugin.ServeInProcess(remote)
	if err != nil {
		t.Fatalf("serve remote: %v", err)
	}
	t.Cleanup(remoteCloser)

	proxyClient, proxyCloser, err := plugin.ServeInProcess(proxytest.New(remoteClient))
	if err != nil {
		t.Fatalf("serve proxy: %v", err)
	}
	t.Cleanup(proxyCloser)
	return proxyClient
}

func TestProxy_UnaryForwards(t *testing.T) {
	c := proxied(t)
	ctx := context.Background()

	info, err := c.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != "home" || info.RootGridId == "" {
		t.Fatalf("Info through proxy = %+v, want the remote's", info)
	}

	// A create + write + read round trip, end to end through the proxy
	// (creation is metadata-only; the body follows through WriteContent).
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	got := writeThenRead(t, ctx, c, created.Tile.Id, created.Tile.Version, []byte("# remote"))
	if string(got) != "# remote" {
		t.Errorf("content through proxy = %q, want %q", got, "# remote")
	}
}

// writeThenRead drives the contracted content surface: WriteContent commits
// the bytes (version claimed from the created row), ReadContent streams them
// back. The test helper for what CreateTile.data used to carry.
func writeThenRead(t *testing.T, ctx context.Context, c gridwellv1.GridwellClient, tileID string, version int64, data []byte) []byte {
	t.Helper()
	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tileID, Version: version, Data: data}); err != nil {
		t.Fatalf("WriteContent send: %v", err)
	}
	if _, err := w.CloseAndRecv(); err != nil {
		t.Fatalf("WriteContent close: %v", err)
	}
	r, err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	var out []byte
	for {
		chunk, err := r.Recv()
		if err != nil {
			break
		}
		out = append(out, chunk.Data...)
	}
	return out
}

func TestProxy_OpenShellBidiForwards(t *testing.T) {
	c := proxied(t)
	ctx := context.Background()
	info, _ := c.Info(ctx, &gridwellv1.InfoRequest{})

	// A shell tile must exist on the remote for OpenShell to bind.
	shellTile, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 0, Y: 0, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}

	stream, err := c.OpenShell(ctx)
	if err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if err := stream.Send(&gridwellv1.OpenShellRequest{TileId: shellTile.Tile.Id, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := stream.Send(&gridwellv1.OpenShellRequest{Data: []byte("ping")}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(resp.Data) != "ping" {
		t.Errorf("shell echo through proxy = %q, want %q", resp.Data, "ping")
	}
	_ = stream.CloseSend()
}

func TestProxy_ContentStreamForwards(t *testing.T) {
	c := proxied(t)
	ctx := context.Background()
	info, _ := c.Info(ctx, &gridwellv1.InfoRequest{})

	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}

	// WriteContent (client-stream) forwards with commit-at-close intact.
	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{
		TileId: created.Tile.Id, Version: created.Tile.Version, Data: []byte("# through "),
	}); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{Data: []byte("the proxy")}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	resp, err := w.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}

	// ReadContent (server-stream) back through the proxy, meta on chunk 1.
	r, err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	var got []byte
	var version int64
	first := true
	for {
		chunk, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if first {
			version = chunk.Version
			first = false
		}
		got = append(got, chunk.Data...)
	}
	if string(got) != "# through the proxy" {
		t.Errorf("content through proxy = %q", got)
	}
	if version != resp.Tile.Version {
		t.Errorf("version %d through proxy, want %d — the basis must survive the hop", version, resp.Tile.Version)
	}
}
