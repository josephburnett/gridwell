package server

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// The gitlab todos plugin through the whole shipped stack — fake GitLab, the
// spawned binary, the adapter, the server, ReadContent — pinning three
// promises: the plugin's hints become the first arrangement and the user's
// moves win after that; a todo that leaves GitLab flips to done without moving
// or changing identity; and a plugin restart does not lose the tile.
//
// Nothing here is injected. The plugin lives in another repository, so the
// subprocess is its only door, and the clock is real: a short `refresh:` makes
// the second walk happen, and weeks derive from each todo's created_at.

// todoTileW mirrors the plugin's hinted todo width — two cells, so the label
// reads — and doneMark its one status, the done mark. It is the plugin's own arrangement fact, read back off the wire
// here rather than shared: the two repositories share the contract, not a
// package.
const (
	todoTileW = 2
	doneMark  = "✅"
)

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
	// A full-walk window of one nanosecond: every read starts a full walk, so
	// a todo that leaves shows up within a read or two instead of after the
	// default window. Anything longer is a race against the test's own speed.
	cfg := gl.Config(t, map[string]string{"full_refresh": "1ns"})
	client, _, closeStack := gitlabStackAt(t, memPath, cfg)

	reg := plugin.NewRegistry()
	reg.Register("ug1", "gitlab", client, nil)
	srv := mustNew(t, reg, Config{Password: "pw"})
	webDoorTest(t, srv.WebHandler())
	ctx := context.Background()

	// Root: one well per week, hinted into the epoch-anchored column.
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
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

	// Descent: the week's todos, calendar-hinted: a column per day, a row
	// per local creation hour. Each is named by its title alone; only the
	// done one carries a status.
	wk, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	one := tileByLabelPrefix(wk.Tiles, "Ada: !1 ")
	three := tileByLabelPrefix(wk.Tiles, "Ada: !3 ")
	if len(wk.Tiles) != 2 || one == nil || three == nil {
		t.Fatalf("week grid = %v", wk.Tiles)
	}
	if one.StatusDetail != "" || three.StatusDetail != doneMark {
		t.Errorf("status: open %q, done %q", one.StatusDetail, three.StatusDetail)
	}
	if one.TextPresentation != "both" || three.TextPresentation != "both" {
		t.Errorf("text_presentation: %q %q, want both", one.TextPresentation, three.TextPresentation)
	}
	hour := int64(time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC).Local().Hour())
	if one.ServesPage || one.Kind != "text" || one.Y != hour || three.Y != hour || one.W != todoTileW || three.X-one.X != todoTileW {
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

	// Todo 1 leaves GitLab entirely (target deleted): a walk finds it in
	// neither state, so it reads as done — same id, where the user left it. A
	// read answers the last walk that landed and starts the next, so the flip
	// shows on a later read, not necessarily the next one.
	gl.Set(gitlabTodo(2, "2026-08-25T10:00:00Z"), done)
	var flipped *gridwellv1.Tile
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		wk, err = client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
		if err != nil {
			t.Fatal(err)
		}
		if flipped = tileByLabelPrefix(wk.Tiles, "Ada: !1 "); flipped.GetStatusDetail() == doneMark || time.Now().After(deadline) {
			break
		}
	}
	if len(wk.Tiles) != 2 || flipped.GetStatusDetail() != doneMark || flipped.Id != one.Id || flipped.X != 5 || flipped.Y != 5 || flipped.W != 3 {
		t.Fatalf("after deletion in GitLab: %v", wk.Tiles)
	}

	// Plugin restart: a fresh process has never seen todo 1, and GitLab does
	// not list it. The node remembers — same tile, same id, same placement,
	// label — and its content says it is not in memory.
	closeStack()
	client2, _, closeStack2 := gitlabStackAt(t, memPath, cfg)
	t.Cleanup(closeStack2)
	reg2 := plugin.NewRegistry()
	reg2.Register("ug1", "gitlab", client2, nil)
	wk, err = client2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	kept := tileByLabelPrefix(wk.Tiles, "Ada: !1 ")
	if len(wk.Tiles) != 2 || kept == nil || kept.Id != one.Id || kept.X != 5 || kept.Y != 5 {
		t.Fatalf("restart lost the todo: %v", wk.Tiles)
	}
	if body := readContent(t, client2, one.Id); !strings.Contains(body, "todo:1") {
		t.Errorf("gone content = %q", body)
	}
	// And a live todo still reads after the restart.
	three = tileByLabelPrefix(wk.Tiles, "Ada: !3 ")
	if body := readContent(t, client2, three.Id); !strings.Contains(body, "[Open !3 in GitLab](") {
		t.Errorf("live content after restart = %q", body)
	}
}

