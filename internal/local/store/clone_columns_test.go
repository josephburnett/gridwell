package store

import (
	"context"
	"reflect"
	"sort"
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
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID,
		URL:    "https://example.com/page", History: history,
	}); err != nil {
		t.Fatal(err)
	}
	// ...a content zoom (issue #82; framing, no version bump)...
	if _, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
		TileID: tile.ID, ContentZoom: 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	// ...and a USER rename (issue #61; latches alt_user).
	tileIDInt, _ := parseID(tile.ID)
	if err := s.SetTileAlt(ctx, tileIDInt, "my page", true); err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID:     tile.ID,
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

// TestEveryTileColumnIsCopiedOrExcused: a clone is an EAGER, COMPLETE copy,
// so every tiles column must be either copied or carry a written reason it is
// not. Before the column descriptor this was a lint over a hand-listed
// INSERT; now the list IS the descriptor, and what still needs holding is the
// claim behind each exclusion.
func TestEveryTileColumnIsCopiedOrExcused(t *testing.T) {
	excused := map[string]bool{}
	for _, c := range tilesColumns {
		if c.noCopy != "" {
			excused[c.name] = true
		}
	}
	// Named exclusions only — a new column defaults to being copied, which is
	// the safe direction.
	want := map[string]bool{"id": true, "ns": true, "key": true, "tombstoned": true}
	if !reflect.DeepEqual(excused, want) {
		t.Errorf("clone exclusions = %v, want %v — a column left out of a clone needs a reason on its descriptor entry", excused, want)
	}
}

// TestCopyBindingRefusesAnIncompleteCopy pins the mechanism that makes "add a
// column, forget the clone path" loud: the copy is written BY NAME, and a
// value map missing any copied column is an error at the copy, not a row with
// a silently defaulted column.
func TestCopyBindingRefusesAnIncompleteCopy(t *testing.T) {
	full := map[string]any{}
	for _, c := range copyColumns() {
		full[c] = 0
	}
	if _, _, err := copyBinding(full); err != nil {
		t.Fatalf("a complete map must bind: %v", err)
	}
	delete(full, "content_zoom")
	_, _, err := copyBinding(full)
	if err == nil || !strings.Contains(err.Error(), "content_zoom") {
		t.Errorf("a missing column must name itself; got %v", err)
	}
	full["content_zoom"] = 0
	full["not_a_column"] = 0
	if _, _, err := copyBinding(full); err == nil {
		t.Error("a value for a non-copied column must be refused")
	}
}

// TestDescriptorMatchesLiveSchema: the descriptor renders the DDL, so the
// columns SQLite actually has must be exactly the columns described. This is
// what catches a typo in a name — which would otherwise fail only when a
// query naming it runs.
func TestDescriptorMatchesLiveSchema(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		table string
		want  []string
	}{
		{"tiles", columnNames(tilesColumns)},
		{"grids", columnNames(gridsColumns)},
	} {
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, tc.table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		rows.Close()
		sort.Strings(got)
		want := append([]string(nil), tc.want...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, descriptor says %v", tc.table, got, want)
		}
	}
}

func columnNames[T any](cols []column[T]) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.name
	}
	return out
}
