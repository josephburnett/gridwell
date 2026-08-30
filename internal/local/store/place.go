package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/api/rpc"
)

// PlaceTile is the single placement writeback (2026-07-26,
// interface-redesign-plan.md decision 7): placement is one fact —
// (grid_id, x, y, w, h) — and this verb owns all of it. A move is a grid
// change, a resize a footprint change, and both at once are one write.
// Id-addressed; there is no descent path.
//
// Placement is LAYOUT, not content: no version claim, no version bump
// (docs/simplify-plan.md S5). A drag is an explicit act on a tile the user
// can see, so when two clients race, "whoever moved it last moved it" is the
// physical-world answer and the tile event reconciles it. The one thing a
// race could actually corrupt — two tiles in one cell — is refused by the
// overlap check below, inside this same transaction, claim or no claim.
//
// Moving a well into its own subtree is refused by walking ANCESTORS of the
// destination grid (wellWouldContainItself) — a fact the server derives
// itself, where the old MoveTile validated a client-supplied copy of it
// (the DestPath membership check).
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
// destination == the well's child grid, or any grid beneath it. The check
// walks UP from the destination grid through parent wells — each interior
// child grid hangs off exactly one well by construction (wells are created
// with fresh grids, clones deep-copy, placement carries the well row whole),
// so the ancestor chain is a server-derived fact and needs no client path.
// Non-well tiles and exit wells (qualified child_grid_id — the subtree is
// another plugin's) have no local subtree and pass trivially.
func (s *Store) wellWouldContainItself(ctx context.Context, tx *sql.Tx, n *rpc.Tile, destGridID int64) error {
	if !isWellKind(n.Kind) {
		return nil
	}
	childGrid, err := strconv.ParseInt(n.ChildGridID, 10, 64)
	if err != nil {
		return nil // qualified (exit well / link): no local subtree
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
			return nil // reached a root (or scratch): destination is outside the subtree
		}
		if err != nil {
			return err
		}
		g = parent
	}
}
