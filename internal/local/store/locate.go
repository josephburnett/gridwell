package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Find implements home's side of the one find verb. rpc.ParseFindQuery
// decides the mode: id: is the exact locate, and free text matches names and
// text bodies case-insensitively, names ranking above bodies. The request's
// filters narrow every mode (tileFilter); an empty query with a filter lists
// what passes, newest first. Every result is a tile plus its path. Scratch-grid
// tiles never surface, because a result is a promise you can go there and they
// die on ascent. Matching nothing returns empty results, never an error.
func (s *Store) Find(ctx context.Context, req *gridwellv1.FindRequest) ([]*gridwellv1.FindResult, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = rpc.FindDefaultLimit
	}
	f := tileFilter{req: req}
	if req.GridId != "" {
		gid, err := parseID(req.GridId)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid grid_id %q", ErrInvalidArgument, req.GridId)
		}
		f.grid = gid
	}
	q := rpc.ParseFindQuery(req.Query)
	if q.ID != "" {
		return s.locate(ctx, q.ID, f)
	}
	f.scratch = s.findScratchGrid(ctx)
	where, args := f.sql()

	var out []*gridwellv1.FindResult
	seen := map[int64]bool{}
	appendHits := func(snippet func(text string) string, score float64, q string, qargs ...any) error {
		rows, err := s.db.QueryContext(ctx, q, append(qargs, limit)...)
		if err != nil {
			return err
		}
		hits, err := collect(rows, func(rows *sql.Rows) (findHit, error) {
			var h findHit
			err := rows.Scan(&h.id, &h.text)
			return h, err
		})
		if err != nil {
			return err
		}
		for _, h := range hits {
			if len(out) >= limit {
				return nil
			}
			if seen[h.id] {
				continue
			}
			seen[h.id] = true
			t, err := s.loadTile(ctx, s.db, h.id)
			if err != nil {
				return err
			}
			path, err := s.doorChainFor(ctx, t)
			if err != nil {
				return err
			}
			out = append(out, &gridwellv1.FindResult{Tile: t, Path: path, Snippet: snippet(h.text), Score: score})
		}
		return nil
	}
	none := func(string) string { return "" }

	needle := strings.ToLower(strings.TrimSpace(q.Text))
	if needle == "" {
		if !rpc.FindFiltered(req) {
			return nil, nil
		}
		err := appendHits(none, 1, `SELECT t.id, '' FROM tiles t WHERE `+where+
			` ORDER BY `+f.newest()+` DESC, t.id DESC LIMIT ?`, args...)
		return out, err
	}
	near := func(text string) string { return findSnippet(text, needle) }
	// instr rather than LIKE, so there is no pattern-escaping trap.
	if err := appendHits(near, 1, `SELECT t.id, t.alt_text FROM tiles t
		WHERE t.alt_text != '' AND instr(lower(t.alt_text), ?) > 0 AND `+where+`
		ORDER BY t.id LIMIT ?`, append([]any{needle}, args...)...); err != nil {
		return nil, err
	}
	if err := appendHits(near, 0.5, `SELECT t.id, CAST(b.data AS TEXT) FROM tiles t
		JOIN blobs b ON t.blob_id = b.id
		WHERE t.kind = 'text' AND instr(lower(CAST(b.data AS TEXT)), ?) > 0 AND `+where+`
		ORDER BY t.id LIMIT ?`, append([]any{needle}, args...)...); err != nil {
		return nil, err
	}
	return out, nil
}

