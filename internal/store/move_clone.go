package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"

	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// MoveTile moves a tile either within its grid or across grids.
func (s *Store) MoveTile(ctx context.Context, req *rpc.MoveTileRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	destGridID, err := parseID(req.DestGridID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dest_grid_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, tileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		if n.GridID != strconv.FormatInt(srcGrid, 10) {
			return fmt.Errorf("%w: tile %d not in source path leaf grid %d", ErrInvalidPath, tileID, srcGrid)
		}

		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if err := checkLeafGrid(dstSeq, destGridID); err != nil {
			return err
		}
		dstGrid := dstSeq.grids[len(dstSeq.grids)-1]

		crossGrid := dstGrid != srcGrid
		if n.Kind == rpc.KindWell {
			if slices.Contains(req.DestPath.WellIDs, req.TileID) {
				return fmt.Errorf("%w: cannot move a well into itself or a descendant", ErrInvalidArgument)
			}
		}

		var excludes []int64
		if dstGrid == srcGrid {
			excludes = []int64{tileID}
		}
		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H, excludes...)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ?, x = ?, y = ?, updated_at = ? WHERE id = ?`,
			dstGrid, req.X, req.Y, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		if crossGrid {
			if err := s.bumpGridVersion(ctx, tx, srcGrid); err != nil {
				return err
			}
			if err := s.bumpGridVersion(ctx, tx, dstGrid); err != nil {
				return err
			}
		}
		if crossGrid {
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

// CloneTile duplicates a tile into a destination grid at (x, y) as an eager,
// independent copy. The new row carries the source's object_id + version as a
// provenance marker but gets a fresh row id. An interior well's whole child
// subtree is deep-copied (new grid + tile rows; blobs shared); an exit well
// keeps its qualified cross-plugin child_grid_id (the child grid is owned by
// another plugin, not duplicated); a text/url/shell tile shares its
// content/preview blob (refcount bumped). Nothing is shared between the two
// copies, so editing one can never touch the other.
func (s *Store) CloneTile(ctx context.Context, req *rpc.CloneTileRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	destGridID, err := parseID(req.DestGridID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dest_grid_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, tileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if n.GridID != strconv.FormatInt(srcSeq.grids[len(srcSeq.grids)-1], 10) {
			return fmt.Errorf("%w: tile %d not in source path leaf grid", ErrInvalidPath, tileID)
		}
		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if err := checkLeafGrid(dstSeq, destGridID); err != nil {
			return err
		}

		dstGrid := dstSeq.grids[len(dstSeq.grids)-1]

		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		// child_grid_id: an interior well gets a deep copy of its subtree
		// (cloneSubtree); an exit well keeps its qualified cross-plugin child
		// reference (the child grid lives in another plugin); everything else
		// has none.
		child, err := s.childGridForClone(ctx, tx, n)
		if err != nil {
			return err
		}
		newID, err := s.insertTileCopy(ctx, tx, dstGrid, n, req.X, req.Y, child, now)
		if err != nil {
			return err
		}
		if err := s.bumpGridVersion(ctx, tx, dstGrid); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, newID, events)
		return err
	})
	return out, err
}

// UpdateText replaces a text tile's blob with new bytes.
func (s *Store) UpdateText(ctx context.Context, req *rpc.UpdateTextRequest) (*rpc.Tile, error) {
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, tileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindText {
			return ErrNotTextTile
		}
		if _, err := s.checkPathLeaf(ctx, tx, req.Path, n); err != nil {
			return err
		}

		if _, _, err := s.swapTileBlob(ctx, tx, tileID, "blob_id", req.Data, mediaMarkdown); err != nil {
			return err
		}
		// alt_text is a deterministic function of the content; write it
		// alongside (a separate statement from the blob kernel).
		alt := markdown.AltFromSource(string(req.Data))
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
			alt, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	})
	return out, err
}
