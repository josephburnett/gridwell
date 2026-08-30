package pluginhost_test

// A plugin's Search is only worth anything if the node can reach it and
// turn its answers into PLACES — tiles with the ids the memory DB
// minted, on a path of well tiles. This crosses the whole seam: fake
// GitLab → gitlab plugin → adapter → server → rpc client.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

const gitlabUUID = "gluuidx"

type fakeGitLab struct{ pending []todos.Todo }

func (f *fakeGitLab) Page(_ context.Context, state string, page int) ([]todos.Todo, bool, error) {
	if page > 1 || state == todos.StateDone {
		return nil, false, nil
	}
	return f.pending, false, nil
}

func gitlabTodo(id int64, created, title, body string) todos.Todo {
	var t todos.Todo
	t.ID, t.State = id, todos.StatePending
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.TargetType, t.Target.IID, t.Target.Title, t.Body = "MergeRequest", id, title, body
	t.TargetURL = "https://gitlab.example/g/p/-/merge_requests/1"
	return t
}

func gitlabNode(t *testing.T, gl *fakeGitLab) *rpc.Client {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, cpCloser, err := compose.PluginInProcess(gitlabplugin.New(gl, gitlabplugin.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	reg := plugin.NewRegistry()
	reg.Register(gitlabUUID, "gitlab", client, nil)
	srv, err := server.New(reg, server.Config{Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

// Search results are the SAME tiles GetGrid mints — id, grid, placement
// — on a path of the well tiles that lead there, so a hit is a place
// the client can go. Before the adapter forwarded Search, the server's
// fan-out skipped the plugin (Unimplemented) and the query answered
// nothing at all.
func TestSearchThroughTheAdapterAnswersMintedPlaces(t *testing.T) {
	gl := &fakeGitLab{pending: []todos.Todo{
		gitlabTodo(1, "2026-08-18T10:00:00Z", "fix the widget", "please review the widget"),
		gitlabTodo(2, "2026-08-25T10:00:00Z", "add a gadget", "no rush"),
	}}
	cl := gitlabNode(t, gl)
	ctx := context.Background()

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := cl.GetGrid(ctx, pl.Plugins[0].RootGridID)
	if err != nil {
		t.Fatal(err)
	}
	var weekWell *rpc.Tile
	for i := range root.Tiles {
		if strings.HasPrefix(root.Tiles[i].AltText, "2026-08-17") {
			weekWell = &root.Tiles[i]
		}
	}
	if weekWell == nil {
		t.Fatalf("no week well in the root: %v", root.Tiles)
	}

	// Search BEFORE the week was ever listed: the hit is minted through
	// the same synthesis a GetGrid would run.
	hits, err := cl.Search(ctx, "widget", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want the one widget todo", hits)
	}
	hit := hits[0]
	if len(hit.Path) != 1 || hit.Path[0].ID != weekWell.ID {
		t.Errorf("path = %v, want the week well %s", hit.Path, weekWell.ID)
	}
	if hit.Tile.GridID != weekWell.ChildGridID || !strings.Contains(hit.Tile.AltText, "!1") {
		t.Errorf("hit tile = %+v, want !1 in the week grid %s", hit.Tile, weekWell.ChildGridID)
	}
	if !strings.HasPrefix(hit.Tile.ID, gitlabUUID+"/") {
		t.Errorf("hit id %q is not qualified into the plugin's namespace", hit.Tile.ID)
	}

	week, err := cl.GetGrid(ctx, weekWell.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	var minted *rpc.Tile
	for i := range week.Tiles {
		if week.Tiles[i].ID == hit.Tile.ID {
			minted = &week.Tiles[i]
		}
	}
	if minted == nil {
		t.Fatalf("the hit's id %s is not among the week's minted tiles %v", hit.Tile.ID, week.Tiles)
	}
	if minted.X != hit.Tile.X || minted.Y != hit.Tile.Y || minted.W != hit.Tile.W {
		t.Errorf("hit placement %+v differs from the grid's %+v", hit.Tile, minted)
	}

	// Scoped to the plugin, the same answer; an id: locate is the one
	// selector the adapter cannot resolve yet and says so.
	scoped, err := cl.Search(ctx, "widget", hit.Tile.ID, 10)
	if err != nil || len(scoped) != 1 || scoped[0].Tile.ID != hit.Tile.ID {
		t.Errorf("scoped search = %v, %v", scoped, err)
	}
	if _, err := cl.Search(ctx, "id:"+hit.Tile.ID, hit.Tile.ID, 1); err == nil {
		t.Error("id: locate through the adapter must refuse, not answer an empty or wrong place")
	}
}
