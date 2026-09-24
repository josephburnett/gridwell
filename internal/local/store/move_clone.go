package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
)

// CloneTile duplicates a tile into a destination grid as an eager, independent
// copy: a fresh row id, the source's version, and the source row untouched, so
// a clone is layout, not content. An interior well's whole subtree is
// deep-copied; an exit well keeps its qualified child_grid_id, another plugin
// owning that grid; a leaf shares its blob with the refcount bumped. Nothing
// else is shared, so editing one can never touch the other.
func (s *Store) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.Tile, error) {
	tileID, err := parseID(req.TileId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	destGridID, err := parseID(req.DestGridId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dest_grid_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "CloneTile", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}

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

// writeTextContent replaces a text tile's blob with new bytes: the text arm of
// WriteContent. Text is a content edit, so it bumps.
func (s *Store) writeTextContent(ctx context.Context, tileIDStr string, version int64, data []byte) (*gridwellv1.Tile, error) {
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "WriteContent/text", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.claimContentVersion(ctx, tx, tileID, version)
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
			// A no-op write never mutates: re-saving unchanged bytes must not
			// bump the version or fan a TileChanged, and alt_text, a pure
			// function of the content, is unchanged too.
			out, err = s.loadTile(ctx, tx, tileID)
			return err
		}
		// alt_text is a deterministic function of the content.
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
