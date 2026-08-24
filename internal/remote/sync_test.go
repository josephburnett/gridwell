package remote

// SyncConfig: server.yaml owns the connection set (v2 #269, reversing
// #199). These pin the reconcile semantics — names verbatim, idempotence
// (no version churn on a no-op boot), tombstone on removal, retirement
// forever — and the config-mode refusals on every mutating verb.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/remote/dial"
)

func openSyncDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func specs() []ConnSpec {
	return []ConnSpec{
		{Name: "geneva1", Label: "geneva", Addr: "127.0.0.1:10011"},
		{Name: "rtbhome", Host: "192.168.88.5", User: "joe", Key: "~/.ssh/rtb.local"},
	}
}

func TestSyncMaterializesNamesVerbatim(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	live, err := SyncConfig(ctx, db, specs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[0] != "geneva1" || live[1] != "rtbhome" {
		t.Fatalf("live = %v", live)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || g.Deleted {
		t.Fatalf("geneva1 row: %+v err=%v", g, err)
	}
	if g.AltText != "geneva" {
		t.Fatalf("label = %q, want geneva", g.AltText)
	}
	p, err := ParseParams([]byte(g.Params))
	if err != nil || p.Addr != "127.0.0.1:10011" || p.Host != "" {
		t.Fatalf("params: %+v err=%v", p, err)
	}
	r, _ := db.GetByNS(ctx, "rtbhome")
	if r.AltText != "joe@192.168.88.5" {
		t.Fatalf("auto label = %q", r.AltText)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetByNS(ctx, "geneva1")
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetByNS(ctx, "geneva1")
	if after.Version != before.Version {
		t.Fatalf("a no-op boot churned the version: %d → %d", before.Version, after.Version)
	}
}

func TestSyncParamsChangeUpdates(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	changed := specs()
	changed[0].Addr = "127.0.0.1:20022"
	if _, err := SyncConfig(ctx, db, changed, nil); err != nil {
		t.Fatal(err)
	}
	g, _ := db.GetByNS(ctx, "geneva1")
	p, _ := ParseParams([]byte(g.Params))
	if p.Addr != "127.0.0.1:20022" {
		t.Fatalf("params not updated: %+v", p)
	}
	if g.RemoteRoot != "" {
		t.Fatal("params change must clear the remote-root cache (may name another machine)")
	}
}

func TestSyncRemovalRetiresForever(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	// geneva1 leaves the config: tombstoned.
	if _, err := SyncConfig(ctx, db, specs()[1:], nil); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || !g.Deleted {
		t.Fatalf("removed connection not tombstoned: %+v err=%v", g, err)
	}
	// The name never returns.
	if _, err := SyncConfig(ctx, db, specs(), nil); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("reusing a retired name not refused: %v", err)
	}
}

func TestSyncRetiredNamesReserveOnFreshDB(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs()[1:], []string{"geneva1"}); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || !g.Deleted {
		t.Fatalf("retired name not reserved: %+v err=%v", g, err)
	}
	// Declaring a retired name is refused up front.
	if _, err := SyncConfig(ctx, db, specs(), []string{"geneva1"}); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("declared retired name not refused: %v", err)
	}
}

func TestSyncRefusesBadNames(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "has/slash", Addr: "x:1"}}, nil); err == nil {
		t.Fatal("slash name accepted")
	}
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "a1", Addr: "x:1"}, {Name: "a1", Addr: "y:1"}}, nil); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "noaddr"}}, nil); err == nil {
		t.Fatal("empty params accepted (need addr or host)")
	}
}

func TestSyncConfigLabelLatches(t *testing.T) {
	// A config label is the USER speaking (yaml is theirs): it must latch
	// alt_user, or connTile's auto-label override displays joe@host
	// instead of the declared label (the work-laptop cutover bug,
	// 2026-08-23).
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.AltUser {
		t.Fatal("config label did not latch alt_user — the display falls back to the auto-label")
	}
	if g.AltText != "geneva" {
		t.Fatalf("alt = %q, want the config label", g.AltText)
	}
	// No label declared: the auto-label rules, unlatched.
	r, _ := db.GetByNS(ctx, "rtbhome")
	if r.AltUser {
		t.Fatal("an auto-labeled connection must stay unlatched")
	}
}

