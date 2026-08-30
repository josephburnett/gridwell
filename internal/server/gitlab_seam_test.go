package server

import (
	"context"
	"github.com/josephburnett/gridwell/internal/namespace"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

// The gitlab todos plugin through the WHOLE shipped stack — fake
// GitLab → plugin → adapter → server → ReadContent — pinning
// the three promises the design makes at the seam where they are kept:
// the plugin's hints become the first arrangement and the user's
// moves win from then on; a todo that leaves GitLab flips to done
// without moving or changing identity; and a plugin RESTART (empty
// memory) does not lose the tile — the node remembers it.

type fakeGitLab struct {
	pending, done []todos.Todo
}

func (f *fakeGitLab) Page(_ context.Context, state string, page int) ([]todos.Todo, bool, error) {
	if page > 1 {
		return nil, false, nil
	}
	if state == todos.StateDone {
		return f.done, false, nil
	}
	return f.pending, false, nil
}

func gitlabTodo(id int64, created string) todos.Todo {
	var t todos.Todo
	t.ID, t.State = id, todos.StatePending
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.TargetType, t.Target.IID, t.Target.Title, t.Body = "MergeRequest", id, "change "+strings.Repeat("x", int(id)), "please **review**"
	t.ActionName, t.Author.Name = "review_requested", "Ada"
	t.TargetURL = "https://gitlab.example/g/p/-/merge_requests/" + strconv.FormatInt(id, 10)
	return t
}

// gitlabStackAt stands the plugin up over an EXISTING memory DB path
// (a restart reuses it) and returns the adapter client plus a closer.
func gitlabStackAt(t *testing.T, memPath string, impl pluginv1.PluginServer) (namespace.Namespace, func()) {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	cp, cpCloser, err := compose.PluginInProcess(impl)
	if err != nil {
		t.Fatal(err)
	}
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	return client, func() { cpCloser(); _ = memStore.Close() }
}

func tileByLabelPrefix(tiles []*gridwellv1.Tile, prefix string) *gridwellv1.Tile {
	for _, tl := range tiles {
		if strings.HasPrefix(tl.AltText, prefix) {
			return tl
		}
	}
	return nil
}

func TestGitLabTodosThroughTheStack(t *testing.T) {
	gl := &fakeGitLab{
		pending: []todos.Todo{gitlabTodo(1, "2026-08-18T10:00:00Z"), gitlabTodo(2, "2026-08-25T10:00:00Z")},
		done:    []todos.Todo{gitlabTodo(3, "2026-08-19T10:00:00Z")},
	}
	gl.done[0].State = todos.StateDone
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	memPath := filepath.Join(t.TempDir(), "mem.db")
	client, closeStack := gitlabStackAt(t, memPath, gitlabplugin.New(gl, gitlabplugin.Options{Now: now}))

	reg := plugin.NewRegistry()
	reg.Register("ug1", "gitlab", client, nil)
	srv := mustNew(t, reg, Config{Password: "pw"})
	hs := httptest.NewServer(srv.WebHandler())
	t.Cleanup(hs.Close)
	ctx := context.Background()

	// Root: one well per week, hinted into the epoch-anchored column.
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Tiles) != 2 || root.Grid.SourceKind != "gitlab" {
		t.Fatalf("root = %+v %v", root.Grid, root.Tiles)
	}
	week := tileByLabelPrefix(root.Tiles, "2026-08-17")
	wantX, wantY := todos.WeekCell(time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if week == nil || week.Kind != "well" || week.ChildGridId == "" || week.X != wantX || week.Y != wantY {
		t.Fatalf("week well = %+v", week)
	}

	// Descent: the week's todos, calendar-hinted (Tue = column 1).
	wk, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	one := tileByLabelPrefix(wk.Tiles, "Ada: !1 ")
	three := tileByLabelPrefix(wk.Tiles, "✓ Ada: !3 ")
	if len(wk.Tiles) != 2 || one == nil || three == nil {
		t.Fatalf("week grid = %v", wk.Tiles)
	}
	if one.ServesPage || one.Kind != "text" || one.X != 1*todos.TodoTileW || one.Y != 0 || one.W != todos.TodoTileW || three.X != 2*todos.TodoTileW {
		t.Errorf("hints not honored: one=%+v three=%+v", one, three)
	}

	// The tile's content is markdown with the target link — what the
	// rendered face shows, and what a click opens as an ephemeral visit.
	if body := readContent(t, client, one.Id); !strings.Contains(body, "> please **review**") || !strings.Contains(body, "[Open !1 in GitLab](") {
		t.Fatalf("content = %q", body)
	}

	// The user moves todo 1.
	if _, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: one.Id, X: 5, Y: 5, W: 3, H: 2}); err != nil {
		t.Fatal(err)
	}

	// Todo 1 leaves GitLab entirely (target deleted): after the refresh
	// window it reads as done — same id, where the user left it.
	gl.pending = gl.pending[1:]
	clock = clock.Add(gitlabplugin.DefaultRefresh + time.Second)
	wk, err = client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	flipped := tileByLabelPrefix(wk.Tiles, "✓ Ada: !1 ")
	if len(wk.Tiles) != 2 || flipped == nil || flipped.Id != one.Id || flipped.X != 5 || flipped.Y != 5 || flipped.W != 3 {
		t.Fatalf("after deletion in GitLab: %v", wk.Tiles)
	}

	// Plugin restart: a fresh process has never seen todo 1, and GitLab does
	// not list it. The node remembers — same tile, same id, same placement,
	// last-seen label — and its content says it is not in memory.
	closeStack()
	client2, closeStack2 := gitlabStackAt(t, memPath, gitlabplugin.New(gl, gitlabplugin.Options{Now: now}))
	t.Cleanup(closeStack2)
	reg2 := plugin.NewRegistry()
	reg2.Register("ug1", "gitlab", client2, nil)
	wk, err = client2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	kept := tileByLabelPrefix(wk.Tiles, "✓ Ada: !1 ")
	if len(wk.Tiles) != 2 || kept == nil || kept.Id != one.Id || kept.X != 5 || kept.Y != 5 {
		t.Fatalf("restart lost the todo: %v", wk.Tiles)
	}
	if body := readContent(t, client2, one.Id); !strings.Contains(body, "todo:1") {
		t.Errorf("gone content = %q", body)
	}
	// And a live todo still reads after the restart.
	three = tileByLabelPrefix(wk.Tiles, "✓ Ada: !3 ")
	if body := readContent(t, client2, three.Id); !strings.Contains(body, "[Open !3 in GitLab](") {
		t.Errorf("live content after restart = %q", body)
	}

}

// readContent drains a tile's ReadContent stream through the adapter client.
func readContent(t *testing.T, client namespace.Namespace, tileID string) string {
	t.Helper()
	var out []byte
	if err := client.ReadContent(context.Background(), &gridwellv1.ReadContentRequest{TileId: tileID},
		func(c *gridwellv1.ContentChunk) error { out = append(out, c.Data...); return nil }); err != nil {
		t.Fatal(err)
	}
	return string(out)
}
