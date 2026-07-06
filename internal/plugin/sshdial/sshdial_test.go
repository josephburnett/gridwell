package sshdial_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial/sshdialtest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
)

// This is the ssh plugin's REAL transport seam, in-process: a genuine
// x/crypto/ssh server (public-key auth, host-key verification against a
// known_hosts file, direct-tcpip channel forwarding) in front of a genuine
// `gridwell serve` node handler (h2c + id-routed node export + node grid +
// TWO in-process localdbs). sshdial.Dial crosses every layer the production
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
		direct, closer, err := plugin.ServeInProcess(localdb.New(st, shellsvc.NewManager(shellsvctest.New())))
		if err != nil {
			t.Fatalf("serve localdb: %v", err)
		}
		t.Cleanup(closer)
		uuid := fmt.Sprintf("ur%d", i+1)
		reg.Register(uuid, "localdb", direct, nil)
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
	creds := sshdialtest.Server(t, t.TempDir())

	client, closer, err := sshdial.Dial(sshdial.Config{
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
		Data:   []byte("# over ssh"),
	})
	if err != nil {
		t.Fatalf("CreateTile through tunnel: %v", err)
	}
	if !strings.HasPrefix(created.Tile.Id, "ur1/") {
		t.Fatalf("created id = %q, want the remote's qualified ur1/<n>", created.Tile.Id)
	}
	body, err := direct.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: strings.TrimPrefix(created.Tile.Id, "ur1/")})
	if err != nil {
		t.Fatalf("direct read: %v", err)
	}
	if string(body.Data) != "# over ssh" {
		t.Errorf("content = %q, want %q", body.Data, "# over ssh")
	}

	// The SECOND remote plugin is reachable through the same mount — the
	// whole point of the node design: no per-plugin config, no selector —
	// and its grid carries the remote plugin's capabilities: writable, and
	// the scratch grid id qualified from the REMOTE's view (the local server
	// prepends the transit segment; here we sit inside the tunnel, so one
	// segment: "<remote-plugin>/<id>").
	pg, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: ng.Tiles[1].ChildGridId})
	if err != nil {
		t.Fatalf("second remote plugin unreachable through the mount: %v", err)
	}
	if !pg.Grid.Writable {
		t.Error("remote localdb grid must arrive writable")
	}
	if !strings.HasPrefix(pg.Grid.ScratchGridId, "ur2/") {
		t.Errorf("remote scratch = %q, want the remote's qualified ur2/<id>", pg.Grid.ScratchGridId)
	}
}

func TestFromPluginConfigNamesEveryMissingKey(t *testing.T) {
	_, err := sshdial.FromPluginConfig(map[string]string{"host": "h:22"})
	if err == nil {
		t.Fatalf("want error for missing keys")
	}
	// The exact missing-key list: every absent key named, the provided one not.
	msg := err.Error()
	list := msg[strings.Index(msg, ": ")+2:]
	if list != "user, key, known_hosts, addr" {
		t.Errorf("missing keys = %q, want %q", list, "user, key, known_hosts, addr")
	}
	// A complete config passes; an obsolete remote_plugin key is tolerated
	// (warned, not fatal) so yesterday's configs keep starting.
	if _, err := sshdial.FromPluginConfig(map[string]string{
		"host": "h:22", "user": "u", "key": "/k", "known_hosts": "/kh", "addr": "127.0.0.1:8080",
		"remote_plugin": "obsolete",
	}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
}
