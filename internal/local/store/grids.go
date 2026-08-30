package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/api/rpc"
)

// GetGrid returns the grid plus all of its tiles. It is a pure read: home
// holds only Gridwell-owned grids, so there is no host-state reconciliation
// here. That lives in the plugins, which the server routes to.
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

// gridReader is the interface needed to read grid and tile rows. Both *sql.DB
// and *sql.Tx satisfy it, so helpers that need only QueryRowContext take it
// too.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// gridColumns is the SELECT list for a grid row: the grids columns that are on
// the wire. Everything else on rpc.Grid — writable, node_ns, menu_entries —
// is derived by the serving node and never read from a row.
var gridColumns = wireColumns(gridsColumns)

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*rpc.Grid, error) {
	var g rpc.Grid
	err := q.QueryRowContext(ctx,
		`SELECT `+gridColumns+` FROM grids WHERE id = ? AND ns = ''`, gridID,
	).Scan(scanDests(gridsColumns, &g)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// tileColumns is the SELECT list for reading a tile row, and scanTile reads it
// back. Both derive from the one column descriptor in columns.go, in the same
// order, so the list and the scan cannot fall out of step.
var tileColumns = wireColumns(tilesColumns)

// scanTile scans a single row into an rpc.Tile.
func scanTile(scanner interface {
	Scan(dest ...any) error
}) (*rpc.Tile, error) {
	var n rpc.Tile
	if err := scanner.Scan(scanDests(tilesColumns, &n)...); err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*rpc.Tile, error) {
	row := q.QueryRowContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE id = ? AND ns = ''`, tileID)
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
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ? AND ns = '' ORDER BY id`, gridID)
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

// GetTilePreview returns the JPEG bytes for a tile's current preview: a url
// tile stores the last-frozen page render, a shell tile the last-frozen
// terminal frame. Returns nil for a tile that has no preview yet.
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

// ShellTileExists reports whether a shell tile with the given row id is still
// present. The DeleteTile handler uses it to decide whether the tmux session
// keyed to that id is orphaned: the session must die only when this exact id
// is gone. A cloned shell is an independent copy with its own id and no
// session, so deleting it never affects the original.
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

// bumpGridVersion increments a grid row's version by 1 and stamps updated_at.
// A grid's version moves on a structural change: a tile added, removed, or
// moved, or a source reconcile.
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
