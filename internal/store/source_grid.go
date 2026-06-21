package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/source"
)

// autoGridWidth is the number of columns the auto-layout wraps at. New
// entries are placed row-major into the next free cell.
const autoGridWidth = 8

// reconcileSourceGrid brings the tile rows in a source-backed grid up to
// date with the plugin that owns it. Called from GetGrid before tiles are
// returned. No-op for regular Gridwell grids or unrecognised source kinds.
func (s *Store) reconcileSourceGrid(ctx context.Context, g *rpc.Grid) error {
	src, ok := s.sources.Get(g.SourceKind)
	if !ok {
		return nil
	}
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		listing, err := src.List(ctx, g.SourceID)
		if err != nil {
			return nil // source unavailable; keep existing tiles unchanged
		}

		existing, err := loadSourceGridTiles(ctx, tx, g.ID)
		if err != nil {
			return err
		}

		existingSlice := make([]source.ExistingTile, 0, len(existing))
		for key, tile := range existing {
			existingSlice = append(existingSlice, source.ExistingTile{Key: key, Label: tile.AltText})
		}

		probe := func(key string) source.Presence {
			p, _ := src.Probe(ctx, g.SourceID, key)
			return p
		}
		plan := source.Reconcile(existingSlice, listing, probe)

		now := s.now().Unix()
		layout := newLayoutTracker(existing)
		changed := false

		// Apply insertions.
		for _, n := range plan.Insert {
			switch n.Kind {
			case source.KindWell:
				if err := s.insertSourceWellTile(ctx, tx, g, n, layout.next(), now, events); err != nil {
					return err
				}
			case source.KindText:
				if err := s.insertSourceTextTile(ctx, tx, g.ID, src, g.SourceID, n, layout.next(), now, events); err != nil {
					return err
				}
			// KindURL / KindShell: not yet wired into the reconciler.
			}
			changed = true
		}

		// Apply label refreshes.
		for _, r := range plan.Relabel {
			tile := existing[r.Key]
			if err := s.updateTileAltText(ctx, tx, tile.ID, r.Label, now, events); err != nil {
				return err
			}
			changed = true
		}

		// One pass over stable listing nodes: handle kind mismatches and
		// refresh body blobs whose content has changed since last reconcile.
		inInsert := make(map[string]bool, len(plan.Insert))
		for _, n := range plan.Insert {
			inInsert[n.Key] = true
		}
		for _, n := range listing.Nodes {
			if inInsert[n.Key] {
				continue // freshly inserted, nothing to refresh
			}
			tile, ok := existing[n.Key]
			if !ok {
				continue
			}

			// Kind mismatch (e.g., a file replaced by a directory of the same
			// name): delete the old tile and re-insert at the same position.
			if wantKind := sourceNodeKind(g.SourceKind, n.Kind); wantKind != "" && tile.Kind != wantKind {
				if err := s.deleteFSGridTile(ctx, tx, tile, events); err != nil {
					return err
				}
				pos := position{tile.X, tile.Y}
				switch n.Kind {
				case source.KindWell:
					err = s.insertSourceWellTile(ctx, tx, g, n, pos, now, events)
				case source.KindText:
					err = s.insertSourceTextTile(ctx, tx, g.ID, src, g.SourceID, n, pos, now, events)
				}
				if err != nil {
					return err
				}
				changed = true
				continue
			}

			// Body blob refresh: content-addressed, so this is a no-op when
			// nothing changed (tileBlobHashTx avoids a ReadBlob call when the
			// hash already matches).
			if n.Body == nil || n.Body.Hash == "" {
				continue
			}
			if tileBlobHashTx(ctx, tx, tile.BlobID) == n.Body.Hash {
				continue
			}
			body, err := src.ReadBlob(ctx, g.SourceID, n.Body.BlobRef)
			if err != nil {
				continue // can't refresh; keep old blob
			}
			_, bodyChanged, err := s.swapTileBlob(ctx, tx, tile.ID, "blob_id", body, n.Body.MediaType)
			if err != nil {
				return err
			}
			if bodyChanged {
				if err := bumpTileVersion(ctx, tx, tile.ID); err != nil {
					return err
				}
				t, err := s.loadTile(ctx, tx, tile.ID)
				if err != nil {
					return err
				}
				*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
				changed = true
			}
		}

		// Apply deletions.
		for _, key := range plan.Delete {
			tile := existing[key]
			if err := s.deleteFSGridTile(ctx, tx, tile, events); err != nil {
				return err
			}
			changed = true
		}

		if !changed {
			return nil
		}
		return s.bumpGridVersion(ctx, tx, g.ID)
	})
}

