package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/doctype"
	"github.com/josephburnett/gridwell/internal/rpc"
)

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

		// destination is id-addressed; refuse a grid that doesn't exist.
		if _, err := s.loadGrid(ctx, tx, destGridID); err != nil {
			return fmt.Errorf("%w: destination grid %d: %v", ErrInvalidArgument, destGridID, err)
		}
		dstGrid := destGridID

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

// writeTextContent replaces a text tile's blob with new bytes — the text arm
// of WriteContent (the one content write; text is a content edit and bumps).
func (s *Store) writeTextContent(ctx context.Context, tileIDStr string, version int64, data []byte) (*rpc.Tile, error) {
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, tileID, version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindText {
			return ErrNotTextTile
		}

		_, changed, err := s.swapTileBlob(ctx, tx, tileID, "blob_id", data, mediaMarkdown)
		if err != nil {
			return err
		}
		if !changed {
			// Byte-identical content (same content-addressed blob). Re-saving
			// unchanged bytes must NOT bump the version or fan a TileChanged —
			// reading and no-op writes never mutate (the primary rule). alt_text
			// is a pure function of the content, so it is unchanged too. A
			// debounced auto-save that fires on a tile the user didn't actually
			// edit is thereby a true no-op.
			out, err = s.loadTile(ctx, tx, tileID)
			return err
		}
		// alt_text is a deterministic function of the content; write it
		// alongside (a separate statement from the blob kernel).
		alt := doctype.AltFromSource(string(data))
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
