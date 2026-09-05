package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
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
	client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
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
	three := tileByLabelPrefix(wk.Tiles, "✅ Ada: !3 ")
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
	flipped := tileByLabelPrefix(wk.Tiles, "✅ Ada: !1 ")
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
	kept := tileByLabelPrefix(wk.Tiles, "✅ Ada: !1 ")
	if len(wk.Tiles) != 2 || kept == nil || kept.Id != one.Id || kept.X != 5 || kept.Y != 5 {
		t.Fatalf("restart lost the todo: %v", wk.Tiles)
	}
	if body := readContent(t, client2, one.Id); !strings.Contains(body, "todo:1") {
		t.Errorf("gone content = %q", body)
	}
	// And a live todo still reads after the restart.
	three = tileByLabelPrefix(wk.Tiles, "✅ Ada: !3 ")
	if body := readContent(t, client2, three.Id); !strings.Contains(body, "[Open !3 in GitLab](") {
		t.Errorf("live content after restart = %q", body)
	}
}

// The trash gesture on a todo, through the WHOLE stack the user's hand
// crosses: the wire client → the router → the adapter → the spawned
// gridwell-plugin-gitlab binary → fake GitLab, with a home namespace beside
// the plugin so a link can be dragged from another grid.
//
// Delete here means "mark the todo done at GitLab" — the tile stays and
// changes state — so the node must keep the row it minted. The row is where
// two durable facts live: the placement the user chose, and the identity every
// stored reference names (MintRef canonicalizes a link's target to a row id).
// Retiring it on the plugin's word alone snapped the tile back to its calendar
// hint under a fresh id and killed every link to it. Nothing but a delete-
// then-look test on a plugin with keep semantics can see that, which is why
// this journey moves the tile, links to it, and then trashes it.
func TestTrashingATodoKeepsItsRowItsPlacementAndItsLinks(t *testing.T) {
	gl := gitlabfake.New(t, gitlabTodo(1, "2026-08-18T10:00:00Z"))
	// A one-nanosecond refresh window: every read walks GitLab again, so the
	// done state lands on the very next listing.
	cfg := gl.Config(t, map[string]string{"refresh": "1ns"})
	client, _, _ := gitlabStackAt(t, filepath.Join(t.TempDir(), "mem.db"), cfg)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, homeRoot := registerPrimaryLocaldb(t, reg, st)
	reg.Register("ug1", "gitlab", client, nil)
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var todosRoot string
	for _, p := range pl.Plugins {
		if p.UUID == "ug1" {
			todosRoot = p.RootGridID
		}
	}
	if todosRoot == "" {
		t.Fatalf("no gitlab plugin in the handshake: %+v", pl.Plugins)
	}
	root, err := cl.GetGrid(ctx, todosRoot)
	if err != nil {
		t.Fatal(err)
	}
	var week rpc.Tile
	for _, tl := range root.Tiles {
		if strings.HasPrefix(tl.AltText, "2026-08-17") {
			week = tl
		}
	}
	if week.ChildGridID == "" {
		t.Fatalf("no week well: %+v", root.Tiles)
	}
	wk, err := cl.GetGrid(ctx, week.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wk.Tiles) != 1 {
		t.Fatalf("week grid = %+v, want the one todo", wk.Tiles)
	}
	todo := wk.Tiles[0]

	// The user moves the todo somewhere of their own, and drags a link to it
	// onto the home grid — a weekly plan naming this todo.
	moved, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: todo.ID, X: 5, Y: 5, W: 3, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	todo = *moved
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: homeRoot, X: 1, Y: 1, W: 1, H: 1,
		Kind: rpc.KindText, LinkTargetID: todo.ID, Label: todo.AltText,
	})
	if err != nil {
		t.Fatalf("link to a todo: %v", err)
	}
	// A reference at rest names a row, so the link's target is the id the
	// delete is about to decide the fate of.
	if link.LinkTargetID != todo.ID {
		t.Fatalf("link target = %q, want the moved todo %q", link.LinkTargetID, todo.ID)
	}

	// The trash gesture. GitLab accepts the mark-as-done; the todo stays.
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: todo.ID}); err != nil {
		t.Fatalf("trash a todo: %v", err)
	}

	after, err := cl.GetGrid(ctx, week.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tiles) != 1 {
		t.Fatalf("week grid after the trash = %+v, want the todo still there, done", after.Tiles)
	}
	kept := after.Tiles[0]
	if kept.ID != todo.ID {
		t.Errorf("the todo came back under a fresh id %q, want %q: every stored reference to it is now dead", kept.ID, todo.ID)
	}
	if kept.X != 5 || kept.Y != 5 || kept.W != 3 || kept.H != 2 {
		t.Errorf("the todo snapped back to its hint: %+v, want the 5,5 3x2 the user left", kept)
	}
	// The gesture's whole visible effect: the same tile, repainted done.
	if !strings.HasPrefix(kept.AltText, "✅ ") {
		t.Errorf("label after the trash = %q, want the done mark", kept.AltText)
	}

	// And the link still names something that reads.
	if _, err := cl.GetTile(ctx, link.LinkTargetID); err != nil {
		t.Fatalf("the link went dead: GetTile %s: %v", link.LinkTargetID, err)
	}
	body, _, _, err := cl.ReadContent(ctx, link.LinkTargetID)
	if err != nil {
		t.Fatalf("content through the link target: %v", err)
	}
	if !strings.Contains(string(body), "[Open !1 in GitLab](") {
		t.Errorf("content through the link = %q", body)
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