// locate is the id: mode: the one tile, when it passes the filters, and its
// path. A scratch tile is found by its id, the id being the promise.
func (s *Store) locate(ctx context.Context, tileID string, f tileFilter) ([]*gridwellv1.FindResult, error) {
	t, err := s.GetTile(ctx, tileID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rpc.FindFiltered(f.req) {
		id, err := parseID(t.Id)
		if err != nil {
			return nil, err
		}
		where, args := f.sql()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tiles t WHERE t.id = ? AND `+where,
			append([]any{id}, args...)...).Scan(&n); err != nil || n == 0 {
			return nil, err
		}
	}
	path, err := s.doorChainFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return []*gridwellv1.FindResult{{Tile: t, Path: path, Score: 1}}, nil
}

type findHit struct {
	id   int64
	text string
}

// tileFilter is a find's filters as one SQL condition over `tiles t`, the
// one place they mean anything: home's find and a plugin's rows
// (Namespace.FindRows) both read it. grid 0 is no grid scope; scratch 0
// excludes no grid.
type tileFilter struct {
	ns      string
	req     *gridwellv1.FindRequest
	grid    int64
	scratch int64
}

func (f tileFilter) sql() (string, []any) {
	conds := []string{"t.ns = ?", "t.tombstoned = 0"}
	args := []any{f.ns}
	if f.scratch != 0 {
		conds = append(conds, "t.grid_id != ?")
		args = append(args, f.scratch)
	}
	switch {
	case f.grid != 0 && f.req.Recurse:
		// An owned door's child grid is an integer; a link door's is a
		// qualified path elsewhere, which recursion does not follow.
		conds = append(conds, `t.grid_id IN (WITH RECURSIVE scope(id) AS (
			SELECT ? UNION
			SELECT w.child_grid_id FROM tiles w JOIN scope ON w.grid_id = scope.id
			WHERE w.kind = 'door' AND w.ns = ? AND w.tombstoned = 0 AND typeof(w.child_grid_id) = 'integer')
			SELECT id FROM scope)`)
		args = append(args, f.grid, f.ns)
	case f.grid != 0:
		conds = append(conds, "t.grid_id = ?")
		args = append(args, f.grid)
	}
	for _, r := range []struct {
		col string
		r   *gridwellv1.TimeRange
	}{
		{"t.created_at", f.req.Created},
		{"t.updated_at", f.req.Modified},
		{contentAtSQL, f.req.ContentChanged},
	} {
		if r.r.GetFrom() > 0 {
			conds = append(conds, r.col+" >= ?")
			args = append(args, r.r.GetFrom())
		}
		if r.r.GetTo() > 0 {
			conds = append(conds, r.col+" <= ?")
			args = append(args, r.r.GetTo())
		}
	}
	if f.req.Kind != "" {
		conds = append(conds, "t.kind = ?")
		args = append(args, f.req.Kind)
	}
	if f.req.Selected {
		conds = append(conds, `instr(',' || (SELECT g.selection FROM grids g WHERE g.id = t.grid_id) || ',',
			',' || t.id || ',') > 0`)
	}
	return strings.Join(conds, " AND "), args
}

// newest is the time an empty query's hits are ordered by: the one it
// filters on, content before modified, else when the tile was made.
func (f tileFilter) newest() string {
	switch {
	case f.req.ContentChanged != nil:
		return contentAtSQL
	case f.req.Modified != nil:
		return "t.updated_at"
	}
	return "t.created_at"
}

// findScratchGrid resolves the scratch grid's id for the surface filter, 0
// when it is unresolvable.
func (s *Store) findScratchGrid(ctx context.Context) int64 {
	id, err := s.ScratchGridID(ctx)
	if err != nil {
		return 0
	}
	gid, err := parseID(id)
	if err != nil {
		return 0
	}
	return gid
}

// findSnippet is a one-line window around the first case-insensitive
// occurrence of needle in text.
func findSnippet(text, needle string) string {
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

// doorChainFor returns the containing-door chain for tile t, outermost first,
// and empty for a tile at a root. It is the same server-derived parent chain
// doorWouldContainItself trusts.
func (s *Store) doorChainFor(ctx context.Context, t *gridwellv1.Tile) ([]*gridwellv1.Tile, error) {
	grid, err := parseID(t.GridId)
	if err != nil {
		return nil, ErrNotFound
	}
	var doors []*gridwellv1.Tile
	for {
		var doorID int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM tiles WHERE child_grid_id = ?`, grid).Scan(&doorID)
		if errors.Is(err, sql.ErrNoRows) {
			reverse(doors)
			return doors, nil
		}
		if err != nil {
			return nil, err
		}
		w, err := s.loadTile(ctx, s.db, doorID)
		if err != nil {
			return nil, err
		}
		doors = append(doors, w)
		grid, err = parseID(w.GridId)
		if err != nil {
			return nil, ErrNotFound
		}
	}
}

func reverse(ts []*gridwellv1.Tile) {
	for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
		ts[i], ts[j] = ts[j], ts[i]
	}
}

// FindRow is a plugin row a find found by its facts alone: its id, the
// grid it sits on and its key there.
type FindRow struct {
	ID, GridID int64
	Key        string
}

// FindPassing answers which of ids pass req's kind, time and selection filters,
// each with the time an empty query orders by. A plugin's grid scope is its
// caller's, the grids being grids the plugin names.
func (n *Namespace) FindPassing(ids []int64, req *gridwellv1.FindRequest) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	f := tileFilter{ns: n.ns, req: req}
	where, args := f.sql()
	in := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	qargs := make([]any, 0, len(ids)+len(args))
	for _, id := range ids {
		qargs = append(qargs, id)
	}
	rows, err := n.s.db.Query(`SELECT t.id, `+f.newest()+` FROM tiles t WHERE t.id IN (`+in+`) AND `+where,
		append(qargs, args...)...)
	if err != nil {
		return nil, err
	}
	pairs, err := collect(rows, func(rows *sql.Rows) ([2]int64, error) {
		var p [2]int64
		err := rows.Scan(&p[0], &p[1])
		return p, err
	})
	for _, p := range pairs {
		out[p[0]] = p[1]
	}
	return out, err
}

// FindRows lists this namespace's rows passing req's kind, time and
// selection filters, newest first, at most limit: all an empty query with no grid can
// find, since an entry with no row has no time and is in no selection.
func (n *Namespace) FindRows(req *gridwellv1.FindRequest, limit int) ([]FindRow, error) {
	f := tileFilter{ns: n.ns, req: req}
	where, args := f.sql()
	rows, err := n.s.db.Query(`SELECT t.id, t.grid_id, t.key FROM tiles t WHERE `+where+
		` ORDER BY `+f.newest()+` DESC, t.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (FindRow, error) {
		var r FindRow
		err := rows.Scan(&r.ID, &r.GridID, &r.Key)
		return r, err
	})
}
