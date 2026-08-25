package server_test

// The #199 seam test: the multi-connection ssh plugin composed with the REAL
// routing stack on both sides — a local server that transit-qualifies the
// plugin's responses, and a remote node reached through its real node export
// over h2c — with only the ssh transport itself faked (a Dialer returning an
// in-process client). It crosses every seam the feature adds:
//
//	client → local server → sshhost (peel <conn>) → remote node export
//	         (peel <plugin>) → remote localdb
//
// and asserts the ids that come back are correctly re-chained at each hop.

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/plugin"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/server"
)

// chainHarness wires remote node ⇐ sshhost plugin ⇐ local server.
type chainHarness struct {
	localCl   *rpc.Client               // the local node's front door
	remoteCl  *rpc.Client               // the remote node's own front door (for direct remote mutations)
	sshClient gridwellv1.GridwellClient // the sshhost plugin, in process (plugin-seam asserts)
	dialed    []dial.Config             // every config the fake dialer saw
	rootBare  string                    // the remote localdb's bare root grid id

	mu      sync.Mutex
	dialErr error // injected dial-construction failure (nil = dials succeed)
}

// setDialErr makes every subsequent dial CONSTRUCTION fail (nil heals it) —
// the injectable transport fault the status_detail pins need.
func (h *chainHarness) setDialErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dialErr = err
}

func (h *chainHarness) takeDialErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dialErr
}

func newChainHarness(t *testing.T) *chainHarness {
	t.Helper()
	ctx := context.Background()

	// The remote node: one localdb plugin behind a real node export over h2c.
	remoteStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteStore.Close() })
	remoteClient, remoteCloser, err := compose.ServeInProcess(local.New(remoteStore, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remoteCloser)
	remoteReg := plugin.NewRegistry()
	remoteReg.Register("rp1", "local", remoteClient, nil)
	remoteSrv := server.New(remoteReg, server.Config{NodeID: "rnodex"})
	remoteHTTP := httptest.NewUnstartedServer(nil)
	remoteHTTP.Config.Handler = remoteSrv.NodeHandler()
	remoteHTTP.Config.Protocols = server.NodeProtocols()
	remoteHTTP.EnableHTTP2 = true
	remoteHTTP.Start()
	t.Cleanup(remoteHTTP.Close)

	grpcConn, err := grpc.NewClient(strings.TrimPrefix(remoteHTTP.URL, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grpcConn.Close() })
	remoteExport := gridwellv1.NewGridwellClient(grpcConn)

	// The plugin under test, with the ssh transport faked out: every dial
	// lands on the one remote export, and the resolved config is recorded so
	// the params → dial.Config plumbing is assertable.
	h := &chainHarness{}
	db, err := remote.OpenDB(t.TempDir() + "/ssh.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := remote.New(db, func(cfg dial.Config) (gridwellv1.GridwellClient, func(), error) {
		h.dialed = append(h.dialed, cfg)
		if err := h.takeDialErr(); err != nil {
			return nil, nil, err
		}
		return remoteExport, func() {}, nil
	}, "")
	t.Cleanup(srv.Close)
	sshClient, sshCloser, err := compose.ServeInProcess(srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sshCloser)
	h.sshClient = sshClient

	// The local node, registering the plugin as kind "remote" — the transit
	// classification the real server applies.
	localReg := plugin.NewRegistry()
	localReg.Register("sshc", "remote", sshClient, nil)
	localReg.SetTransit("sshc", true) // the declaration the loader reads from Info in production
	localSrv := server.New(localReg, server.Config{NodeID: "lnodex"})
	localHTTP := httptest.NewServer(localSrv.Handler())
	t.Cleanup(localHTTP.Close)
	h.localCl = rpc.NewClient(localHTTP.Client(), localHTTP.URL, connect.WithProtoJSON())
	h.remoteCl = rpc.NewClient(remoteHTTP.Client(), remoteHTTP.URL, connect.WithProtoJSON())

	h.rootBare, err = remoteStore.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// connParams is a valid params document with explicit paths (no ~ defaults —
// the harness passes home="").
const connParams = `{"host":"rtb","user":"joe","port":2222,"addr":"10.0.0.5:9999","key":"/kp","known_hosts":"/kh"}`

