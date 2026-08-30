package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/api/rpc"
)

// PlaceTile is the single placement writeback: placement is one fact,
// (grid_id, x, y, w, h), and this verb owns all of it. A move is a grid
// change, a resize a footprint change, and both at once are one write. It is
// id-addressed; there is no descent path.
//
// Placement is layout, not content: no version claim, no version bump. A drag
// is an explicit act on a tile the user can see, so when two clients race,
// whoever moved it last moved it, and the tile event reconciles. The one thing
// a race could corrupt, two tiles in one cell, is refused by the overlap check
// below, inside this same transaction.
//
// Moving a well into its own subtree is refused by walking ancestors of the
// destination grid, in wellWouldContainItself: a fact the server derives
// itself rather than trusting a client-supplied path.
func (s *Store) PlaceTile(ctx context.Context, req *rpc.PlaceTileRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	destGridID, err := parseID(req.GridID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		srcGrid, err := parseID(n.GridID)
		if err != nil {
			return fmt.Errorf("tile %d: bad grid_id %q: %w", tileID, n.GridID, err)
		}
		if _, err := s.loadGrid(ctx, tx, destGridID); err != nil {
			return fmt.Errorf("%w: destination grid %d: %v", ErrInvalidArgument, destGridID, err)
		}
		if err := s.wellWouldContainItself(ctx, tx, n, destGridID); err != nil {
			return err
		}

		crossGrid := destGridID != srcGrid
		var excludes []int64
		if !crossGrid {
			excludes = []int64{tileID}
		}
		over, err := overlapsExisting(ctx, tx, destGridID, req.X, req.Y, req.W, req.H, excludes...)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ?, x = ?, y = ?, w = ?, h = ?, updated_at = ? WHERE id = ?`,
			destGridID, req.X, req.Y, req.W, req.H, s.now().Unix(), tileID); err != nil {
			return err
		}
		if crossGrid {
			if err := s.bumpGridVersion(ctx, tx, srcGrid); err != nil {
				return err
			}
			if err := s.bumpGridVersion(ctx, tx, destGridID); err != nil {
				return err
			}
			*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{
				GridID: strconv.FormatInt(srcGrid, 10),
				TileID: strconv.FormatInt(tileID, 10),
			}})
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// wellWouldContainItself refuses placing a well tile inside its own subtree:
// a destination that is the well's child grid, or any grid beneath it. The
// check walks up from the destination grid through parent wells. Each interior
// child grid hangs off exactly one well by construction — wells are created
// with fresh grids, clones deep-copy, and placement carries the well row whole
// — so the ancestor chain is a server-derived fact and needs no client path.
// Non-well tiles and exit wells, whose qualified child_grid_id names another
// plugin's subtree, have no local subtree and pass trivially.
func (s *Store) wellWouldContainItself(ctx context.Context, tx *sql.Tx, n *rpc.Tile, destGridID int64) error {
	if !isWellKind(n.Kind) {
		return nil
	}
	childGrid, err := strconv.ParseInt(n.ChildGridID, 10, 64)
	if err != nil {
		return nil // qualified, an exit well or link: no local subtree
	}
	g := destGridID
	for {
		if g == childGrid {
			return fmt.Errorf("%w: cannot place a well inside its own subtree", ErrInvalidArgument)
		}
		var parent int64
		err := tx.QueryRowContext(ctx,
			`SELECT grid_id FROM tiles WHERE child_grid_id = ?`, g).Scan(&parent)
		if err == sql.ErrNoRows {
			return nil // reached a root or scratch: the destination is outside
		}
		if err != nil {
			return err
		}
		g = parent
	}
}
