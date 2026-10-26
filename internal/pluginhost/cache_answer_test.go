package pluginhost_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

const outage = "hey cannot reach app.hey.com, so this is the mailbox as last seen."

// cachingSource lists "doc" and "other" live. Told to answer from its cache, it answers
// only "other" from the cache with unreachable set and claims authority anyway,
// and its probe calls every key gone: a node that took either for a verdict
// would retire "doc". It declares Watch, so its health is its listings' alone.
type cachingSource struct {
	pluginv1.UnimplementedPluginServer

	mu          sync.Mutex
	unreachable string
}

func (p *cachingSource) fromCache(why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unreachable = why
}

func (p *cachingSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "mem", DisplayName: "mem", Watch: true,
		Grids: []*pluginv1.PluginGrid{{Id: "r", Grid: "r"}}}, nil
}

func (p *cachingSource) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resp := &pluginv1.ListResponse{Authoritative: true, Unreachable: p.unreachable,
		Entries: []*pluginv1.Entry{{Key: "other", Kind: "text", Label: "other"}}}
	if p.unreachable == "" {
		resp.Entries = append(resp.Entries, &pluginv1.Entry{Key: "doc", Kind: "text", Label: "doc"})
	}
	return resp, nil
}

func (p *cachingSource) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
}

// A listing answered from the cache is served as a live one and its reason is
// the source's health: the rows read, nothing retires even when the cache
// claims authority, and the next live answer brings the source back up.
func TestACacheAnswerServesItsRowsAndReportsTheSourceDown(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	src := &cachingSource{}
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := pluginhost.New(cp, memStore.Namespace("p1"), nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	health := make(chan *gridwellv1.EventPluginHealth, 16)
	go func() {
		_ = a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			if h := ev.GetPluginHealth(); h != nil {
				health <- h
			}
			return nil
		})
	}()
	next := func() *gridwellv1.EventPluginHealth {
		t.Helper()
		select {
		case h := <-health:
			return h
		case <-time.After(10 * time.Second):
			t.Fatal("no health event")
			return nil
		}
	}

	grid := rpc.EntryGridID("r")
	doc := rpc.EntryTileID("r", "doc")
	if _, err := a.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: doc, X: 5, Y: 5, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	src.fromCache(outage)
	g, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid})
	if err != nil {
		t.Fatalf("a cache answer failed the read: %v", err)
	}
	labels := map[string]bool{}
	for _, tile := range g.Tiles {
		labels[tile.AltText] = true
	}
	if !labels["other"] || !labels["doc"] {
		t.Fatalf("the cache answer served %v, want the cached entry and the minted row", g.Tiles)
	}
	if h := next(); h.Healthy || h.Detail != outage {
		t.Fatalf("health = %+v, want down with the plugin's sentence", h)
	}
	if _, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: doc}); err != nil {
		t.Fatalf("a cache answer retired the minted row: %v", err)
	}

	src.fromCache("")
	if _, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid}); err != nil {
		t.Fatal(err)
	}
	// A subscriber attaching mid-publish may hear the down twice.
	h := next()
	for !h.Healthy && h.Detail == outage {
		h = next()
	}
	if !h.Healthy || h.Detail != "" {
		t.Fatalf("health = %+v, want up once a live answer lands", h)
	}
}
