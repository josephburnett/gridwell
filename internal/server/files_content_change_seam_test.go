package server_test

// A file's bytes changed on disk reach a client that shows them: the open
// file's body in a pane, a text tile's preview in a shown grid, an image's preview.
// The client half is the shim's own loop over its js-free owners: cache.Apply,
// events.Route, a refetch of the grid the plan names, and a ReadContent for a
// body the cache no longer holds (render.go fetchTileContent).

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/events"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// contentClient is a client as the shim runs one: its cache and its event
// stream.
type contentClient struct {
	t       *testing.T
	ctx     context.Context
	cl      *rpc.Client
	c       *cache.Cache
	events  <-chan *gridwellv1.Event
	landing string
	// changed counts the grid changes the client was told, and told the
	// rows whose bytes it was told moved, by id.
	changed int
	told    map[string]int
}

// newContentClient is a client of a node whose files are root, landed on
// root's grid.
func newContentClient(t *testing.T, root string) *contentClient {
	t.Helper()
	return newContentClientVia(t, root, nil)
}

// newContentClientVia is newContentClient with the node reaching the files
// through via, nil for directly.
func newContentClientVia(t *testing.T, root string, via func(namespace.Namespace) namespace.Namespace) *contentClient {
	t.Helper()
	n := newFilesNodeVia(t, via, root)
	return &contentClient{t: t, ctx: n.ctx, cl: n.cl, c: cache.New(), events: n.subscribe(), landing: n.roots[0]}
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
func TestFilesOpenFileShowsItsNewBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, root)
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("notes.md"))
	if got := k.body(id); string(got) != "# before\n" {
		t.Fatalf("open body %q", got)
	}
	if err := k.cl.SetView(k.ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{k.landing}}); err != nil {
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

// A text tile's preview in a shown grid draws its new bytes once they change on
// disk: the preview is drawn from the same cached body (render.go tileBody).
func TestFilesTextPreviewShowsItsNewBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "todo.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, root)
	k.fetchGrid(k.landing)
	if err := k.cl.SetView(k.ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{k.landing}}); err != nil {
		t.Fatal(err)
	}
	id := rpc.ContentID(k.tile("todo.txt"))
	if got := k.body(id); string(got) != "one\n" {
		t.Fatalf("preview body %q", got)
	}
	k.settle()
	k.changed = 0

	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return bytes.Equal(k.body(id), []byte("one\ntwo\n")) }) {
		t.Fatalf("the preview still draws %q five seconds after its bytes changed on disk (grid changes told: %d)", k.body(id), k.changed)
	}
}

// An image's preview in a shown grid is keyed by the file's mtime, so an
// edited image is asked for its new picture.
func TestFilesImagePreviewShowsItsNewPicture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pic.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nA"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, root)
	k.fetchGrid(k.landing)
	if err := k.cl.SetView(k.ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{k.landing}}); err != nil {
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
		t.Fatalf("the image's preview key is still %d five seconds after it changed on disk", before)
	}
}
