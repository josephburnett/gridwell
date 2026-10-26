package pluginhost

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/trace"
)

// traceMark is the newest seq on the node's ring, so a test reads only what it
// caused.
func traceMark() uint64 {
	recs := trace.Default().Snapshot()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

// traced is every pluginhost record of kind since mark.
func traced(mark uint64, kind string) []tracewire.Record {
	var out []tracewire.Record
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "pluginhost" && rec.Kind == kind {
			out = append(out, rec)
		}
	}
	return out
}

// awaitTraced waits for the first record of kind since mark that ok accepts.
func awaitTraced(t *testing.T, mark uint64, kind string, ok func(tracewire.Record) bool) tracewire.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range traced(mark, kind) {
			if ok(rec) {
				return rec
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pluginhost/%s record arrived; have %v", kind, traced(mark, kind))
	return tracewire.Record{}
}

// cacheAnswering lists "r" from its cache with the reason set while
// unreachable is. It declares Watch, so its health is its listings' alone.
type cacheAnswering struct {
	oneEntryPlugin
	unreachable string
}

func (p *cacheAnswering) Info(ctx context.Context, req *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	resp, err := p.oneEntryPlugin.Info(ctx, req)
	if err == nil {
		resp.Watch = true
	}
	return resp, err
}

func (p *cacheAnswering) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Unreachable: p.unreachable,
		Entries: []*pluginv1.Entry{{Key: "a", Kind: "text", Label: "a"}}}, nil
}

// A read served from the plugin's own cache, and the source going dark and
// coming back, each leave a record naming the plugin: a dump otherwise shows
// a read that succeeded and an event keyed by no plugin.
func TestACachedReadAndTheSourcesHealthAreTraced(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	src := &cacheAnswering{unreachable: "the source is offline"}
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := New(cp, st.Namespace("ptrace1"), nil)

	mark := traceMark()
	read(t, a, "all")
	src.unreachable = ""
	read(t, a, "all")

	serves := traced(mark, "serve")
	if len(serves) != 1 || serves[0].KV["plugin"] != "ptrace1" || serves[0].KV["cached"] != "1" ||
		serves[0].KV["grid"] != rpc.EntryGridID("all") {
		t.Errorf("serve records = %v, want one cached serve of the grid naming the plugin", serves)
	}
	health := traced(mark, "health")
	if len(health) != 2 {
		t.Fatalf("health records = %v, want dark then live", health)
	}
	if h := health[0]; h.KV["plugin"] != "ptrace1" || h.KV["dark"] != "true" || h.KV["detail"] != "the source is offline" {
		t.Errorf("dark record = %v", h)
	}
	if h := health[1]; h.KV["dark"] != "false" || h.KV["detail"] != "" {
		t.Errorf("live record = %v", h)
	}
}

// Each Watch stream the node holds on a plugin, and each change the plugin
// tells on it, is a record, so a stream nobody needed or a storm of changes
// shows in a dump.
func TestWatchStreamsAndChangesAreTraced(t *testing.T) {
	tell := make(chan struct{})
	p := newListingPlugin(map[string][]string{"all": {"a"}},
		func(_ int32, ctx context.Context, send func(*pluginv1.Change) error) error {
			select {
			case <-tell:
			case <-ctx.Done():
				return nil
			}
			if err := send(gridChanged("all")); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
	mark := traceMark()
	a, _ := watching(t, p, nil)
	grid := rpc.EntryGridID("all")
	awaitTraced(t, mark, "watch", func(r tracewire.Record) bool {
		return r.KV["state"] == "open" && r.KV["grids"] == grid && r.KV["plugin"] == "p1"
	})
	close(tell)
	awaitTraced(t, mark, "changed", func(r tracewire.Record) bool {
		return r.KV["grid"] == grid && r.KV["plugin"] == "p1"
	})
	show(t, a)
	awaitTraced(t, mark, "watch", func(r tracewire.Record) bool {
		return r.KV["state"] == "close" && r.KV["grids"] == grid
	})
}