func TestConnectionLifecycleThroughTheChain(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)

	// The plugin's root grid: writable, empty, declaring the #198 schema.
	g, err := h.localCl.GetGrid(ctx, "sshc/0")
	if err != nil {
		t.Fatal(err)
	}
	if g.Grid.Writable {
		t.Error("the instance grid must NOT read writable — that's the + palette gate, and instances are created through the picker, never the palette")
	}
	if len(g.Grid.CreateSchemas) != 0 {
		t.Fatalf("no creation schema anymore — the picker retired with v2 config-managed connections; got %v", g.Grid.CreateSchemas)
	}
	if len(g.Tiles) != 0 {
		t.Fatalf("fresh plugin: want 0 tiles, got %d", len(g.Tiles))
	}

	// Drop a connection well. It arrives dashed (Reference) and CHILDLESS —
	// no params yet, nothing to dial.
	well, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 2, Y: 3, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if well.ID != "sshc/1" {
		t.Fatalf("well id = %q, want sshc/1", well.ID)
	}
	if !well.Reference {
		t.Error("a connection well is a link (delete unlinks, the remote is shared not owned)")
	}
	if well.ChildGridID != "" {
		t.Errorf("no params yet: child must be empty, got %q", well.ChildGridID)
	}
	if len(h.dialed) != 0 {
		t.Fatalf("nothing may dial before params commit, dialed %v", h.dialed)
	}

	// Commit the params — the well's CONTENT, through the one content door.
	// This is a content edit: the version bumps.
	afterParams, err := h.localCl.WriteContent(ctx, well.ID, well.Version, []byte(connParams))
	if err != nil {
		t.Fatal(err)
	}
	if afterParams.Version != well.Version+1 {
		t.Errorf("params commit must bump: %d → %d", well.Version, afterParams.Version)
	}
	if afterParams.AltText != "joe@rtb" {
		t.Errorf("auto label = %q, want joe@rtb", afterParams.AltText)
	}

	// The plugin learns the remote's root in the background and the well
	// gains its child: <conn-ns>/<remote-root>, chained under the plugin.
	var child string
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, err := h.localCl.GetGrid(ctx, "sshc/0")
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) == 1 && g.Tiles[0].ChildGridID != "" {
			child = g.Tiles[0].ChildGridID
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("well never gained its child (remote root learn)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Exactly one dial config, resolved from the params.
	if len(h.dialed) == 0 {
		t.Fatal("params commit must construct the transport")
	}
	cfg := h.dialed[0]
	if cfg.Host != "rtb:2222" || cfg.User != "joe" || cfg.KeyPath != "/kp" ||
		cfg.KnownHosts != "/kh" || cfg.Addr != "10.0.0.5:9999" {
		t.Errorf("dial config not plumbed from params: %+v", cfg)
	}
	// The child chains — and lands on the remote's HOME (remote-menu,
	// 2026-08-16: "when I descend into a node, I am there"): the first
	// rooted plugin's root grid, exactly where a direct client boots.
	// sshc is stripped by the server before the plugin saw the request,
	// so from the client the chain is sshc/<ns>/rp1/<root>.
	parts := strings.Split(child, "/")
	if len(parts) != 4 || parts[0] != "sshc" || parts[2] != "rp1" || parts[3] != h.rootBare {
		t.Fatalf("child = %q, want sshc/<conn-ns>/rp1/%s (the remote HOME, not its node grid)", child, h.rootBare)
	}
	ns := parts[1]
	if ns[0] < 'a' || ns[0] > 'z' {
		t.Errorf("minted connection segment %q must start with a letter (the URL grammar's namespace rule)", ns)
	}

	// The remote's NODE GRID stays addressable through the chain — it just
	// is not the landing page anymore (same rule as locally, 2026-07-19).
	nodeGrid, err := h.localCl.GetGrid(ctx, "sshc/"+ns+"/rnodex/0")
	if err != nil {
		t.Fatalf("GetGrid(node grid): %v", err)
	}
	if len(nodeGrid.Tiles) != 1 {
		t.Fatalf("remote node grid: want 1 plugin tile, got %d", len(nodeGrid.Tiles))
	}
	pluginTile := nodeGrid.Tiles[0]
	wantPluginChild := "sshc/" + ns + "/rp1/" + h.rootBare
	if pluginTile.ChildGridID != wantPluginChild {
		t.Fatalf("plugin link child = %q, want %q", pluginTile.ChildGridID, wantPluginChild)
	}
	if !pluginTile.Reference {
		t.Error("a node-grid plugin tile must stay a link through the chain (Reference verbatim)")
	}

	// The ROUTED plugin list (remote-menu): asking through the chain
	// answers the REMOTE node's plugins, ids re-qualified per hop and
	// node-local fields zeroed — the + menu inside a remote pane is
	// exactly what a direct client of that node would see.
	menu, err := h.localCl.ListPluginsNS(ctx, "sshc/"+ns)
	if err != nil {
		t.Fatalf("routed ListPlugins: %v", err)
	}
	if len(menu.Plugins) != 1 || menu.Plugins[0].RootGridID != wantPluginChild {
		t.Fatalf("routed menu = %+v, want one plugin rooted at %s", menu.Plugins, wantPluginChild)
	}
	if menu.Plugins[0].UUID != "sshc/"+ns+"/rp1" {
		t.Errorf("routed plugin uuid = %q, want the chain-qualified namespace", menu.Plugins[0].UUID)
	}
	if menu.ContentToken != "" || menu.NodeUUID != "" {
		t.Error("node-local fields must be ZEROED on a forwarded plugin list")
	}
	// The grid's node_ns names the serving node from this receiver — the
	// menu-context key the client routes by.
	homeGrid, err := h.localCl.GetGrid(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if homeGrid.Grid.NodeNS != "sshc/"+ns {
		t.Errorf("home grid node_ns = %q, want %q", homeGrid.Grid.NodeNS, "sshc/"+ns)
	}

	// Content crosses both peels: create a text tile in the remote localdb
	// through the chain, write bytes, read them back.
	text, err := h.localCl.CreateText(ctx, &rpc.CreateTextRequest{GridID: wantPluginChild, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text.ID, "sshc/"+ns+"/rp1/") {
		t.Fatalf("created text id = %q, want the full chain prefix", text.ID)
	}
	body := []byte("# across two namespace peels")
	afterWrite, err := h.localCl.WriteContent(ctx, text.ID, text.Version, body)
	if err != nil {
		t.Fatal(err)
	}
	got, _, gotVersion, err := h.localCl.ReadContent(ctx, text.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("read back %q, want %q", got, body)
	}
	if gotVersion != afterWrite.Version {
		t.Errorf("chunk-1 version = %d, want the committed %d (the save basis, never split)", gotVersion, afterWrite.Version)
	}

	// Version discipline on the connection well itself: framing never bumps,
	// rename bumps and latches over the auto label.
	framed, err := h.localCl.SetWellView(ctx, &rpc.SetWellViewRequest{
		TileID: well.ID, Version: afterParams.Version, ViewX: 7, ViewY: 8, ViewZoom: 2.5})
	if err != nil {
		t.Fatal(err)
	}
	if framed.Version != afterParams.Version {
		t.Errorf("framing bumped the version: %d → %d", afterParams.Version, framed.Version)
	}
	if framed.ViewX != 7 || framed.ViewY != 8 || framed.ViewZoom != 2.5 {
		t.Errorf("framing not persisted: %+v", framed)
	}
	renamed, err := h.localCl.RenameTile(ctx, well.ID, framed.Version, "rtb basement")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Version != framed.Version+1 {
		t.Errorf("rename must bump: %d → %d", framed.Version, renamed.Version)
	}
	if renamed.AltText != "rtb basement" {
		t.Errorf("rename lost: %q", renamed.AltText)
	}

	// Delete unlinks: the remote is untouched, the row tombstones, the
	// namespace answers "gone" — never a stranger.
	if err := h.localCl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: well.ID, Version: renamed.Version}); err != nil {
		t.Fatal(err)
	}
	g, err = h.localCl.GetGrid(ctx, "sshc/0")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 0 {
		t.Fatalf("after delete: want 0 tiles, got %d", len(g.Tiles))
	}
	if _, err := h.localCl.GetGrid(ctx, child); err == nil {
		t.Error("a deleted connection's namespace must stop resolving")
	}
	// The remote still has the text tile — delete never cascades over a link.
	rg, err := h.remoteCl.GetGrid(ctx, "rp1/"+h.rootBare)
	if err != nil {
		t.Fatal(err)
	}
	if len(rg.Tiles) != 1 {
		t.Errorf("the remote must be untouched by the unlink: want 1 tile, got %d", len(rg.Tiles))
	}
	// Probe: the tombstone is the ONLY gone verdict (a failed read must
	// never sweep a tile) — asserted at the plugin seam.
	pr, err := h.sshClient.Probe(ctx, &gridwellv1.ProbeRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Errorf("tombstoned connection: presence = %v, want GONE", pr.Presence)
	}
}

// TestUnreachableRemoteIsNotGone pins the sweep guard: a connection whose
// dial CONSTRUCTION fails (bad config) or whose params are absent answers
// with an error, never GONE — only a tombstone is gone.
func TestUnreachableRemoteIsNotGone(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)
	well, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = well
	// No params yet: a routed probe through the (nonexistent) namespace of a
	// LIVE but unconfigured connection errors; the local probe is PRESENT.
	pr, err := h.sshClient.Probe(ctx, &gridwellv1.ProbeRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("a live connection well must probe PRESENT, got %v", pr.Presence)
	}
}

