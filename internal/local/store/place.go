package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// placementSet is the footprint a placement write touches, here and in
// Namespace.Place. The grid rides beside it on home's side alone.
const placementSet = `x = ?, y = ?, w = ?, h = ?`

// PlaceTile is the single placement writeback: placement is one fact,
// (grid_id, x, y, w, h), and this verb owns all of it, id-addressed with no
// descent path. Placement is layout, not content: no claim, no bump, and when
// two clients race, whoever moved it last moved it. The one thing a race could
// corrupt, two tiles in one cell, is refused by the overlap check in this same
// transaction. Moving a well into its own subtree is refused by
// wellWouldContainItself, walking a chain the server derives itself.
func (s *Store) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	tileID, err := parseID(req.TileId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	destGridID, err := parseID(req.GridId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		srcGrid, err := parseID(n.GridId)
		if err != nil {
			return fmt.Errorf("tile %d: bad grid_id %q: %w", tileID, n.GridId, err)
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
			`UPDATE tiles SET grid_id = ?, `+placementSet+`, updated_at = ? WHERE id = ?`,
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
			*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{
				GridId: strconv.FormatInt(srcGrid, 10),
				TileId: strconv.FormatInt(tileID, 10),
			}}})
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// wellWouldContainItself refuses placing a well inside its own subtree, walking
// up from the destination grid through parent wells (gridInSubtree). Each
// interior child grid hangs off exactly one well by construction, so the
// ancestor chain is a server-derived fact and needs no client path. Non-well
// tiles and exit wells have no local subtree and pass trivially.
func (s *Store) wellWouldContainItself(ctx context.Context, tx *sql.Tx, n *gridwellv1.Tile, destGridID int64) error {
	if !isWellKind(n.Kind) {
		return nil
	}
	childGrid, err := strconv.ParseInt(n.ChildGridId, 10, 64)
	if err != nil {
		return nil // qualified, an exit well or link: no local subtree
	}
	inside, err := gridInSubtree(ctx, tx, destGridID, childGrid)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%w: cannot place a well inside its own subtree", ErrInvalidArgument)
	}
	return nil
}
