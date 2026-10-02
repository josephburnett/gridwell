package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// A plugin's hints shift between listings: the gitlab plugin hints a todo by
// its index among its day's todos, so a todo created earlier that day pushes
// every later one down a row. The node keeps the rows the user touched where
// they were, and the newcomer's hint, which now names a touched row's cell,
// must stack below instead of landing on it. Only the seam sees this: the plugin's
// hints are each correct, and the store's rows are each where they were left.
func TestShiftedHintsNeverLandOnAStoredRow(t *testing.T) {
	gl := gitlabfake.New(t,
		gitlabTodo(1, "2026-08-17T10:00:00Z"),
		gitlabTodo(2, "2026-08-17T12:00:00Z"),
	)
	cfg := gl.Config(t, map[string]string{"full_refresh": "1ns"})
	client, _, closeStack := gitlabStackAt(t, filepath.Join(t.TempDir(), "mem.db"), cfg)
	t.Cleanup(closeStack)
	reg := plugin.NewRegistry()
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
	root, err := cl.GetGrid(ctx, todosRoot)
	if err != nil {
		t.Fatal(err)
	}
	week := tileByLabelPrefix(root.Tiles, "2026-08-17")
	if week == nil {
		t.Fatalf("no week well: %+v", root.Tiles)
	}
	wk, err := cl.GetGrid(ctx, week.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	// The user touches both Monday todos where they stand, which mints their
	// rows at the hinted cells: (0,0) and (0,1).
	kept := map[string]*gridwellv1.Tile{}
	for _, prefix := range []string{"Ada: !1 ", "Ada: !2 "} {
		tl := tileByLabelPrefix(wk.Tiles, prefix)
		if tl == nil {
			t.Fatalf("no %q in %+v", prefix, wk.Tiles)
		}
		if kept[prefix], err = cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: tl.Id, X: tl.X, Y: tl.Y, W: tl.W, H: tl.H}); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := kept["Ada: !1 "], kept["Ada: !2 "]; a.X != 0 || a.Y != 0 || b.X != 0 || b.Y != 1 {
		t.Fatalf("first listing = %+v, %+v, want Monday rows 0 and 1", a, b)
	}

	// A todo created earlier on Monday: the plugin now hints it at (0,0) and
	// shifts the touched ones to rows 1 and 2.
	gl.Set(gitlabTodo(3, "2026-08-17T08:00:00Z"), gitlabTodo(1, "2026-08-17T10:00:00Z"), gitlabTodo(2, "2026-08-17T12:00:00Z"))
	var newcomer *gridwellv1.Tile
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if wk, err = cl.GetGrid(ctx, week.ChildGridId); err != nil {
			t.Fatal(err)
		}
		if newcomer = tileByLabelPrefix(wk.Tiles, "Ada: !3 "); newcomer != nil || time.Now().After(deadline) {
			break
		}
	}
	if newcomer == nil {
		t.Fatalf("the new todo never listed: %+v", wk.Tiles)
	}
	if newcomer.X != 0 || newcomer.Y != 2 {
		t.Fatalf("new Monday todo at (%d,%d), want (0,2): Monday's column, below the touched ones", newcomer.X, newcomer.Y)
	}
	owner := map[[2]int64]string{}
	for _, tl := range wk.Tiles {
		for dx := range max(tl.W, 1) {
			for dy := range max(tl.H, 1) {
				c := [2]int64{tl.X + dx, tl.Y + dy}
				if o, ok := owner[c]; ok {
					t.Fatalf("%q and %q share cell %v: %+v", o, tl.AltText, c, wk.Tiles)
				}
				owner[c] = tl.AltText
			}
		}
	}
	for prefix, was := range kept {
		now := tileByLabelPrefix(wk.Tiles, prefix)
		if now == nil || now.Id != was.Id || now.X != was.X || now.Y != was.Y || now.W != was.W {
			t.Fatalf("%s moved or changed identity: was %+v, now %+v", strings.TrimSpace(prefix), was, now)
		}
	}
}