// TestDialFailureSurfacesOnTheWell pins errors-must-surface for the picker's
// exact read path: while a committed connection cannot come up, the well the
// create-flow polls (GetTile) and the row the list shows (GetGrid) both carry
// the plugin's recorded failure as Tile.StatusDetail — through the plugin,
// the local server's transit qualification, and the Connect JSON — and the
// moment the transport heals, the child appears and the trouble clears. This
// was the "created, but the remote hasn't answered" dead end: the one message
// naming the problem died in kickRootFetch while the picker showed a shrug.
func TestDialFailureSurfacesOnTheWell(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)
	h.setDialErr(errors.New(`read key "~/.ssh/rtb.local": no such file or directory`))

	well, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The commit succeeds — the entry is durable; the failure is a STATUS,
	// never a refusal of valid params.
	if _, err := h.localCl.WriteContent(ctx, well.ID, well.Version, []byte(connParams)); err != nil {
		t.Fatalf("params commit must survive a dead transport: %v", err)
	}

	// The polled read carries the reason (recorded synchronously by the
	// commit's own kick, so the first poll already sees it).
	cur, err := h.localCl.GetTile(ctx, well.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cur.StatusDetail, "read key") {
		t.Fatalf("polled well StatusDetail = %q, want the dial error", cur.StatusDetail)
	}
	if cur.ChildGridID != "" {
		t.Fatalf("a failing connection must stay childless, got %q", cur.ChildGridID)
	}
	// The list row says the same (the picker's entries read).
	g, err := h.localCl.GetGrid(ctx, "sshc/0")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 || !strings.Contains(g.Tiles[0].StatusDetail, "read key") {
		t.Fatalf("list row must carry the failure, got %+v", g.Tiles)
	}

	// Heal the transport (same params): the next list read re-kicks the
	// learn; the child appears and the stale trouble clears with it.
	h.setDialErr(nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, err := h.localCl.GetGrid(ctx, "sshc/0")
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) == 1 && g.Tiles[0].ChildGridID != "" {
			if g.Tiles[0].StatusDetail != "" {
				t.Fatalf("a connected well must not keep old trouble: %q", g.Tiles[0].StatusDetail)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("connection never healed after the dial error cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRemoteEventsArrivePrefixed pins the per-connection fan-in: a change
// made directly on the remote node surfaces on the plugin's Subscribe stream
// with the connection segment prepended (the transit rule, one level down).
func TestRemoteEventsArrivePrefixed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newChainHarness(t)

	well, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.localCl.WriteContent(ctx, well.ID, well.Version, []byte(connParams)); err != nil {
		t.Fatal(err)
	}
	// Wait for the connection to come up (child backfilled = transport live).
	var ns string
	deadline := time.Now().Add(10 * time.Second)
	for ns == "" {
		g, err := h.localCl.GetGrid(ctx, "sshc/0")
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) == 1 && g.Tiles[0].ChildGridID != "" {
			ns = strings.Split(g.Tiles[0].ChildGridID, "/")[1]
		} else if time.Now().After(deadline) {
			t.Fatal("connection never came up")
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}

	stream, err := h.sshClient.Subscribe(ctx, &gridwellv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan *gridwellv1.Event, 16)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				close(events)
				return
			}
			events <- ev
		}
	}()

	// The remote-side subscription is established asynchronously; retry the
	// remote mutation until its event arrives prefixed.
	deadline = time.Now().Add(15 * time.Second)
	for {
		if _, err := h.remoteCl.CreateText(ctx, &rpc.CreateTextRequest{
			GridID: "rp1/" + h.rootBare, X: int64(time.Now().UnixNano() % 100), Y: 50, W: 1, H: 1}); err != nil {
			// Placement collisions across retries are fine — any remote
			// mutation that lands produces events.
			if time.Now().After(deadline) {
				t.Fatalf("remote mutation kept failing: %v", err)
			}
		}
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed")
			}
			if tc := ev.GetTileChanged(); tc != nil && strings.HasPrefix(tc.Tile.Id, ns+"/rp1/") {
				return // the prefixed remote event arrived
			}
			if gc := ev.GetGridChanged(); gc != nil && strings.HasPrefix(gc.GridId, ns+"/rp1/") {
				return
			}
			// Local events (the params TileChanged) and non-matching shapes
			// are skipped.
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("no prefixed remote event arrived")
		}
	}
}

