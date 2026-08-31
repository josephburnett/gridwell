package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// The gitlab todos plugin through the WHOLE shipped stack — fake
// GitLab → the spawned gridwell-plugin-gitlab binary → adapter → server →
// ReadContent — pinning the three promises the design makes at the seam where
// they are kept: the plugin's hints become the first arrangement and the
// user's moves win from then on; a todo that leaves GitLab flips to done
// without moving or changing identity; and a plugin RESTART (empty
// memory) does not lose the tile — the node remembers it.
//
// The plugin is a subprocess, which is the only door it has: it lives in
// another repository. So nothing here is injected. The clock is not either:
// a short refresh: in the config is what makes the second walk happen, and
// weeks derive from each todo's created_at, never from now.

// todoTileW mirrors the plugin's hinted todo width — two cells, so the label
// reads. It is the plugin's own arrangement fact, read back off the wire
// here rather than shared: the two repositories share the contract, not a
// package.
const todoTileW = 2

// gitlabTodo is one merge-request todo as GitLab serves it, from Ada.
func gitlabTodo(id int64, created string) gitlabfake.Todo {
	var t gitlabfake.Todo
	t.ID, t.State, t.CreatedAt = id, "pending", created
	t.TargetType, t.Body, t.ActionName = "MergeRequest", "please **review**", "review_requested"
	t.Target.IID, t.Target.Title = id, "change "+strings.Repeat("x", int(id))
	t.Author.Name = "Ada"
	t.TargetURL = "https://gitlab.example/g/p/-/merge_requests/" + strconv.FormatInt(id, 10)
	return t
}

// gitlabStackAt spawns the plugin over an EXISTING memory DB path (a restart
// reuses it) and returns the adapter client plus a closer that stops both.
func gitlabStackAt(t *testing.T, memPath string, cfg map[string]string) (namespace.Namespace, pluginv1.PluginClient, func()) {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	cp, kill := plugintest.SpawnCloser(t, "gitlab", cfg)
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	return client, cp, func() { kill(); _ = memStore.Close() }
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
	done := gitlabTodo(3, "2026-08-19T10:00:00Z")
	done.State = "done"
	gl := gitlabfake.New(t,
		gitlabTodo(1, "2026-08-18T10:00:00Z"),
		gitlabTodo(2, "2026-08-25T10:00:00Z"),
		done,
	)
	memPath := filepath.Join(t.TempDir(), "mem.db")
	// A refresh window of one nanosecond: every read walks GitLab again, so a
	// todo that leaves shows up on the very next read instead of after the
	// default window. Anything longer is a race against the test's own speed.
	cfg := gl.Config(t, map[string]string{"refresh": "1ns"})
	client, _, closeStack := gitlabStackAt(t, memPath, cfg)

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
	// A plugin that holds its own content declares no host_content, so its
	// grids render as owned content — the gitlab face is the well, exactly
	// as it was when the client had no declaration for the kind "gitlab".
	if len(root.Tiles) != 2 || root.Grid.HostContent || root.Grid.Glyph != "" {
		t.Fatalf("root = %+v %v", root.Grid, root.Tiles)
	}
	week := tileByLabelPrefix(root.Tiles, "2026-08-17")
	// The epoch is the month of 2026-08-24, so August is row 0 and the week
	// of Monday the 17th is that month's third Monday: column 2.
	if week == nil || week.Kind != "well" || week.ChildGridId == "" || week.X != 2 || week.Y != 0 {
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
	if one.ServesPage || one.Kind != "text" || one.X != 1*todoTileW || one.Y != 0 || one.W != todoTileW || three.X != 2*todoTileW {
		t.Errorf("hints not honored: one=%+v three=%+v", one, three)
	}

	// The tile's content is markdown with the target link — what the
	// rendered face shows, and what a click opens as an ephemeral visit.
	if body := readContent(t, client, one.Id); !strings.Contains(body, "> please **review**") || !strings.Contains(body, "[Open !1 in GitLab](") {
		t.Fatalf("content = %q", body)
	}

	// The user moves todo 1. That is the durable touch: the entry earns a row
	// and is named by it from here on.
	movedOne, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: one.Id, X: 5, Y: 5, W: 3, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	one = movedOne.GetTile()

	// Todo 1 leaves GitLab entirely (target deleted): the next walk finds it
	// in neither state, so it reads as done — same id, where the user left it.
	gl.Set(gitlabTodo(2, "2026-08-25T10:00:00Z"), done)
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
	client2, _, closeStack2 := gitlabStackAt(t, memPath, cfg)
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
