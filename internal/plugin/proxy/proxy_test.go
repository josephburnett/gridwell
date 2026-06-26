package proxy_test

import (
	"context"
	"io"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
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

	remote := localdb.New(st, shellsvc.NewManager(shellsvctest.New()))
	remoteClient, remoteCloser, err := plugin.ServeInProcess(remote)
	if err != nil {
		t.Fatalf("serve remote: %v", err)
	}
	t.Cleanup(remoteCloser)

	proxyClient, proxyCloser, err := plugin.ServeInProcess(proxy.New(remoteClient))
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
	if info.Kind != "localdb" || info.RootGridId == "" {
		t.Fatalf("Info through proxy = %+v, want the remote's", info)
	}

	// A create + read round trip, end to end through the proxy.
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
		Data:   []byte("# remote"),
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	body, err := c.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if string(body.Data) != "# remote" {
		t.Errorf("content through proxy = %q, want %q", body.Data, "# remote")
	}
}

func TestProxy_SessionRoundTrips(t *testing.T) {
	c := proxied(t)
	ctx := context.Background()

	// PutSession (client-stream) through the proxy.
	put, err := c.PutSession(ctx)
	if err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := put.Send(&gridwellv1.PutSessionRequest{RootGridId: "r", Data: []byte("cookies")}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := put.CloseAndRecv(); err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}

	// GetSession (server-stream) back through the proxy.
	get, err := c.GetSession(ctx, &gridwellv1.GetSessionRequest{})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	var got []byte
	for {
		chunk, err := get.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		got = append(got, chunk.Data...)
	}
	if string(got) != "cookies" {
		t.Errorf("session through proxy = %q, want %q", got, "cookies")
	}
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
