package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// GetGrid returns the grid plus all of its tiles. It is a pure read: the
// local store holds only Gridwell-owned grids now, so there is no host-state
// reconciliation here — that lives in the fs/proc plugins, which the server
// routes to directly.
func (s *Store) GetGrid(ctx context.Context, gridID string) (*rpc.GetGridResponse, error) {
	id, err := parseID(gridID)
	if err != nil {
		return nil, ErrNotFound
	}
	g, err := s.loadGrid(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	tiles, err := s.loadTilesInGrid(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return &rpc.GetGridResponse{Grid: *g, Tiles: tiles}, nil
}

// gridReader is the interface needed to read grid/tile rows. Both *sql.DB and
// *sql.Tx satisfy it. It is also used by helpers that only need QueryRowContext
// (e.g. rootGridID, readFloatKey) since *sql.DB and *sql.Tx satisfy the
// superset anyway.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*rpc.Grid, error) {
	var g rpc.Grid
	err := q.QueryRowContext(ctx,
		`SELECT id, object_id, version FROM grids WHERE id = ?`, gridID,
	).Scan(&g.ID, &g.ObjectID, &g.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// tileColumns is the column list for reading a tile row. Keep in sync with
// scanTile.
const tileColumns = `id, object_id, version, grid_id, kind, x, y, w, h,
	view_x, view_y, view_zoom, child_grid_id,
	text_x, text_y, text_w, text_h, text_mode, blob_id,
	url_string, preview_blob_id, alt_text, content_zoom, url_history,
	link_target_id`

// scanTile scans a single row into an rpc.Tile. It expects the columns to
// match tileColumns in order.
func scanTile(scanner interface {
	Scan(dest ...any) error
}) (*rpc.Tile, error) {
	var (
		n          rpc.Tile
		childGrid  sql.NullString
		blob       sql.NullInt64
		urlStr     sql.NullString
		previewBID sql.NullInt64
		textMode   sql.NullString
		urlHist    sql.NullString
		linkTarget sql.NullString
	)
	if err := scanner.Scan(
		&n.ID, &n.ObjectID, &n.Version, &n.GridID, &n.Kind,
		&n.X, &n.Y, &n.W, &n.H,
		&n.ViewX, &n.ViewY, &n.ViewZoom, &childGrid,
		&n.TextX, &n.TextY, &n.TextW, &n.TextH, &textMode, &blob,
		&urlStr, &previewBID, &n.AltText, &n.ContentZoom, &urlHist,
		&linkTarget,
	); err != nil {
		return nil, err
	}
	if linkTarget.Valid {
		n.LinkTargetID = linkTarget.String
	}
	if childGrid.Valid {
		n.ChildGridID = childGrid.String
	}
	if blob.Valid {
		n.BlobID = blob.Int64
	}
	if urlStr.Valid {
		n.URLString = urlStr.String
	}
	if urlHist.Valid {
		n.URLHistory = urlHist.String
	}
	if previewBID.Valid {
		n.PreviewBlobID = previewBID.Int64
	}
	if textMode.Valid {
		n.TextMode = textMode.String
	}
	return &n, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*rpc.Tile, error) {
	row := q.QueryRowContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE id = ?`, tileID)
	n, err := scanTile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]rpc.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rpc.Tile
	for rows.Next() {
		n, err := scanTile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// GetTile returns a single tile by ID.
func (s *Store) GetTile(ctx context.Context, tileID string) (*rpc.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.loadTile(ctx, s.db, id)
}

// GetTilePreview returns the JPEG bytes for a tile's current preview —
// URL tiles store the last-frozen page render; shell tiles store the
// last-frozen terminal frame. Returns nil for tiles that don't have a
// preview yet (fresh palette drop, never refreshed).
func (s *Store) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	var previewBID sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ?`, id).Scan(&previewBID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !previewBID.Valid {
		return nil, nil
	}
	return s.GetBlob(ctx, previewBID.Int64)
}

// AllShellTileIDs returns the ids of every tile with kind='shell' in
// the database. Used by the server's startup orphan-cleanup pass: a
// session on the gridwell tmux socket whose id isn't in this list is
// a leftover from a tile deleted while gridwell was down, and gets
// killed.
func (s *Store) AllShellTileIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tiles WHERE kind = 'shell'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ShellTileExists reports whether a shell tile with the given row id is
// still present. The DeleteTile handler uses it to decide whether the tmux
// session keyed to that id is now orphaned: the session must die only when
// this exact id is truly gone. (A cloned shell is an independent copy with
// its own id and no session, so deleting it never affects the original.)
func (s *Store) ShellTileExists(ctx context.Context, id string) (bool, error) {
	idInt, err := parseID(id)
	if err != nil {
		return false, nil
	}
	var n int64
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM tiles WHERE id = ? AND kind = 'shell'`, idInt).Scan(&n)
	return n > 0, err
}

// bumpTileVersion increments a tile row's version by 1.
func bumpTileVersion(ctx context.Context, tx *sql.Tx, tileID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE tiles SET version = version + 1 WHERE id = ?`, tileID)
	return err
}

// bumpGridVersion increments a grid row's version by 1 and stamps
// updated_at. A grid's version moves on structural change (tile added /
// removed / moved, source reconcile), so this is the right place to record
// "last touched" for the planned recency feature.
func (s *Store) bumpGridVersion(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE grids SET version = version + 1, updated_at = ? WHERE id = ?`,
		s.now().Unix(), gridID)
	return err
}

// overlapsExisting reports whether the rectangle (x,y,w,h) overlaps any tile
// in the grid except those whose id is in the excludeIDs set.
func overlapsExisting(ctx context.Context, q gridReader, gridID, x, y, w, h int64, excludeIDs ...int64) (bool, error) {
	args := []any{gridID, x + w, x, y + h, y}
	excl := ""
	if len(excludeIDs) > 0 {
		excl = " AND id NOT IN ("
		for i, id := range excludeIDs {
			if i > 0 {
				excl += ","
			}
			excl += "?"
			args = append(args, id)
		}
		excl += ")"
	}
	q1 := fmt.Sprintf(`
		SELECT 1 FROM tiles
		WHERE grid_id = ?
		  AND x < ? AND (x + w) > ?
		  AND y < ? AND (y + h) > ?
		%s LIMIT 1`, excl)
	var n int
	err := q.QueryRowContext(ctx, q1, args...).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
