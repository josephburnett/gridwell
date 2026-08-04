package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// searchDefaultLimit caps a query that doesn't bring its own limit.
const searchDefaultLimit = 20

// Search implements localdb's side of the one find verb (issue #244,
// generalizing #234's LocateTile). The shared grammar
// (rpc.ParseSearchQuery) decides the mode: `id:` is the exact locate —
// the tile plus its containing-well chain, what LocateTile was — and
// free text matches tile NAMES (alt_text) and TEXT BODIES
// case-insensitively, names ranking above bodies. Every result is a
// PLACE (tile + path). Scratch-grid (ephemeral) tiles never surface: a
// result is a promise you can go there, and they die on ascent. A query
// that matches nothing returns empty results, never an error.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]rpc.SearchResult, error) {
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	q := rpc.ParseSearchQuery(query)
	if q.ID != "" {
		t, err := s.GetTile(ctx, q.ID)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		path, err := s.wellChainFor(ctx, t)
		if err != nil {
			return nil, err
		}
		return []rpc.SearchResult{{Tile: *t, Path: path, Score: 1}}, nil
	}
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	if needle == "" {
		return nil, nil
	}
	scratch := s.searchScratchGrid(ctx)

	var out []rpc.SearchResult
	seen := map[string]bool{}
	appendHit := func(id int64, snippet string, score float64) error {
		t, err := s.loadTile(ctx, s.db, id)
		if err != nil {
			return err
		}
		if seen[t.ID] || t.GridID == scratch {
			return nil
		}
		seen[t.ID] = true
		path, err := s.wellChainFor(ctx, t)
		if err != nil {
			return err
		}
		out = append(out, rpc.SearchResult{Tile: *t, Path: path, Snippet: snippet, Score: score})
		return nil
	}

	// Name hits first (instr, not LIKE — no pattern-escaping trap).
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, alt_text FROM tiles
		 WHERE alt_text != '' AND instr(lower(alt_text), ?) > 0
		 ORDER BY id LIMIT ?`, needle, limit)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id   int64
		text string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.text); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range hits {
		if len(out) >= limit {
			return out, nil
		}
		if err := appendHit(h.id, searchSnippet(h.text, needle), 1); err != nil {
			return nil, err
		}
	}

	// Body hits: the text blob a text tile owns.
	rows, err = s.db.QueryContext(ctx,
		`SELECT t.id, CAST(b.data AS TEXT) FROM tiles t
		 JOIN blobs b ON t.blob_id = b.id
		 WHERE t.kind = 'text' AND instr(lower(CAST(b.data AS TEXT)), ?) > 0
		 ORDER BY t.id LIMIT ?`, needle, limit)
	if err != nil {
		return nil, err
	}
	hits = hits[:0]
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.text); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range hits {
		if len(out) >= limit {
			return out, nil
		}
		if err := appendHit(h.id, searchSnippet(h.text, needle), 0.5); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// searchScratchGrid resolves the scratch grid's qualified-free id string
// for the surface filter; "" (matches no row's grid) when unresolvable.
func (s *Store) searchScratchGrid(ctx context.Context) string {
	id, err := s.ScratchGridID(ctx)
	if err != nil {
		return ""
	}
	return id
}

// searchSnippet is a one-line window around the first (case-insensitive)
// occurrence of needle in text.
func searchSnippet(text, needle string) string {
	const around = 40
	i := strings.Index(strings.ToLower(text), needle)
	if i < 0 {
		i = 0
	}
	start := i - around
	if start < 0 {
		start = 0
	}
	end := i + len(needle) + around
	if end > len(text) {
		end = len(text)
	}
	snip := strings.Join(strings.Fields(text[start:end]), " ")
	return snip
}

// wellChainFor returns the containing-well chain for tile t, outermost
// first — empty for a tile at a root. The upward walk is the same
// server-derived parent chain wellWouldContainItself trusts: each
// interior child grid hangs off exactly one well by construction.
func (s *Store) wellChainFor(ctx context.Context, t *rpc.Tile) ([]rpc.Tile, error) {
	grid, err := parseID(t.GridID)
	if err != nil {
		return nil, ErrNotFound
	}
	var wells []rpc.Tile
	for {
		var wellID int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM tiles WHERE child_grid_id = ?`, grid).Scan(&wellID)
		if errors.Is(err, sql.ErrNoRows) {
			reverse(wells)
			return wells, nil
		}
		if err != nil {
			return nil, err
		}
		w, err := s.loadTile(ctx, s.db, wellID)
		if err != nil {
			return nil, err
		}
		wells = append(wells, *w)
		grid, err = parseID(w.GridID)
		if err != nil {
			return nil, ErrNotFound
		}
	}
}

// reverse flips the collected leaf-first walk into outermost-first order.
func reverse(ts []rpc.Tile) {
	for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
		ts[i], ts[j] = ts[j], ts[i]
	}
}