// v2 (2026-08-23, retiring #251's picker): the transport hides behind
// its instances — zero connections, zero rows; each connection is a menu
// row of its own. The instance grid itself keeps serving under the same
// id (legacy links, the row synthesis).
func TestTransportHidesBehindItsInstances(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)
	rows := func() []rpc.PluginInfo {
		pls, err := h.localCl.ListPlugins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []rpc.PluginInfo
		for _, p := range pls.Plugins {
			if p.Kind == "remote" || strings.HasPrefix(p.UUID, "sshc/") {
				out = append(out, p)
			}
		}
		return out
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("an empty transport must list NO rows, got %+v", got)
	}
	// The instance grid (the storage address) still serves.
	if _, err := h.localCl.GetGrid(ctx, "sshc/0"); err != nil {
		t.Errorf("the instance grid must keep serving: %v", err)
	}
	// One connection → one row, the connection's own (pending: no params
	// committed, so no chain learned — rootless-inert, never blanked).
	if _, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1, Label: "gpu-box"}); err != nil {
		t.Fatal(err)
	}
	got := rows()
	if len(got) != 1 || got[0].Label != "gpu-box" || got[0].RootGridID != "" {
		t.Fatalf("want the connection's own pending row, got %+v", got)
	}
}

// The #251 dedup refusal: identical details ARE an existing connection.
// Canonical comparison (key order, empty values irrelevant); different
// details commit fine; and a TOMBSTONED connection never blocks reuse —
// recreating identical details after a delete mints a fresh segment.
func TestDuplicateParamsRefusedByName(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)

	first, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1, Label: "gpu-box"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.localCl.WriteContent(ctx, first.ID, first.Version, []byte(connParams)); err != nil {
		t.Fatal(err)
	}

	// Same details, different key order, plus an empty value: refused, and
	// the refusal NAMES the existing connection.
	dup := `{"user":"joe","host":"rtb","addr":"10.0.0.5:9999","port":2222,"known_hosts":"/kh","key":"/kp","extra":""}`
	second, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 3, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.localCl.WriteContent(ctx, second.ID, second.Version, []byte(dup))
	if err == nil {
		t.Fatal("identical params must be refused — one param-set, one connection")
	}
	if !strings.Contains(err.Error(), "gpu-box") {
		t.Errorf("the refusal must name the existing connection: %v", err)
	}

	// Different details are a different connection.
	other := `{"host":"other","user":"joe","key":"/kp","known_hosts":"/kh"}`
	if _, err := h.localCl.WriteContent(ctx, second.ID, second.Version, []byte(other)); err != nil {
		t.Fatalf("different params must commit: %v", err)
	}

	// Tombstone the first, then its details become mintable again (a NEW
	// segment — the old one is dead forever, which is the point).
	cur, err := h.localCl.GetTile(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.localCl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: first.ID, Version: cur.Version}); err != nil {
		t.Fatal(err)
	}
	third, err := h.localCl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: "sshc/0", X: 6, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.localCl.WriteContent(ctx, third.ID, third.Version, []byte(connParams)); err != nil {
		t.Fatalf("a tombstoned connection must not block recreating its details: %v", err)
	}
}

// The instance grid accepts only wells (a connection IS a well). Anything
// else is refused loudly — the refusal crosses the wire so the client's
// generic error surfacing (pinned by errsurface.spec.ts) can show it. This
// used to ride the schema-create e2e spec, whose land-on-the-grid flow the
// #251 flip retired.
func TestNonWellCreateRefused(t *testing.T) {
	ctx := context.Background()
	h := newChainHarness(t)
	_, err := h.localCl.CreateText(ctx, &rpc.CreateTextRequest{GridID: "sshc/0", X: 0, Y: 0, W: 1, H: 1})
	if err == nil {
		t.Fatal("a text create on the connection grid must be refused")
	}
	if !strings.Contains(err.Error(), "well") {
		t.Errorf("the refusal should say what the grid holds: %v", err)
	}
}
