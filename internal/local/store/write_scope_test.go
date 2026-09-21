package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// version_rule_test.go says which writes bump a version. This says which
// columns each of them is allowed to touch at all, in tiles and grids — the
// two tables holding what the user arranged. A capture that clobbers a name, a
// framing that resets a scroll, a placement that moves a neighbour and a clone
// that reaches back into its source are one class: a write into a fact it does
// not own, invisible until the user finds a thing they never touched has
// changed. Blobs are refcounted storage with their own owner
// (refcount_kinds_test.go) and are out of this table.

// bystanders are the rows no case names: another tile of every kind, in the
// same grid and in a nested one. Every case must leave them identical.
func seedBystanders(t *testing.T, s *Store, root string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateText(ctx, root, 20, 0, 2, 2, []byte("# a neighbour")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateURL(ctx, root, 23, 0, 2, 2, "https://neighbour.example"); err != nil {
		t.Fatal(err)
	}
	well, err := s.CreateWell(ctx, root, 26, 0, 2, 2, "next door")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: 9, Cy: 9, Zoom: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, well.ChildGridId, 0, 0, 1, 1, []byte("inside the neighbour")); err != nil {
		t.Fatal(err)
	}
}

// writeScope is what one mutation may change. Columns are named on the row the
// mutation was handed (tile) and on the grid that row sits in (grid);
// updated_at is implicit on both, being "when this row was last written" and
// nothing the user sees. mints allows rows to appear — a clone's copy, a
// delete's trash grid — and the ids they spend.
type writeScope struct {
	tile  []string
	grid  []string
	mints bool
}

// writeScopes is the whole rule, one entry per versionCase. A mutation without
// an entry fails: the write's reach is part of what a write is.
var writeScopes = map[string]writeScope{
	// ── Content: the bytes, and the claim that guards them. ────────────
	"WriteContent/text body": {tile: []string{"version", "blob_id", "alt_text"}, mints: true},
	// A no-op write never mutates, so it may not even stamp updated_at.
	"WriteContent/text body unchanged": {},
	"WriteContent/url address":         {tile: []string{"version", "url_string"}},
	"RenameTile/user rename":           {tile: []string{"version", "alt_text", "alt_user"}},

	// ── Captures: what the server observed, on top of the user's row. ──
	"SetTileAlt/automatic capture": {tile: []string{"alt_text"}},
	// url_string carries two origins in one column: the address the user typed,
	// which WriteContent claims a version for, and the address the live page
	// ended on, which the freeze writes here with no claim and no bump.
	"SetURLState/freeze capture":   {tile: []string{"preview_blob_id", "url_string", "alt_text", "url_history"}, mints: true},
	"SetShellPreview/frozen frame": {tile: []string{"preview_blob_id"}, mints: true},

	// ── Framing: how it looked. ────────────────────────────────────────
	"SetTextView/window and mode":         {tile: []string{"text_x", "text_y", "text_w", "text_h", "text_mode"}},
	"SetContentZoom/content scale":        {tile: []string{"content_zoom"}},
	"SetFrozen/standing freeze":           {tile: []string{"url_frozen"}},
	"SetFraming/doorway viewport":         {tile: []string{"view_cx", "view_cy", "view_zoom"}},
	"SetPaneLayout/workspace arrangement": {tile: []string{"blob_id"}, mints: true},

	// ── Layout: where it sits. ─────────────────────────────────────────
	"PlaceTile/move and resize": {tile: []string{"x", "y", "w", "h"}},
	// The copy is new rows, and the grid it lands in counts one more tile; the
	// source row is neither.
	"CloneTile/source row": {grid: []string{"version"}, mints: true},
	// A delete on an ordinary grid is a move into the trash: the same row, at
	// a new address, under a grid the trash mints on first use.
	"DeleteTile/move to trash": {tile: []string{"grid_id", "x", "y"}, grid: []string{"version"}, mints: true},
}

func TestAWriteTouchesOnlyWhatItOwns(t *testing.T) {
	for _, c := range versionCases {
		scope, ok := writeScopes[c.name]
		if !ok {
			t.Errorf("%s has no entry in writeScopes: a write's reach is part of the rule", c.name)
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			root := rootID(t, s)
			seedBystanders(t, s, root)
			tile := c.subject(t, s, ctx, root)

			before := storetest.DumpOf(t, s.SQL())
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			for _, ch := range storetest.Changed(before, storetest.DumpOf(t, s.SQL())) {
				if why := scope.refuse(ch, tile.Id, tile.GridId); why != "" {
					t.Errorf("%s: %s", ch, why)
				}
			}
		})
	}
	for name := range writeScopes {
		if !slices.ContainsFunc(versionCases, func(c versionCase) bool { return c.name == name }) {
			t.Errorf("writeScopes names %q, which is no longer a mutation", name)
		}
	}
}

// refuse says why one change is out of scope, or "" when it is allowed. Only
// tiles and grids are judged; see the file header.
func (w writeScope) refuse(change, tileID, gridID string) string {
	if strings.HasPrefix(change, "+") || strings.HasPrefix(change, "-") {
		if !strings.HasPrefix(change[1:], "tiles[") && !strings.HasPrefix(change[1:], "grids[") {
			return ""
		}
		if w.mints {
			return ""
		}
		return "this write makes no rows and retires none"
	}
	table, rest, _ := strings.Cut(change, "[")
	id, col, _ := strings.Cut(rest, "].")
	allowed := w.tile
	switch {
	case table == "tiles" && id == tileID:
	case table == "grids" && id == gridID:
		allowed = w.grid
	case table == "tiles" || table == "grids":
		return "a row this write was not handed"
	default:
		return ""
	}
	if col == "updated_at" {
		return ""
	}
	if slices.Contains(allowed, col) {
		return ""
	}
	return "a column this write does not own"
}

// A grid's own version is not a tile's: it counts the arrangement of the grid,
// so a framing left on a root must not move it — a root framing is the same
// no-claim, no-bump write a doorway's is, on the row a root keeps it in.
func TestRootFramingLeavesTheGridRowOtherwiseAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	seedBystanders(t, s, root)

	before := storetest.DumpOf(t, s.SQL())
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: root, Cx: -3.5, Cy: 8.25, Zoom: 0.4,
	}); err != nil {
		t.Fatalf("SetFraming: %v", err)
	}
	scope := writeScope{grid: []string{"root_cx", "root_cy", "root_zoom"}}
	for _, ch := range storetest.Changed(before, storetest.DumpOf(t, s.SQL())) {
		if why := scope.refuse(ch, "", root); why != "" {
			t.Errorf("%s: %s", ch, why)
		}
	}

	f, ok, err := s.RootFraming(ctx)
	if err != nil || !ok {
		t.Fatalf("root framing: %v ok=%v", err, ok)
	}
	if !f.SameAs(rpc.Framing{Cx: -3.5, Cy: 8.25, Zoom: 0.4}) {
		t.Errorf("root framing round-trip: %+v", f)
	}
}
