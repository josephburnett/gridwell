package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// placementSet is the footprint a placement write touches, here and in
// Namespace.Place. The grid rides beside it on home's side alone.
const placementSet = `x = ?, y = ?, w = ?, h = ?`

// MoveTile is the single placement writeback: placement is one fact,
// (grid_id, x, y, w, h), and this verb owns all of it, id-addressed with no
// descent path. It claims the placement the writer saw (ClaimPlacement) and
// bumps no version, so a move never conflicts with typing. Two tiles in one
// cell is refused by the overlap check in this same transaction. Moving a door
// into its own subtree is refused by doorWouldContainItself, walking a chain
// the server derives itself.
func (s *Store) MoveTile(ctx context.Context, req *gridwellv1.MoveTileRequest) (*gridwellv1.Tile, error) {
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
	err = s.withTileWrite(ctx, "MoveTile", tileID, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		srcGrid, err := parseID(n.GridId)
		if err != nil {
			return fmt.Errorf("tile %d: bad grid_id %q: %w", tileID, n.GridId, err)
		}
		if srcGrid == destGridID && n.X == req.X && n.Y == req.Y && n.W == req.W && n.H == req.H {
			// Already where the write puts it: nothing to overwrite, so no
			// claim to lose.
			out = n
			return nil
		}
		if err := ClaimPlacement(n, req.Claim, true); err != nil {
			return err
		}
		if _, err := s.loadGrid(ctx, tx, destGridID); err != nil {
			return fmt.Errorf("%w: destination grid %d: %v", ErrInvalidArgument, destGridID, err)
		}
		if err := s.doorWouldContainItself(ctx, tx, n, destGridID); err != nil {
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
			if err := s.tileLeft(ctx, tx, events, srcGrid, tileID); err != nil {
				return err
			}
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// ClaimPlacement verifies a placement claim against row n: the placement the
// writer saw must be the one the row holds. It is the state, not its history,
// so a tile moved away and back is where the writer saw it and the write
// lands. A nil claim is the node's own write. withGrid is false for a plugin's
// row, whose grid its listing fixes.
func ClaimPlacement(n *gridwellv1.Tile, c *gridwellv1.Placement, withGrid bool) error {
	if c == nil {
		return nil
	}
	gridMoved := false
	if withGrid {
		have, herr := parseID(n.GetGridId())
		saw, serr := parseID(c.GridId)
		gridMoved = herr != nil || serr != nil || have != saw
	}
	if gridMoved || n.X != c.X || n.Y != c.Y || n.W != c.W || n.H != c.H {
		return fmt.Errorf("%w: tile %s is no longer where the writer saw it", ErrVersionConflict, n.Id)
	}
	return nil
}

// doorWouldContainItself refuses moving a door into its own subtree, walking
// up from the destination grid through parent doors (gridInSubtree). Each
// interior child grid hangs off exactly one door by construction, so the
// ancestor chain is a server-derived fact and needs no client path. Non-door
// tiles and doors to other namespaces have no local subtree and pass trivially.
func (s *Store) doorWouldContainItself(ctx context.Context, tx *sql.Tx, n *gridwellv1.Tile, destGridID int64) error {
	if !rpc.IsDoorKind(n.Kind) {
		return nil
	}
	childGrid, err := strconv.ParseInt(n.ChildGridId, 10, 64)
	if err != nil {
		return nil // qualified, a door to another namespace or link: no local subtree
	}
	inside, err := gridInSubtree(ctx, tx, destGridID, childGrid)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%w: cannot move a door into its own subtree", ErrInvalidArgument)
	}
	return nil
}
