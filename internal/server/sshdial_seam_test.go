package server_test

import (
	"context"
	"fmt"
	"github.com/josephburnett/gridwell/internal/namespace"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/plugin"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/remote/dial/dialtest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// This is the ssh plugin's REAL transport seam, in-process: a genuine
// x/crypto/ssh server (public-key auth, host-key verification against a
// known_hosts file, direct-tcpip channel forwarding) in front of a genuine
// `gridwell serve` node handler (h2c + id-routed node export +
// TWO in-process localdbs). dial.Dial crosses every layer the production
// binary crosses except the network itself.

// remoteNode stands up the "remote gridwell serve": node id "rnode" with two
// localdb plugins ("personal", "work") behind FederationHandler on a real listener.
// Returns its address and a direct client to the first plugin for
// ground-truth assertions.
func remoteNode(t *testing.T) (string, namespace.Namespace) {
	t.Helper()
	reg := plugin.NewRegistry()
	for i, name := range []string{"personal", "work"} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		t.Cleanup(func() { st.Close() })
		direct := local.New(st, shellsvc.NewManager(shellsvctest.New()))
		uuid := fmt.Sprintf("ur%d", i+1)
		reg.Register(uuid, "home", direct, nil)
		reg.SetLabel(uuid, name)
	}
	direct, _ := reg.Get("ur1")
	srv := servertest.New(t, reg, server.Config{})
	// The federation door is a unix socket (2026-08-26); the test sshd
	// forwards direct-streamlocal to it, exactly like a real sshd.
	sock := filepath.Join(t.TempDir(), "federation.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.FederationHandler(), Protocols: server.NodeProtocols()}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })
	return sock, direct
}

// dialThroughSSH assembles the full topology and returns the tunneled client.
func dialThroughSSH(t *testing.T) (namespace.Namespace, namespace.Namespace, error) {
	t.Helper()
	nodeAddr, direct := remoteNode(t)
	creds := dialtest.Server(t, t.TempDir())

	client, dialClose, err := dial.Dial(dial.Config{
		Host:       creds.Addr,
		User:       "joe",
		KeyPath:    creds.KeyPath,
		KnownHosts: creds.KnownHostsPath,
		Addr:       nodeAddr,
	})
	if err != nil {
		return nil, direct, err
	}
	t.Cleanup(dialClose)
	return client, direct, nil
}

func TestDialMountsRemoteNodeThroughRealSSH(t *testing.T) {
	c, direct, err := dialThroughSSH(t)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	// The mount's root is the remote's HOME (its first rooted plugin) —
	// where a direct client of that node lands too. The remote's plugin
	// list rides the same tunnel, labels intact.
	info, err := c.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("tunneled Info: %v", err)
	}
	if !strings.HasPrefix(info.RootGridId, "ur1/") {
		t.Fatalf("tunneled root = %q, want the remote home ur1/<n>", info.RootGridId)
	}
	lp, err := c.Handshake(ctx, &gridwellv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("tunneled Handshake: %v", err)
	}
	if len(lp.Plugins) != 2 || lp.Plugins[0].Label != "personal" || lp.Plugins[1].Label != "work" {
		t.Fatalf("remote plugins = %+v, want personal,work", lp.Plugins)
	}

	// Write at the remote's home; read back directly on the remote: the
	// mount is the same plugin, hop peeled.
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
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
	got := readAllChunks(t, ctx, direct, strings.TrimPrefix(created.Tile.Id, "ur1/"))
	if string(got) != "# over ssh" {
		t.Errorf("content = %q, want %q", got, "# over ssh")
	}
}

// writeThenRead drives the contracted content surface: WriteContent commits
// the bytes (version claimed from the created row), ReadContent streams them
// back. The test helper for what CreateTile.data used to carry.
func writeThenRead(t *testing.T, ctx context.Context, c namespace.Namespace, tileID string, version int64, data []byte) []byte {
	t.Helper()
	sent := false
	if _, err := c.WriteContent(ctx, func() (*gridwellv1.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &gridwellv1.WriteContentRequest{TileId: tileID, Version: version, Data: data}, nil
	}); err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	return readAllChunks(t, ctx, c, tileID)
}

// readAllChunks drains a namespace's ReadContent into one value.
func readAllChunks(t *testing.T, ctx context.Context, c namespace.Namespace, tileID string) []byte {
	t.Helper()
	var out []byte
	if err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: tileID},
		func(chunk *gridwellv1.ContentChunk) error { out = append(out, chunk.Data...); return nil }); err != nil {
		t.Fatalf("ReadContent: %v", err)
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

	client, dialClose, err := dial.Dial(dial.Config{
		Host:       creds.Addr,
		User:       "joe",
		KeyPath:    creds.KeyPath,
		KnownHosts: creds.KnownHostsPath,
		Addr:       nodeAddr,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(dialClose)
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
