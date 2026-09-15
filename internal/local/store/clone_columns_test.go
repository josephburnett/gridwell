package store

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Clone is an eager, complete copy, so everything the user set on the source
// must be on the copy. An INSERT that omits content_zoom, url_history or
// alt_user silently loses a content zoom, a back-stack, or the latch that
// keeps the next title capture off a name the user chose.
func TestClonePreservesAllContentColumns(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateURL(ctx, root, 0, 0, 2, 1, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}

	// A freeze that stored a navigation back-stack...
	history := `[{"url":"https://example.com"},{"url":"https://example.com/page"}]`
	if _, err := s.SetURLState(ctx, tile.Id, nil, "https://example.com/page", "", history); err != nil {
		t.Fatal(err)
	}
	// ...a content zoom, which is framing and bumps no version...
	if _, err := s.SetContentZoom(ctx, tile.Id, 1.5); err != nil {
		t.Fatal(err)
	}
	// ...and a user rename, which latches alt_user.
	if err := s.SetTileAlt(ctx, tile.Id, "my page", true); err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId:     tile.Id,
		DestGridId: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	if clone.ContentZoom != 1.5 {
		t.Errorf("clone content_zoom = %v, want 1.5 (the source's zoom)", clone.ContentZoom)
	}
	if clone.UrlHistory != history {
		t.Errorf("clone url_history = %q, want the source's back-stack", clone.UrlHistory)
	}
	if clone.AltText != "my page" {
		t.Errorf("clone alt_text = %q, want %q", clone.AltText, "my page")
	}

	// The behavioral half of alt_user: an automatic (non-user) title capture
	// on the CLONE must defer to the copied user-owned name, exactly as it
	// would on the source.
	if err := s.SetTileAlt(ctx, clone.Id, "Captured Page Title", false); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetTile(ctx, clone.Id)
	if err != nil {
		t.Fatal(err)
	}
	if after.AltText != "my page" {
		t.Errorf("automatic capture clobbered the clone's user-owned name: alt = %q (alt_user latch was not copied)", after.AltText)
	}
}

// Every tiles column is either copied or carries a written reason it is not.
// The list is the descriptor, so what needs holding is the claim behind each
// exclusion.
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

// The copy is written by name, so a value map missing a copied column is an
// error at the copy, not a row with a silently defaulted column.
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

// The descriptor renders the DDL, so the columns SQLite has are exactly the
// columns described. A typo in a name would otherwise fail only when a query
// naming it runs.
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
