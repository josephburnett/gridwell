package sourcecache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/trace"
)

func traceMark() uint64 {
	recs := trace.Default().Snapshot()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

// traced is every sourcecache record of kind since mark that ok accepts.
func traced(mark uint64, kind string, ok func(tracewire.Record) bool) []tracewire.Record {
	var out []tracewire.Record
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "sourcecache" && rec.Kind == kind && ok(rec) {
			out = append(out, rec)
		}
	}
	return out
}

// awaitTraced waits for a record of kind since mark that ok accepts.
func awaitTraced(t *testing.T, mark uint64, kind string, ok func(tracewire.Record) bool) tracewire.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if recs := traced(mark, kind, ok); len(recs) > 0 {
			return recs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no sourcecache/%s record arrived", kind)
	return tracewire.Record{}
}

// How a grid read was answered, and what each refresh behind it found, are
// records: a dump otherwise shows the same "GetGrid ok" for a fresh hit, a
// stale answer and the source's own word.
func TestEachGridServeAndRevalidationIsTraced(t *testing.T) {
	cc, far, root, _ := fixture(t)
	ctx := context.Background()
	mark := traceMark()
	ofRoot := func(r tracewire.Record) bool { return r.KV["grid"] == root }
	read := func() {
		t.Helper()
		if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
			t.Fatal(err)
		}
	}

	read() // miss
	read() // hit
	ageGrid(t, cc, root)
	read() // cached, revalidates: same
	awaitTraced(t, mark, "revalidate", func(r tracewire.Record) bool { return ofRoot(r) && r.KV["verdict"] == "same" })
	if _, err := far.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	ageGrid(t, cc, root)
	read() // cached, revalidates: changed
	awaitTraced(t, mark, "revalidate", func(r tracewire.Record) bool { return ofRoot(r) && r.KV["verdict"] == "changed" })

	var hows []string
	for _, r := range traced(mark, "serve", ofRoot) {
		hows = append(hows, r.KV["how"])
	}
	if want := []string{"miss", "hit", "cached", "cached"}; len(hows) != len(want) || hows[0] != want[0] ||
		hows[1] != want[1] || hows[2] != want[2] || hows[3] != want[3] {
		t.Errorf("serves = %v, want %v", hows, want)
	}
}

// A source's verdict that a cached grid is gone evicts it, and the record says
// so.
func TestAnEvictionIsTraced(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	up := &verdicted{Namespace: local.New(st, nil)}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	mark := traceMark()
	up.gone = true
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	awaitTraced(t, mark, "revalidate", func(r tracewire.Record) bool {
		return r.KV["grid"] == root && r.KV["verdict"] == "evicted"
	})
}

// rooted declares the far home as a connection row, the walk's root.
type rooted struct {
	*darkable
	root string
}

func (r rooted) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return &pb.HandshakeResponse{Plugins: []*pb.PluginInfo{rpc.ConnectionRow("u1", "", r.root, "", rpc.View{})}}, nil
}

// A source going dark and coming back is one record each with the reason, and
// the walk the return starts is a start and a done with its breadth.
func TestDarknessAndThePrefetchWalkAreTraced(t *testing.T) {
	_, far, root, dbPath := fixture(t)
	cc := openLayer(t, rooted{far, root}, dbPath+"-rooted", Options{Prefetch: true})
	ctx := context.Background()
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	mark := traceMark()
	far.goDark()
	_, _ = cc.GetTile(ctx, &pb.GetTileRequest{TileId: "zzzzzzz"})
	_, _ = cc.GetTile(ctx, &pb.GetTileRequest{TileId: "zzzzzzz"})
	far.goLive()
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: "zzzzzzz"}); err == nil {
		t.Fatal("an unknown tile answered")
	}
	dark := traced(mark, "dark", func(tracewire.Record) bool { return true })
	if len(dark) != 2 {
		t.Fatalf("dark records = %v, want one dark and one light", dark)
	}
	if d := dark[0]; d.KV["dark"] != "true" || d.KV["reason"] == "" {
		t.Errorf("dark record = %v, want dark with the reason", d)
	}
	if d := dark[1]; d.KV["dark"] != "false" || d.KV["reason"] != "" {
		t.Errorf("light record = %v", d)
	}
	awaitTraced(t, mark, "prefetch", func(r tracewire.Record) bool { return r.KV["state"] == "start" })
	done := awaitTraced(t, mark, "prefetch", func(r tracewire.Record) bool { return r.KV["state"] == "done" })
	if done.KV["grids"] == "" || done.KV["grids"] == "0" {
		t.Errorf("done record = %v, want the grids it walked", done)
	}
}
