package server_test

import (
	"context"
	"fmt"
	"github.com/josephburnett/gridwell/internal/plugin"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/remote/dial/dialtest"
	"github.com/josephburnett/gridwell/internal/server"
)

// This is the ssh plugin's REAL transport seam, in-process: a genuine
// x/crypto/ssh server (public-key auth, host-key verification against a
// known_hosts file, direct-tcpip channel forwarding) in front of a genuine
// `gridwell serve` node handler (h2c + id-routed node export + node grid +
// TWO in-process localdbs). dial.Dial crosses every layer the production
// binary crosses except the network itself.

// remoteNode stands up the "remote gridwell serve": node id "rnode" with two
// localdb plugins ("personal", "work") behind NodeHandler on a real listener.
// Returns its address and a direct client to the first plugin for
// ground-truth assertions.
func remoteNode(t *testing.T) (string, gridwellv1.GridwellClient) {
	t.Helper()
	reg := plugin.NewRegistry()
	for i, name := range []string{"personal", "work"} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		t.Cleanup(func() { st.Close() })
		direct, closer, err := compose.ServeInProcess(local.New(st, shellsvc.NewManager(shellsvctest.New())))
		if err != nil {
			t.Fatalf("serve localdb: %v", err)
		}
		t.Cleanup(closer)
		uuid := fmt.Sprintf("ur%d", i+1)
		reg.Register(uuid, "local", direct, nil)
		reg.SetLabel(uuid, name)
	}
	direct, _ := reg.Get("ur1")
	srv := server.New(reg, server.Config{NodeID: "rnode"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.NodeHandler()}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })
	return ln.Addr().String(), direct
}

// dialThroughSSH assembles the full topology and returns the tunneled client.
func dialThroughSSH(t *testing.T) (gridwellv1.GridwellClient, gridwellv1.GridwellClient, error) {
	t.Helper()
	nodeAddr, direct := remoteNode(t)
	creds := dialtest.Server(t, t.TempDir())

	client, closer, err := dial.Dial(dial.Config{
		Host:       creds.Addr,
		User:       "joe",
		KeyPath:    creds.KeyPath,
		KnownHosts: creds.KnownHostsPath,
		Addr:       nodeAddr,
	})
	if err != nil {
		return nil, direct, err
	}
	t.Cleanup(closer)
	return client, direct, nil
}

func TestDialMountsRemoteNodeThroughRealSSH(t *testing.T) {
	c, direct, err := dialThroughSSH(t)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	// The mount's root is the remote's node grid: both remote plugins appear
	// as link tiles, labels intact.
	info, err := c.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("tunneled Info: %v", err)
	}
	if info.RootGridId != "rnode/0" {
		t.Fatalf("tunneled root = %q, want the remote node grid rnode/0", info.RootGridId)
	}
	ng, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid(remote node grid): %v", err)
	}
	if len(ng.Tiles) != 2 {
		t.Fatalf("remote node grid has %d tiles, want 2 (both remote plugins)", len(ng.Tiles))
	}
	if ng.Tiles[0].AltText != "personal" || ng.Tiles[1].AltText != "work" {
		t.Errorf("labels = %q,%q — want personal,work", ng.Tiles[0].AltText, ng.Tiles[1].AltText)
	}
	if !ng.Tiles[0].Reference {
		t.Error("remote plugin tiles must be links (dashed)")
	}

	// Descend into the FIRST remote plugin through its link and write; read
	// back directly on the remote: the mount is the same plugin, hop peeled.
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: ng.Tiles[0].ChildGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile through tunnel: %v", err)
	}
	if !strings.HasPrefix(created.Tile.Id, "ur1/") {
		t.Fatalf("created id = %q, want the remote's qualified ur1/<n>", created.Tile.Id)
	}
	// The body follows through the tunnel's WriteContent, then reads back
	// DIRECTLY on the remote — the mount is the same plugin, hop peeled.
	_ = writeThenRead(t, ctx, c, created.Tile.Id, created.Tile.Version, []byte("# over ssh"))
	r, err := direct.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: strings.TrimPrefix(created.Tile.Id, "ur1/")})
	if err != nil {
		t.Fatalf("direct read: %v", err)
	}
	var got []byte
	for {
		chunk, rerr := r.Recv()
		if rerr != nil {
			break
		}
		got = append(got, chunk.Data...)
	}
	if string(got) != "# over ssh" {
		t.Errorf("content = %q, want %q", got, "# over ssh")
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

// TestTunnelRecoversAfterSSHDeath is the outage seam: the ssh session dies
// UNDER an established mount (laptop sleep, network change, remote sshd
// restart) and comes back. The mount must heal by itself — the original
// design captured one ssh.Client in the gRPC dialer at spawn, so every
// reconnect attempt tunneled through the dead session and the mount stayed
// dark until the plugin was restarted, while the server's fan-in retried
// forever against a transport that could never recover.
func TestTunnelRecoversAfterSSHDeath(t *testing.T) {
	nodeAddr, _ := remoteNode(t)
	creds, sshd := dialtest.Restartable(t, t.TempDir())

	client, closer, err := dial.Dial(dial.Config{
		Host:       creds.Addr,
		User:       "joe",
		KeyPath:    creds.KeyPath,
		KnownHosts: creds.KnownHostsPath,
		Addr:       nodeAddr,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(closer)
	ctx := context.Background()

	if _, err := client.Info(ctx, &gridwellv1.InfoRequest{}); err != nil {
		t.Fatalf("Info before outage: %v", err)
	}

	// The whole sshd goes away: listener AND the established session.
	sshd.Kill()

	// While it is down, RPCs must FAIL (loudly — that is what feeds the
	// fan-in health notice), not hang.
	failCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := client.Info(failCtx, &gridwellv1.InfoRequest{}); err == nil {
		cancel()
		t.Fatal("Info succeeded through a dead tunnel")
	}
	cancel()

	// The sshd returns on the same address. The mount must recover WITHOUT
	// any restart: gRPC's next reconnect calls the dialer, the dialer
	// re-establishes the ssh session. Poll within the reconnect backoff.
	sshd.Resume(t)
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, err := client.Info(ctx, &gridwellv1.InfoRequest{})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mount never recovered after the sshd returned: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
