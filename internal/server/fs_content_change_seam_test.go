package server_test

// A file's bytes changed on disk reach a client that shows them: the open
// file's body in a pane, a text tile's face in a shown grid, an image's face.
// The client half is the shim's own loop over its js-free owners: cache.Apply,
// events.Route, a refetch of the grid the plan names, and a ReadContent for a
// body the cache no longer holds (render.go fetchTileContent). Every shipped
// plugin's content-change seam test runs that client (newContentClientOf).

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/events"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// contentClient is a client as the shim runs one: its cache and its event
// stream.
type contentClient struct {
	t       *testing.T
	ctx     context.Context
	cl      *rpc.Client
	c       *cache.Cache
	events  chan *gridwellv1.Event
	landing string
	// changed counts the grid changes the client was told, and told the
	// rows whose bytes it was told moved, by id.
	changed int
	told    map[string]int
}

func newContentClient(t *testing.T, uuid, root string) *contentClient {
	t.Helper()
	return newContentClientVia(t, uuid, root, nil)
}

// newContentClientVia is newContentClient with the node reaching the fs
// adapter through via, nil for directly.
func newContentClientVia(t *testing.T, uuid, root string, via func(namespace.Namespace) namespace.Namespace) *contentClient {
	t.Helper()
	return newContentClientOf(t, uuid, "fs", map[string]string{"root": root}, via)
}

// newContentClientOf is newContentClientVia over the shipped plugin kind,
// spawned with cfg.
func newContentClientOf(t *testing.T, uuid, kind string, cfg map[string]string, via func(namespace.Namespace) namespace.Namespace) *contentClient {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, kind, cfg)
	a, stop := pluginhost.Start(cp, st.Namespace(uuid), nil, "plugin "+uuid+" watch")
	var ns namespace.Namespace = a
	if via != nil {
		ns = via(a)
	}
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(uuid, kind, ns, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k := &contentClient{t: t, ctx: ctx, cl: cl, c: cache.New(), events: make(chan *gridwellv1.Event, 256)}
	for _, p := range lp.Plugins {
		if p.Uuid == uuid {
			k.landing = plugintest.LandingOf(t, p)
		}
	}
	if k.landing == "" {
		t.Fatalf("no %s plugin in the handshake", kind)
	}
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			k.events <- ev
		}
	}()
	return k
}

func (k *contentClient) fetchGrid(id string) {
	k.t.Helper()
	g, err := k.cl.GetGrid(k.ctx, id)
	if err != nil {
		k.t.Fatal(err)
	}
	k.c.PutGrid(g.Grid, g.Tiles)
}

func (k *contentClient) tile(label string) *gridwellv1.Tile {
	k.t.Helper()
	return k.tileIn(k.landing, label)
}

func (k *contentClient) tileIn(grid, label string) *gridwellv1.Tile {
	k.t.Helper()
	g, ok := k.c.Grid(grid)
	if !ok {
		k.t.Fatalf("grid %s not cached", grid)
	}
	for _, n := range g.Tiles {
		if n.AltText == label {
			return n
		}
	}
	k.t.Fatalf("no tile %q in %s", label, grid)
	return nil
}

// body is what the screen shows for a text tile: the cached body, read when
// the cache holds none.
func (k *contentClient) body(id string) []byte {
	k.t.Helper()
	if b, ok := k.c.TileContent(id); ok {
		return b
	}
	asked := k.c.AskContent(id)
	data, _, version, err := k.cl.ReadContent(k.ctx, id)
	if err != nil {
		k.t.Fatal(err)
	}
	k.c.PutFetchedContent(id, data, version, asked)
	return data
}

// run applies every event for up to d as the shim does, until until holds:
// the cache folds the event and the route's refetch is read in.
func (k *contentClient) run(d time.Duration, until func() bool) bool {
	deadline := time.After(d)
	for {
		if until() {
			return true
		}
		select {
		case ev := <-k.events:
			k.c.Apply(ev)
			if ev.GetGridChanged() != nil {
				k.changed++
			}
			if tc := ev.GetTileChanged(); tc.GetContentChanged() {
				if k.told == nil {
					k.told = map[string]int{}
				}
				k.told[tc.GetTile().GetId()]++
			}
			if p := events.Route(ev); p.Fetch != "" {
				k.fetchGrid(p.Fetch)
			}
		case <-deadline:
			return until()
		}
	}
}

func (k *contentClient) settle() {
	k.run(time.Second, func() bool { return false })
}

// A text file open in a pane shows its new bytes once they change on disk.
// The pane tells the node it shows the grid the file sits in (pane.Showing).
func TestFsOpenFileShowsItsNewBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfsopen", root)
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("notes.md"))
	if got := k.body(id); string(got) != "# before\n" {
		t.Fatalf("open body %q", got)
	}
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()
	k.changed = 0

	if err := os.WriteFile(path, []byte("# after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return bytes.Equal(k.body(id), []byte("# after\n")) }) {
		t.Fatalf("the open file still shows %q five seconds after its bytes changed on disk (grid changes told: %d)", k.body(id), k.changed)
	}
}

// A text tile's face in a shown grid draws its new bytes once they change on
// disk: the face is drawn from the same cached body (render.go tileBody).
func TestFsTextFaceShowsItsNewBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "todo.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfsface", root)
	k.fetchGrid(k.landing)
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	id := rpc.ContentID(k.tile("todo.txt"))
	if got := k.body(id); string(got) != "one\n" {
		t.Fatalf("face body %q", got)
	}
	k.settle()
	k.changed = 0

	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return bytes.Equal(k.body(id), []byte("one\ntwo\n")) }) {
		t.Fatalf("the face still draws %q five seconds after its bytes changed on disk (grid changes told: %d)", k.body(id), k.changed)
	}
}

// An image's face in a shown grid is keyed by the file's mtime
// (Entry.preview_stamp), so an edited image is asked for its new picture.
func TestFsImageFaceShowsItsNewPicture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pic.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nA"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfsimg", root)
	k.fetchGrid(k.landing)
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	before := k.tile("pic.png").PreviewBlobId
	if before == 0 {
		t.Fatal("the image declares no picture")
	}
	k.settle()

	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nB"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return k.tile("pic.png").PreviewBlobId != before }) {
		t.Fatalf("the image's face key is still %d five seconds after it changed on disk", before)
	}
}
