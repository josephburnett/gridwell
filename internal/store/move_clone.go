package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// gridSourceKinds returns the source_kind values for two grids. Empty string
// means a regular Gridwell-owned grid.
func (s *Store) gridSourceKinds(ctx context.Context, tx *sql.Tx, a, b int64) (string, string, error) {
	ka, err := s.gridSourceKind(ctx, tx, a)
	if err != nil {
		return "", "", err
	}
	kb, err := s.gridSourceKind(ctx, tx, b)
	if err != nil {
		return "", "", err
	}
	return ka, kb, nil
}

// gridSourceKind returns one grid's source_kind ("" for a regular grid).
func (s *Store) gridSourceKind(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var k sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT source_kind FROM grids WHERE id = ?`, id).Scan(&k)
	if err != nil {
		return "", err
	}
	if k.Valid {
		return k.String, nil
	}
	return "", nil
}

// MoveTile moves a tile either within its grid or across grids.
func (s *Store) MoveTile(ctx context.Context, req *rpc.MoveTileRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		if n.GridID != srcGrid {
			return fmt.Errorf("%w: tile %d not in source path leaf grid %d", ErrInvalidPath, req.TileID, srcGrid)
		}

		// No fork: copy-on-clone keeps tiles unshared, so a move writes the
		// tile's row in place (its id never changes).
		tileID := req.TileID

		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if err := checkLeafGrid(dstSeq, req.DestGridID); err != nil {
			return err
		}
		dstGrid := dstSeq.grids[len(dstSeq.grids)-1]

		crossGrid := dstGrid != srcGrid
		// Cross-grid moves that touch a source-backed grid are not
		// permitted: a file can't be moved into Gridwell ("things stay
		// where you put them" only inside Gridwell), and host-side mv
		// across directories is not yet implemented. The user can clone
		// (right-drag) instead to drop a linked tile.
		if crossGrid {
			srcSource, dstSource, err := s.gridSourceKinds(ctx, tx, srcGrid, dstGrid)
			if err != nil {
				return err
			}
			if srcSource != "" || dstSource != "" {
				return fmt.Errorf("%w: cross-grid move involving a source-backed grid is not allowed; clone (right-drag) to link instead", ErrInvalidArgument)
			}
		}
		if n.Kind == rpc.KindWell {
			if slices.Contains(req.DestPath.WellIDs, tileID) {
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
			*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: srcGrid, TileID: tileID}})
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// CloneTile duplicates a tile into a destination grid at (x, y) as an eager,
// independent copy. The new row carries the source's object_id + version as a
// provenance marker but gets a fresh row id. An interior well's whole child
// subtree is deep-copied (new grid + tile rows; blobs shared); a file/process
// well shares its host-backed source grid (identity, not COW); a text/url/shell
// tile shares its content/preview blob (refcount bumped). Nothing is shared
// between the two copies, so editing one can never touch the other.
func (s *Store) CloneTile(ctx context.Context, req *rpc.CloneTileRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if n.GridID != srcSeq.grids[len(srcSeq.grids)-1] {
			return fmt.Errorf("%w: tile %d not in source path leaf grid", ErrInvalidPath, req.TileID)
		}
		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if err := checkLeafGrid(dstSeq, req.DestGridID); err != nil {
			return err
		}

		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		dstGridRaw := dstSeq.grids[len(dstSeq.grids)-1]
		if srcGrid != dstGridRaw {
			srcSource, dstSource, err := s.gridSourceKinds(ctx, tx, srcGrid, dstGridRaw)
			if err != nil {
				return err
			}
			if dstSource != "" {
				return fmt.Errorf("%w: cannot clone into a source-backed grid; its contents come from the host", ErrInvalidArgument)
			}
			// From a source grid, both well kinds (file-well / process-well)
			// and text tiles can be linked out. Wells carry their FS path /
			// PID; text tiles (the synthesized file / @info rows) carry
			// source_key so the client renders the basename / "info" label and
			// the red exit border — the clone reads as a reference to
			// something outside Gridwell.
			if srcSource != "" && !isWellKind(n.Kind) && n.Kind != rpc.KindText {
				return fmt.Errorf("%w: only wells and file tiles can be linked out of a source-backed grid", ErrInvalidArgument)
			}
		}

		dstGrid := dstGridRaw

		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		// child_grid_id: an interior well gets a deep copy of its subtree
		// (cloneSubtree); a file/process well shares the same host-backed source
		// grid (identity, not COW); everything else has none.
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
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindText {
			return ErrNotTextTile
		}
		// Source-backed text tiles (fs file metadata, the proc @info
		// tile) are read-only views of host state: their content is
		// produced by the reconciler, not the user. Rejecting writes
		// here means even a misbehaving client can't poison the blob.
		// (Checked before checkPathLeaf so the read-only signal wins over
		// a stale path — order matters to callers and is asserted in tests.)
		if n.SourceKey != "" {
			return fmt.Errorf("%w: source-backed text tiles are read-only", ErrInvalidArgument)
		}
		if _, err := s.checkPathLeaf(ctx, tx, req.Path, n); err != nil {
			return err
		}

		if _, _, err := s.swapTileBlob(ctx, tx, req.TileID, "blob_id", req.Data, mediaMarkdown); err != nil {
			return err
		}
		// alt_text is a deterministic function of the content; write it
		// alongside (a separate statement from the blob kernel).
		alt := markdown.AltFromSource(string(req.Data))
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
			alt, s.now().Unix(), req.TileID); err != nil {
			return err
		}
		out, err = s.finishContentEdit(ctx, tx, req.TileID, events)
		return err
	})
	return out, err
}
