package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// GetGrid returns the grid plus all of its tiles. It is a pure read: home holds
// only Gridwell-owned grids, so there is no host-state reconciliation here.
func (s *Store) GetGrid(ctx context.Context, gridID string) (*gridwellv1.GetGridResponse, error) {
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
	return &gridwellv1.GetGridResponse{Grid: g, Tiles: tiles}, nil
}

// gridReader is what reading grid and tile rows needs; *sql.DB and *sql.Tx
// both satisfy it.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// gridColumns is the SELECT list for a grid row. Everything else on
// gridwellv1.Grid is derived by the serving node and never read from a row.
var gridColumns = wireColumns(gridsColumns)

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*gridwellv1.Grid, error) {
	var g gridwellv1.Grid
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

// tileColumns is the SELECT list for a tile row and scanTile reads it back.
// Both derive from columns.go, so they cannot fall out of step.
var tileColumns = wireColumns(tilesColumns)

// scanTile scans a single row into a Tile. extra takes the destinations of any
// columns a caller selected beyond the descriptor's, in their SELECT order.
func scanTile(scanner interface {
	Scan(dest ...any) error
}, extra ...any) (*gridwellv1.Tile, error) {
	var n gridwellv1.Tile
	if err := scanner.Scan(append(scanDests(tilesColumns, &n), extra...)...); err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*gridwellv1.Tile, error) {
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

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]*gridwellv1.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ? AND ns = '' ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (*gridwellv1.Tile, error) { return scanTile(rows) })
}

// insertGrid mints an empty grid row. The migration chain keeps its own copy
// of this INSERT: a migration step must materialize the shape of the version
// it is building, not the current one.
func insertGrid(ctx context.Context, tx *sql.Tx, now int64) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ancestryCap bounds the well-parent walk, which a cycle would loop forever.
const ancestryCap = 256

// gridInSubtree walks the well-parent chain from gridID up to a root, reporting
// whether rootID is on the way. It is the one ancestor walk: the trash's
// bypass check and placement's own-subtree refusal are both this question.
func gridInSubtree(ctx context.Context, tx *sql.Tx, gridID, rootID int64) (bool, error) {
	g := gridID
	for i := 0; i < ancestryCap; i++ {
		if g == rootID {
			return true, nil
		}
		var parent int64
		err := tx.QueryRowContext(ctx,
			`SELECT grid_id FROM tiles WHERE child_grid_id = ?`, g).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		g = parent
	}
	return false, fmt.Errorf("grid %d: ancestry deeper than %d (cycle?)", gridID, ancestryCap)
}

// GetTile returns a single tile by ID.
func (s *Store) GetTile(ctx context.Context, tileID string) (*gridwellv1.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.loadTile(ctx, s.db, id)
}

// GetTilePreview returns a tile's last-frozen JPEG, or nil when it has none.
func (s *Store) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.tilePreview(ctx, "", id)
}

// Preview is a plugin tile's last-frozen JPEG, nil when it has none.
func (n *Namespace) Preview(tileID int64) ([]byte, error) {
	return n.s.tilePreview(context.Background(), n.ns, tileID)
}

// tilePreview is the one frozen-face read.
func (s *Store) tilePreview(ctx context.Context, ns string, id int64) (jpeg []byte, err error) {
	err = s.readSnapshot(ctx, func(tx *sql.Tx) error {
		var previewBID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ? AND ns = ?`, id, ns).Scan(&previewBID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || !previewBID.Valid {
			return err
		}
		jpeg, _, err = readBlob(ctx, tx, previewBID.Int64)
		return err
	})
	return jpeg, err
}

// SessionNamers is how many shell rows name one tmux session, and how many of
// those carry a frozen face.
type SessionNamers struct {
	Rows, Faced int64
}

// ShellSessionNamers counts the shell rows naming the session key, a trashed
// row included: a session with no rows is an orphan, and one whose rows have
// no face was never started.
func (s *Store) ShellSessionNamers(ctx context.Context, key string) (SessionNamers, error) {
	var n SessionNamers
	keyInt, err := parseID(key)
	if err != nil {
		return n, nil
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(1), COUNT(preview_blob_id) FROM tiles
		WHERE kind = 'shell' AND link_target_id IS NULL
		  AND COALESCE(shell_session, id) = ?`, keyInt).Scan(&n.Rows, &n.Faced)
	return n, err
}

// bumpTileVersion increments a tile row's version by 1.
func bumpTileVersion(ctx context.Context, tx *sql.Tx, tileID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE tiles SET version = version + 1 WHERE id = ?`, tileID)
	return err
}

// bumpGridVersion increments a grid row's version and stamps updated_at. A
// grid's version moves on a structural change only.
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
