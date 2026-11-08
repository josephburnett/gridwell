package server_test

// The node's files as the client meets them: files.Source registered beside
// home under the files' owner id, over temp roots, every call through the
// real web API.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// filesNode is a node with a home and the files over roots.
type filesNode struct {
	t       *testing.T
	ctx     context.Context
	cl      *rpc.Client
	raw     gridwellv1connect.GridwellClient
	srv     *server.Server
	url     string
	home    string // home's root grid, qualified
	roots   []string
	trashed []string
}

// newFilesNode serves a node whose files are roots, each a directory, with
// their deletes recorded instead of moved to the OS trash.
func newFilesNode(t *testing.T, roots ...string) *filesNode {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := &filesNode{t: t}
	var rs []files.Root
	for _, r := range roots {
		rs = append(rs, files.Root{Path: r})
		n.roots = append(n.roots, rpc.QualifyID(localNodeID, rpc.KeyTileID(r)))
	}
	src, err := files.New(st.Namespace("~"), rs, func(p string) error {
		n.trashed = append(n.trashed, p)
		return os.RemoveAll(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.SetLabel(localNodeID, "home")
	reg.Register(rpc.FilesOwner(localNodeID), "files", src, src.Close)
	reg.SetLabel(rpc.FilesOwner(localNodeID), "files")
	t.Cleanup(reg.Close)
	n.srv = servertest.New(t, reg, server.Config{ID: localNodeID})
	hs := servertest.Serve(t, n.srv)
	n.url = hs.URL
	n.cl = rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	n.raw = gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	n.ctx = ctx
	lp, err := n.cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n.home = rpc.HomeGrid(lp)
	return n
}

// grid reads id, fatal on a refusal.
func (n *filesNode) grid(id string) *gridwellv1.GetGridResponse {
	n.t.Helper()
	g, err := n.cl.GetGrid(n.ctx, id)
	if err != nil {
		n.t.Fatalf("GetGrid(%s): %v", id, err)
	}
	return g
}

// fileID is the qualified id of a file or directory on the test node.
func fileID(path string) string { return rpc.QualifyID(localNodeID, rpc.KeyTileID(path)) }

// subscribe opens the event stream and answers the channel it fills.
func (n *filesNode) subscribe() <-chan *gridwellv1.Event {
	n.t.Helper()
	es, err := n.cl.Subscribe(n.ctx)
	if err != nil {
		n.t.Fatal(err)
	}
	out := make(chan *gridwellv1.Event, 256)
	go func() {
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			out <- ev
		}
	}()
	return out
}

// await reads events until match holds, fatal at the deadline.
func await(t *testing.T, events <-chan *gridwellv1.Event, what string, match func(*gridwellv1.Event) bool) *gridwellv1.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s within 10s", what)
			return nil
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The roster carries one files row between home and the connections, keyed
// by the files' owner id, with one folder grid per root, and a root's grid
// lists its files under the node's own ids.
func TestFilesRosterRowAndRootGrid(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(a, "notes.md"), "# hi\n")
	n := newFilesNode(t, a, b)
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.Plugins) < 2 || lp.Plugins[0].Uuid != localNodeID || lp.Plugins[1].Uuid != rpc.FilesOwner(localNodeID) {
		t.Fatalf("roster = %v, want home then the files", lp.Plugins)
	}
	row := lp.Plugins[1]
	if row.Label != "files" || row.Glyph != rpc.GlyphFolder || row.RootGridId != "" || len(row.PluginGrids) != 2 {
		t.Fatalf("files row = %v", row)
	}
	for i, g := range row.PluginGrids {
		if g.GridId != n.roots[i] || g.Glyph != rpc.GlyphFolder || g.Label != filepath.Base([]string{a, b}[i]) {
			t.Errorf("root %d = %v, want grid %s", i, g, n.roots[i])
		}
	}
	got := n.grid(n.roots[0])
	if got.Grid.Id != n.roots[0] || got.Grid.Glyph != rpc.GlyphFolder || got.Grid.GetAcceptsTiles() {
		t.Errorf("root grid = %v", got.Grid)
	}
	if len(got.Grid.PluginGrids) != 2 || got.Grid.PluginGrids[0].GridId != n.roots[0] {
		t.Errorf("root grid's declared grids = %v, want the files' roots", got.Grid.PluginGrids)
	}
	if len(got.Tiles) != 1 || got.Tiles[0].Id != fileID(filepath.Join(a, "notes.md")) || got.Tiles[0].GridId != n.roots[0] {
		t.Fatalf("root tiles = %v", got.Tiles)
	}
	if !rpc.IsFilesID(got.Tiles[0].Id) || rpc.OwnerNamespaceOf(got.Tiles[0].Id, localNodeID) != rpc.FilesOwner(localNodeID) {
		t.Errorf("a file's id %q is not the files'", got.Tiles[0].Id)
	}
}

// A root that will not read is dark, told under the files' owner id on every
// open stream, and its grid still answers.
func TestFilesDarkRootHealthIsKeyedByTheFilesOwner(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "missing")
	n := newFilesNode(t, gone)
	events := n.subscribe()
	if _, err := n.cl.GetGrid(n.ctx, n.roots[0]); err != nil {
		t.Fatalf("a dark root's grid must answer: %v", err)
	}
	ev := await(t, events, "dark health", func(ev *gridwellv1.Event) bool {
		h := ev.GetPluginHealth()
		return h != nil && !h.Healthy
	})
	if h := ev.GetPluginHealth(); h.PluginUuid != rpc.FilesOwner(localNodeID) || h.Detail == "" {
		t.Fatalf("health = %v, want dark keyed %s with the root named", h, rpc.FilesOwner(localNodeID))
	}
}

// Find fans out to the files too: an id: locate of a file answers its row.
func TestFilesFindLocatesAFile(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "x")
	n := newFilesNode(t, root)
	n.grid(n.roots[0])
	resp, err := n.raw.Find(n.ctx, connect.NewRequest(&gridwellv1.FindRequest{Query: "id:" + fileID(f)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Results) != 1 || resp.Msg.Results[0].Tile.GetId() != fileID(f) {
		t.Fatalf("find = %v, want %s", resp.Msg.Results, fileID(f))
	}
	text, err := n.raw.Find(n.ctx, connect.NewRequest(&gridwellv1.FindRequest{Query: "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range text.Msg.Results {
		if rpc.IsFilesID(r.Tile.GetId()) {
			t.Errorf("a text query answered a file %s; the files answer it empty", r.Tile.GetId())
		}
	}
}
