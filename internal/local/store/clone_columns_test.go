package store

import (
	"context"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// TestClonePreservesAllContentColumns — clone is an EAGER, COMPLETE copy
// (CLAUDE.md identity semantics): everything the user set on the source must
// be on the copy. Regression: insertTileCopy's hand-listed INSERT omitted
// content_zoom, url_history, and alt_user, so a clone silently lost its
// content zoom, its navigation back-stack, and — worst — the "name is
// user-owned" latch, letting the next automatic title capture clobber a name
// the user chose (the exact issue-#61 class, reintroduced on the clone path).
func TestClonePreservesAllContentColumns(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A freeze that stored a navigation back-stack (issue #113)...
	history := `[{"url":"https://example.com"},{"url":"https://example.com/page"}]`
	frozen, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: tile.Version,
		URL: "https://example.com/page", History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ...a content zoom (issue #82; framing, no version bump)...
	if _, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
		TileID: tile.ID, Version: frozen.Version, ContentZoom: 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	// ...and a USER rename (issue #61; latches alt_user).
	tileIDInt, _ := parseID(tile.ID)
	if err := s.SetTileAlt(ctx, tileIDInt, "my page", true); err != nil {
		t.Fatal(err)
	}

	src, err := s.GetTile(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: tile.ID, Version: src.Version,
		DestGridID: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	if clone.ContentZoom != 1.5 {
		t.Errorf("clone content_zoom = %v, want 1.5 (the source's zoom)", clone.ContentZoom)
	}
	if clone.URLHistory != history {
		t.Errorf("clone url_history = %q, want the source's back-stack", clone.URLHistory)
	}
	if clone.AltText != "my page" {
		t.Errorf("clone alt_text = %q, want %q", clone.AltText, "my page")
	}

	// The behavioral half of alt_user: an automatic (non-user) title capture
	// on the CLONE must defer to the copied user-owned name, exactly as it
	// would on the source.
	cloneIDInt, _ := parseID(clone.ID)
	if err := s.SetTileAlt(ctx, cloneIDInt, "Captured Page Title", false); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetTile(ctx, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AltText != "my page" {
		t.Errorf("automatic capture clobbered the clone's user-owned name: alt = %q (alt_user latch was not copied)", after.AltText)
	}
}

// TestTileCopyColumnsAreTotal is the drift lint for the clone INSERT: every
// column of the tiles table must be either copied by insertTileCopy
// (tileCopyColumns) or on the explicit not-copied list below. Before this
// lint, adding a column to the schema and forgetting the clone path compiled
// clean and silently produced incomplete copies — three columns had already
// slipped through. Now the omission is a named failure.
func TestTileCopyColumnsAreTotal(t *testing.T) {
	notCopied := map[string]string{
		"id":         "the copy's identity — freshly assigned, never reused",
		"ns":         "a clone is home's (ns ''): externals' rows are never cloned, they are re-listed",
		"key":        "an external's key — '' on every home row",
		"tombstoned": "an external's retirement — 0 on every home row",
	}

	s := newTestStore(t)
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('tiles')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	copied := make(map[string]bool, len(tileCopyColumns))
	for _, c := range tileCopyColumns {
		copied[c] = true
	}
	var schemaCols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		schemaCols = append(schemaCols, name)
		if _, deliberate := notCopied[name]; deliberate {
			if copied[name] {
				t.Errorf("column %q is on both tileCopyColumns and the not-copied list", name)
			}
			continue
		}
		if !copied[name] {
			t.Errorf("tiles column %q is not copied by insertTileCopy and not on the deliberate not-copied list — a clone would silently lose it", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// And the inverse: tileCopyColumns must not name a column the schema
	// doesn't have (a rename/typo would otherwise fail only at clone time).
	schema := make(map[string]bool, len(schemaCols))
	for _, c := range schemaCols {
		schema[c] = true
	}
	for _, c := range tileCopyColumns {
		if !schema[c] {
			t.Errorf("tileCopyColumns names %q which is not a column of tiles (typo or stale rename); schema has: %s", c, strings.Join(schemaCols, ", "))
		}
	}
}
