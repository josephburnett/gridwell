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

// rememberingSource lists "doc" and "other" live. Told to remember, it answers
// only "other" from memory with unreachable set and claims authority anyway,
// and its probe calls every key gone: a node that took either for a verdict
// would retire "doc".
type rememberingSource struct {
	pluginv1.UnimplementedPluginServer

	mu          sync.Mutex
	unreachable string
}

func (p *rememberingSource) remember(why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unreachable = why
}

func (p *rememberingSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "mem", DisplayName: "mem",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "r", Context: "r"}}}, nil
}

func (p *rememberingSource) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resp := &pluginv1.ListResponse{Authoritative: true, Unreachable: p.unreachable,
		Entries: []*pluginv1.Entry{{Key: "other", Kind: "text", Label: "other"}}}
	if p.unreachable == "" {
		resp.Entries = append(resp.Entries, &pluginv1.Entry{Key: "doc", Kind: "text", Label: "doc"})
	}
	return resp, nil
}

func (p *rememberingSource) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
}

// A listing answered from memory is served as a live one and its reason is
// the source's health: the rows read, nothing retires even when the memory
// claims authority, and the next live answer brings the source back up.
func TestAMemoryAnswerServesItsRowsAndReportsTheSourceDown(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	src := &rememberingSource{}
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
	if _, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: doc, X: 5, Y: 5, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	src.remember(outage)
	g, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid})
	if err != nil {
		t.Fatalf("a memory answer failed the read: %v", err)
	}
	labels := map[string]bool{}
	for _, tile := range g.Tiles {
		labels[tile.AltText] = true
	}
	if !labels["other"] || !labels["doc"] {
		t.Fatalf("memory answer served %v, want the remembered entry and the minted row", g.Tiles)
	}
	if h := next(); h.Healthy || h.Detail != outage {
		t.Fatalf("health = %+v, want down with the plugin's sentence", h)
	}
	if _, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: doc}); err != nil {
		t.Fatalf("a memory answer retired the minted row: %v", err)
	}

	src.remember("")
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