func TestNoCreationSchemaEver(t *testing.T) {
	// The instance picker is deleted (2026-08-23): the transport never
	// declares a creation schema — a form that could only fail must
	// never render, in any mode.
	db := openSyncDB(t)
	s := New(db, nil, "")
	for _, mode := range []bool{false, true} {
		s.SetConfigMode(mode)
		info, err := s.Info(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(info.CreateSchemas) != 0 {
			t.Fatalf("configMode=%v declared a creation schema: %v", mode, info.CreateSchemas)
		}
	}
}

// fakeRemote answers the two calls learnRoot makes.
type fakeRemote struct {
	gridwellv1.UnimplementedGridwellServer
}

func (fakeRemote) ListPlugins(context.Context, *gridwellv1.ListPluginsRequest) (*gridwellv1.ListPluginsResponse, error) {
	return &gridwellv1.ListPluginsResponse{Plugins: []*gridwellv1.PluginInfo{
		{Uuid: "farplug1", RootGridId: "farplug1/1"},
	}}, nil
}

// ConnectAll (Joe, 2026-08-23: the boot doesn't serve mysteries): a
// reachable connection is LIVE with its root persisted before the node
// serves; an unreachable one has its error recorded (the wire status)
// before the node serves.
func TestConnectAllAtBoot(t *testing.T) {
	ctx := context.Background()
	db := openSyncDB(t)
	if _, err := SyncConfig(ctx, db, []ConnSpec{
		{Name: "goodcon", Label: "good", Addr: "127.0.0.1:1"},
		{Name: "deadcon", Label: "dead", Addr: "127.0.0.1:2"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	client, closer, err := compose.ServeInProcess(fakeRemote{})
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	dialer := func(cfg dial.Config) (gridwellv1.GridwellClient, func(), error) {
		if cfg.Addr == "127.0.0.1:1" {
			return client, func() {}, nil
		}
		return nil, nil, fmt.Errorf("dial %s: connection refused", cfg.Addr)
	}
	s := New(db, dialer, "")
	s.SetConfigMode(true)
	s.ConnectAll(ctx)

	good, _ := db.GetByNS(ctx, "goodcon")
	if good.RemoteRoot != "farplug1/1" {
		t.Fatalf("good connection root = %q, want learned before serving", good.RemoteRoot)
	}
	s.mu.Lock()
	deadErr := s.rootErr["deadcon"]
	s.mu.Unlock()
	if !strings.Contains(deadErr, "connection refused") {
		t.Fatalf("dead connection's recorded error = %q, want the dial failure", deadErr)
	}
}

// The connection-row framing round trip (found 2026-08-23): ascending a
// menu-row portal writes SetRootView at the connection's root chain. The
// DOOR (the connection row) owns that viewport — it must land on the
// row's view_* (what the menu row's root_view serves back), and must NOT
// forward to the far node (whose own landing framing belongs to its own
// clients).
func TestConnectionRootFramingRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openSyncDB(t)
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "framecon", Addr: "127.0.0.1:1"}}, nil); err != nil {
		t.Fatal(err)
	}
	farCalls := 0
	client, closer, err := compose.ServeInProcess(countingRemote{calls: &farCalls})
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	s := New(db, func(dial.Config) (gridwellv1.GridwellClient, func(), error) {
		return client, func() {}, nil
	}, "")
	s.SetConfigMode(true)
	s.ConnectAll(ctx) // learns the root: farplug1/1

	if _, err := s.SetRootView(ctx, &gridwellv1.SetRootViewRequest{
		RootGridId: "framecon/farplug1/1", Cx: 7, Cy: -3, Zoom: 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetByNS(ctx, "framecon")
	if err != nil {
		t.Fatal(err)
	}
	if c.ViewX != 7 || c.ViewY != -3 || c.ViewZoom != 1.5 {
		t.Fatalf("the door's viewport didn't persist: %+v", c)
	}
	if farCalls != 0 {
		t.Fatalf("the far node's landing framing was written %d times — the door owns this viewport", farCalls)
	}
	// The menu row serves it back (tileFromConn view_* is what
	// instanceRows reads).
	tile := tileFromConn(c)
	if tile.ViewX != 7 || tile.ViewZoom != 1.5 {
		t.Fatalf("round trip lost on the read side: %+v", tile)
	}
}

// countingRemote counts SetRootView arrivals; answers the connect/learn
// calls like fakeRemote.
type countingRemote struct {
	gridwellv1.UnimplementedGridwellServer
	calls *int
}

func (countingRemote) ListPlugins(context.Context, *gridwellv1.ListPluginsRequest) (*gridwellv1.ListPluginsResponse, error) {
	return &gridwellv1.ListPluginsResponse{Plugins: []*gridwellv1.PluginInfo{
		{Uuid: "farplug1", RootGridId: "farplug1/1"},
	}}, nil
}

func (r countingRemote) SetRootView(context.Context, *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	*r.calls++
	return &gridwellv1.SetRootViewResponse{}, nil
}

func TestConnectAllIsBounded(t *testing.T) {
	// A hanging dial delays boot by bootDialWait at most — never bricks
	// it (the dial keeps trying in the background).
	ctx := context.Background()
	db := openSyncDB(t)
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "hangcon", Addr: "127.0.0.1:9"}}, nil); err != nil {
		t.Fatal(err)
	}
	old := bootDialWait
	bootDialWait = 50 * time.Millisecond
	t.Cleanup(func() { bootDialWait = old })
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s := New(db, func(dial.Config) (gridwellv1.GridwellClient, func(), error) {
		<-block // a black-holing network
		return nil, nil, fmt.Errorf("never")
	}, "")
	start := time.Now()
	s.ConnectAll(ctx)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("boot blocked %v on a hanging dial — the bound failed", el)
	}
}