// insertSourceWellTile inserts a new well tile for a projected child node.
// The rpc kind and extra columns (fs_path / pid) are determined by the
// parent grid's source kind.
func (s *Store) insertSourceWellTile(ctx context.Context, tx *sql.Tx, g *rpc.Grid, n source.Node, pos position, now int64, events *[]rpc.Event) error {
	childGridID, err := s.getOrCreateSourceGrid(ctx, tx, g.SourceKind, n.Child, now)
	if err != nil {
		return err
	}
	objID := s.newID()
	var res sql.Result
	switch g.SourceKind {
	case rpc.GridSourceFS:
		res, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(g.ID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id, fs_path, source_key,
				alt_text, created_at, updated_at)
			VALUES (?, ?, 'file-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?, ?)`,
			objID, g.ID, pos.x, pos.y, childGridID, n.Child, n.Key, n.Label, now, now)
	case rpc.GridSourceProc:
		pid, perr := strconv.ParseInt(n.Key, 10, 64)
		if perr != nil {
			return fmt.Errorf("bad proc key %q: %w", n.Key, perr)
		}
		res, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(g.ID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id, pid, source_key,
				alt_text, created_at, updated_at)
			VALUES (?, ?, 'process-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?, ?)`,
			objID, g.ID, pos.x, pos.y, childGridID, pid, n.Key, n.Label, now, now)
	default:
		res, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(g.ID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id, source_key,
				alt_text, created_at, updated_at)
			VALUES (?, ?, 'well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?)`,
			objID, g.ID, pos.x, pos.y, childGridID, n.Key, n.Label, now, now)
	}
	if err != nil {
		return fmt.Errorf("insert source well tile: %w", err)
	}
	return s.emitInsertedTile(ctx, tx, res, events)
}

// insertSourceTextTile inserts a new text tile for a projected leaf node.
// If the node carries a Body ref the blob bytes are fetched and stored;
// otherwise blob_id is left NULL (lazy).
func (s *Store) insertSourceTextTile(ctx context.Context, tx *sql.Tx, gridID int64, src source.Source, sourceID string, n source.Node, pos position, now int64, events *[]rpc.Event) error {
	var blobID int64
	if n.Body != nil {
		if body, err := src.ReadBlob(ctx, sourceID, n.Body.BlobRef); err == nil {
			hash := hashBytes(body)
			id, err := s.putBlob(ctx, tx, schemaOf(gridID), hash, body, n.Body.MediaType)
			if err != nil {
				return err
			}
			blobID = id
		}
	}
	objID := s.newID()
	var (
		res sql.Result
		err error
	)
	if blobID != 0 {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				blob_id, source_key, alt_text, created_at, updated_at)
			VALUES (?, ?, 'text', ?, ?, 1, 1, ?, ?, ?, ?, ?)`,
			objID, gridID, pos.x, pos.y, blobID, n.Key, n.Label, now, now)
	} else {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				source_key, alt_text, created_at, updated_at)
			VALUES (?, ?, 'text', ?, ?, 1, 1, ?, ?, ?, ?)`,
			objID, gridID, pos.x, pos.y, n.Key, n.Label, now, now)
	}
	if err != nil {
		return fmt.Errorf("insert source text tile: %w", err)
	}
	if blobID != 0 {
		if err := s.incBlobRefcount(ctx, tx, blobID); err != nil {
			return err
		}
	}
	return s.emitInsertedTile(ctx, tx, res, events)
}

