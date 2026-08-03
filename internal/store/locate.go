package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// LocateTile returns the containing-well chain for a tile, outermost first
// (issue #234): the well rows a descent from the root grid passes through
// to reach the tile's grid — empty for a tile sitting at the root. Ids are
// immutable; paths are not, so a stored reference (a workspace leaf whose
// tile has since been moved) re-derives its path from the id through this
// read. The upward walk is the same server-derived parent chain
// wellWouldContainItself trusts: each interior child grid hangs off
// exactly one well by construction.
func (s *Store) LocateTile(ctx context.Context, tileID string) ([]rpc.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	t, err := s.loadTile(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
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
			// Reached a root (or the scratch grid): the chain is complete.
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