// The trash gesture on a todo, through the whole stack the user's hand crosses,
// with a home namespace beside the plugin so a link can be dragged in from
// another grid.
//
// Delete here means "mark the todo done at GitLab": the tile stays and changes
// state, so the node must keep the row it minted, which is where the placement
// the user chose and the identity every stored reference names both live.
// Retiring it on the plugin's word would snap the tile back under a fresh id
// and kill every link to it, which is why this journey moves the tile, links
// to it, and then trashes it.
func TestTrashingATodoKeepsItsRowItsPlacementAndItsLinks(t *testing.T) {
	gl := gitlabfake.New(t, gitlabTodo(1, "2026-08-18T10:00:00Z"))
	// A one-nanosecond full-walk window: every read starts a full walk, so no
	// listing here waits out the default window. The done state itself lands
	// when GitLab accepts the write.
	cfg := gl.Config(t, map[string]string{"full_refresh": "1ns"})
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
		if p.Uuid == "ug1" {
			todosRoot = plugintest.LandingOf(t, p)
		}
	}
	if todosRoot == "" {
		t.Fatalf("no gitlab plugin in the handshake: %+v", pl.Plugins)
	}
	root, err := cl.GetGrid(ctx, todosRoot)
	if err != nil {
		t.Fatal(err)
	}
	var week *gridwellv1.Tile
	for _, tl := range root.Tiles {
		if strings.HasPrefix(tl.AltText, "2026-08-17") {
			week = tl
		}
	}
	if week.ChildGridId == "" {
		t.Fatalf("no week well: %+v", root.Tiles)
	}
	wk, err := cl.GetGrid(ctx, week.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(wk.Tiles) != 1 {
		t.Fatalf("week grid = %+v, want the one todo", wk.Tiles)
	}
	todo := wk.Tiles[0]

	// The user moves the todo somewhere of their own, and drags a link to it
	// onto the home grid — a weekly plan naming this todo.
	moved, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: todo.Id, X: 5, Y: 5, W: 3, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	todo = moved
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: homeRoot, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, Y: 1, W: 1, H: 1, LinkTargetId: todo.Id, AltText: todo.AltText}})
	if err != nil {
		t.Fatalf("link to a todo: %v", err)
	}
	// A reference at rest names a row, so the link's target is the id the
	// delete is about to decide the fate of.
	if link.LinkTargetId != todo.Id {
		t.Fatalf("link target = %q, want the moved todo %q", link.LinkTargetId, todo.Id)
	}

	// The trash gesture. GitLab accepts the mark-as-done; the todo stays.
	if err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: todo.Id}); err != nil {
		t.Fatalf("trash a todo: %v", err)
	}

	after, err := cl.GetGrid(ctx, week.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tiles) != 1 {
		t.Fatalf("week grid after the trash = %+v, want the todo still there, done", after.Tiles)
	}
	kept := after.Tiles[0]
	if kept.Id != todo.Id {
		t.Errorf("the todo came back under a fresh id %q, want %q: every stored reference to it is now dead", kept.Id, todo.Id)
	}
	if kept.X != 5 || kept.Y != 5 || kept.W != 3 || kept.H != 2 {
		t.Errorf("the todo snapped back to its hint: %+v, want the 5,5 3x2 the user left", kept)
	}
	// The gesture's whole visible effect: the same tile, the same name, now
	// carrying the done status.
	if kept.StatusDetail != doneMark || kept.AltText != todo.AltText {
		t.Errorf("after the trash: label %q status %q, want %q with the done mark", kept.AltText, kept.StatusDetail, todo.AltText)
	}

	// And the link still names something that reads.
	if _, err := cl.GetTile(ctx, link.LinkTargetId); err != nil {
		t.Fatalf("the link went dead: GetTile %s: %v", link.LinkTargetId, err)
	}
	body, _, _, err := cl.ReadContent(ctx, link.LinkTargetId)
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