// sourceNodeKind maps a source Kind to the rpc tile kind stored in the DB,
// taking the parent grid's source kind into account for well tiles.
// Returns "" for kinds the reconciler does not yet handle.
func sourceNodeKind(sourceKind string, kind source.Kind) string {
	switch kind {
	case source.KindWell:
		switch sourceKind {
		case rpc.GridSourceFS:
			return rpc.KindFileWell
		case rpc.GridSourceProc:
			return rpc.KindProcessWell
		default:
			return rpc.KindWell
		}
	case source.KindText:
		return rpc.KindText
	case source.KindURL:
		return rpc.KindURL
	case source.KindShell:
		return rpc.KindShell
	}
	return ""
}

// tileBlobHashTx returns the stored hash for a blob (empty string when
// blobID is 0 or not found). Avoids a ReadBlob call when the content
// hash already matches what's in the DB.
func tileBlobHashTx(ctx context.Context, tx *sql.Tx, blobID int64) string {
	if blobID == 0 {
		return ""
	}
	var hash string
	_ = tx.QueryRowContext(ctx, `SELECT hash FROM `+schemaOf(blobID)+`blobs WHERE id = ?`, blobID).Scan(&hash)
	return hash
}

// loadSourceGridTiles returns the tiles in a source grid keyed by
// source_key. Tiles without a source_key are skipped (they are not
// reconciler-managed).
func loadSourceGridTiles(ctx context.Context, q gridReader, gridID int64) (map[string]*rpc.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM `+schemaOf(gridID)+`tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*rpc.Tile{}
	for rows.Next() {
		t, err := scanTile(rows)
		if err != nil {
			return nil, err
		}
		if t.SourceKey == "" {
			continue
		}
		out[t.SourceKey] = t
	}
	return out, rows.Err()
}

// position is one occupied or candidate auto-grid cell.
type position struct{ x, y int64 }

// layoutTracker walks the auto-grid in row-major order, skipping cells
// that the existing tiles already occupy. next() returns the next free
// position; new tiles are inserted there.
type layoutTracker struct {
	occupied map[position]bool
	cursor   position
}

func newLayoutTracker(existing map[string]*rpc.Tile) *layoutTracker {
	occ := make(map[position]bool, len(existing))
	for _, t := range existing {
		occ[position{t.X, t.Y}] = true
	}
	return &layoutTracker{occupied: occ}
}

func (l *layoutTracker) next() position {
	for {
		p := l.cursor
		l.advance()
		if !l.occupied[p] {
			l.occupied[p] = true
			return p
		}
	}
}

func (l *layoutTracker) advance() {
	l.cursor.x++
	if l.cursor.x >= autoGridWidth {
		l.cursor.x = 0
		l.cursor.y++
	}
}

// emitInsertedTile resolves the row id from a just-executed INSERT, loads
// the tile, and appends a TileChanged event.
func (s *Store) emitInsertedTile(ctx context.Context, tx *sql.Tx, res sql.Result, events *[]rpc.Event) error {
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.emitTileChanged(ctx, tx, id, events)
	return err
}

// updateTileAltText overwrites the alt_text on one tile and emits a
// TileChanged event.
func (s *Store) updateTileAltText(ctx context.Context, tx *sql.Tx, tileID int64, altText string, now int64, events *[]rpc.Event) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+schemaOf(tileID)+`tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
		altText, now, tileID); err != nil {
		return fmt.Errorf("update tile alt_text: %w", err)
	}
	t, err := s.loadTile(ctx, tx, tileID)
	if err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
	return nil
}

// deleteFSGridTile drops a source-backed tile that no longer has a backing
// entry, releasing any child-grid or blob reference it held.
func (s *Store) deleteFSGridTile(ctx context.Context, tx *sql.Tx, t *rpc.Tile, events *[]rpc.Event) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+schemaOf(t.ID)+`tiles WHERE id = ?`, t.ID); err != nil {
		return err
	}
	if err := s.decTileRefs(ctx, tx, t.Kind, t.ChildGridID, t.BlobID, t.PreviewBlobID); err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
	return nil
}
