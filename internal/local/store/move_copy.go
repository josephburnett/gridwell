package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
)

// CopyTile duplicates a tile into a destination grid as an eager, independent
// copy: a fresh row id, the source's version, and the source row untouched, so
// a copy is layout, not content. What each tile copies as is rpc.CopyOf's
// (insertTileCopy); a leaf shares its blob with the refcount bumped. Nothing
// else is shared, so editing one can never touch the other.
func (s *Store) CopyTile(ctx context.Context, req *gridwellv1.CopyTileRequest) (*gridwellv1.Tile, error) {
	tileID, err := parseID(req.TileId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "CopyTile", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		dstGrid, err := s.destGrid(ctx, tx, req.DestGridId)
		if err != nil {
			return err
		}

		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		newID, err := s.insertTileCopy(ctx, tx, dstGrid, n, req.X, req.Y, s.now().Unix())
		if err != nil {
			return err
		}
		if err := s.bumpGridVersion(ctx, tx, events, dstGrid); err != nil {
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
	return s.claimContentVersion(ctx, "WriteContent/text", tileID, version, func(tx *sql.Tx, n *gridwellv1.Tile, events *[]*gridwellv1.Event) (*gridwellv1.Tile, error) {
		if rpc.KindOf(n.Kind).Content != rpc.ContentBody {
			return nil, ErrNotTextTile
		}
		_, changed, err := s.swapTileBlob(ctx, tx, tileID, "blob_id", data, mediaMarkdown)
		if err != nil {
			return nil, err
		}
		if !changed {
			// A no-op write never mutates: re-saving unchanged bytes must not
			// bump the version or fan a TileChanged, and alt_text, a pure
			// function of the content, is unchanged too.
			return s.loadTile(ctx, tx, tileID)
		}
		// alt_text is a deterministic function of the content.
		alt := doctype.AltFromSource(string(data))
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
			alt, s.now().Unix(), tileID); err != nil {
			return nil, err
		}
		return s.finishContentEdit(ctx, tx, tileID, events)
	})
}
