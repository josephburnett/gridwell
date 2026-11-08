package files

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A subscriber that stalls still learns of every directory that changed
// while it stalled: the stream coalesces per directory and drops none.
func TestAStalledSubscriberLosesNoDirectory(t *testing.T) {
	f := newFixture(t)
	const dirs = 200
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall, release := make(chan struct{}), make(chan struct{})
	got := make(chan *pb.Event, 4*dirs)
	header := make(chan struct{})
	go func() {
		_ = f.s.Subscribe(ctx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			if ev == nil {
				close(header)
				return nil
			}
			select {
			case <-stall:
				<-release
			default:
			}
			got <- ev
			return nil
		})
	}()
	<-header
	close(stall)
	want := map[string]bool{}
	for i := range dirs {
		d := f.path("d" + strconv.Itoa(i))
		want[idOf(d)] = true
		f.s.emitGridChanged(d)
	}
	close(release)
	giveUp := time.After(20 * time.Second)
	for len(want) > 0 {
		select {
		case ev := <-got:
			delete(want, ev.GetGridChanged().GetGridId())
		case <-giveUp:
			t.Fatalf("%d of %d changed directories never arrived", len(want), dirs)
		}
	}
}

// A view that adds a directory loses no change to one it keeps, and one it
// adds whose listing is the one a client read announces nothing.
func TestAViewChangeLosesNothingAndAddsQuietly(t *testing.T) {
	f := newFixture(t)
	dir := f.mkdir("dir")
	f.grid(f.root)
	f.grid(dir)
	ev := f.events()
	f.show(f.root)
	f.show(f.root, dir)
	quiet(t, ev, "an unchanged directory a view added", func(e *pb.Event) bool { return e.GetGridChanged() != nil })
	f.write("a.txt", "a")
	await(t, ev, "the kept directory's change", gridChanged(f.root))
	f.write("dir/b.txt", "b")
	await(t, ev, "the added directory's change", gridChanged(dir))
}

// A file's bytes written in place move no name, so they are the file's
// TileChanged and never its directory's GridChanged.
func TestAWriteInPlaceIsNoGridChange(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.md", "one")
	f.grid(f.root)
	ev := f.events()
	f.show(f.root)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	await(t, ev, "the file's change", tileChanged(p))
	quiet(t, ev, "a write in place told as its directory", gridChanged(f.root))
}

// A page file's screenshot yields when the file's bytes move, so the
// preview is the file's own again; the freeze's screenshot stays.
func TestAPageScreenshotYieldsToAChangeUnlessFrozen(t *testing.T) {
	f := newFixture(t)
	page := f.write("doc.pdf", "%PDF-1.4\n")
	frozen := f.write("kept.pdf", "%PDF-1.4\n")
	ctx := context.Background()
	shot := []byte("\xff\xd8screenshot")
	for _, p := range []string{page, frozen} {
		if _, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: idOf(p), Tile: &pb.Tile{Kind: rpc.KindURL}, Preview: shot}); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	if _, err := f.s.SetTile(ctx, &pb.SetTileRequest{TileId: idOf(frozen), UrlFrozen: &yes}); err != nil {
		t.Fatal(err)
	}
	if k := f.tile(page).PreviewBlobId; k <= 0 {
		t.Fatalf("the screenshot does not key the preview: %d", k)
	}
	f.grid(f.root)
	ev := f.events()
	f.show(f.root)
	time.Sleep(10 * time.Millisecond)
	for _, p := range []string{page, frozen} {
		if err := os.WriteFile(p, []byte("%PDF-1.5 changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	awaitAll(t, ev, map[string]func(*pb.Event) bool{"the page": tileChanged(page), "the frozen page": tileChanged(frozen)})
	if k := f.tile(page).PreviewBlobId; k != 0 {
		t.Errorf("the changed page still keys its screenshot: %d", k)
	}
	if k := f.tile(frozen).PreviewBlobId; k <= 0 {
		t.Errorf("the frozen page lost its screenshot: %d", k)
	}
}

func TestPagePreviewStale(t *testing.T) {
	page := listed{Entry: store.Entry{Key: "/r/a.pdf", Kind: rpc.KindURL, ServesPage: true}}
	link := listed{Entry: store.Entry{Key: "/r/b.pdf", Kind: rpc.KindURL, ServesPage: true, LinkTarget: "/r/a.pdf"}}
	text := listed{Entry: store.Entry{Key: "/r/a.md", Kind: rpc.KindText}}
	for _, c := range []struct {
		name string
		e    listed
		row  *pb.Tile
		want bool
	}{
		{"a page's screenshot", page, &pb.Tile{PreviewBlobId: 7}, true},
		{"a frozen one", page, &pb.Tile{PreviewBlobId: 7, UrlFrozen: true}, false},
		{"no screenshot", page, &pb.Tile{}, false},
		{"a link, whose preview is its target's", link, &pb.Tile{PreviewBlobId: 7}, false},
		{"a text file", text, &pb.Tile{PreviewBlobId: 7}, false},
	} {
		if got := pagePreviewStale(c.e, c.row); got != c.want {
			t.Errorf("%s: pagePreviewStale = %v, want %v", c.name, got, c.want)
		}
	}
}

func healthTraced(mark uint64) []tracewire.Record {
	var out []tracewire.Record
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "files" && rec.Kind == "health" {
			out = append(out, rec)
		}
	}
	return out
}

// The files' health is told on its transitions only: to a subscriber
// arriving during an outage, once when it ends, never again for a listing
// that finds nothing new, and each transition leaves a trace record.
func TestHealthIsToldOnTransitionsAndTraced(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "a")
	f.grid(f.root)
	var mark uint64
	if recs := trace.Default().Snapshot(); len(recs) > 0 {
		mark = recs[len(recs)-1].Seq
	}
	moved := f.root + ".away"
	if err := os.Rename(f.root, moved); err != nil {
		t.Fatal(err)
	}
	f.grid(f.root)
	ev := f.events()
	if h := await(t, ev, "the outage, to a late subscriber", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth(); h.Healthy || h.Detail == "" {
		t.Fatalf("health = %+v", h)
	}
	if err := os.Rename(moved, f.root); err != nil {
		t.Fatal(err)
	}
	f.grid(f.root)
	if h := await(t, ev, "the recovery", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth(); !h.Healthy {
		t.Fatalf("health = %+v", h)
	}
	f.grid(f.root)
	quiet(t, ev, "a healthy listing told again", func(e *pb.Event) bool { return e.GetPluginHealth() != nil })
	recs := healthTraced(mark)
	if len(recs) != 2 || recs[0].KV["dark"] != "true" || recs[0].KV["detail"] == "" || recs[1].KV["dark"] != "false" {
		t.Errorf("health records = %v, want dark with its detail, then light", recs)
	}
}
