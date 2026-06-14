package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// checkLeafGrid returns an error if the path's leaf grid doesn't match wantGridID.
func checkLeafGrid(seq gridSequence, wantGridID int64) error {
	got := seq.grids[len(seq.grids)-1]
	if got != wantGridID {
		return fmt.Errorf("%w: path leaf grid is %d not %d", ErrInvalidPath, got, wantGridID)
	}
	return nil
}

// gridSequence is the sequence of grid ids from the root down to the leaf
// grid the editing pane is in. grids[0] is always the current root grid;
// grids[len-1] is the leaf. wells[i] is the well in grids[i] that points at
// grids[i+1], so len(wells) == len(grids)-1.
type gridSequence struct {
	grids []int64
	wells []int64
}

// buildGridSequence validates the path and returns the sequence of grids and
// path wells for it.
func (s *Store) buildGridSequence(ctx context.Context, q gridReader, p rpc.Path) (gridSequence, error) {
	root, err := rootGridID(ctx, q)
	if err != nil {
		return gridSequence{}, err
	}
	seq := gridSequence{grids: []int64{root}}
	for _, wellID := range p.WellIDs {
		w, err := s.loadTile(ctx, q, wellID)
		if err != nil {
			return gridSequence{}, fmt.Errorf("%w: well %d: %v", ErrInvalidPath, wellID, err)
		}
		if !isWellKind(w.Kind) {
			return gridSequence{}, fmt.Errorf("%w: tile %d is not a well", ErrInvalidPath, wellID)
		}
		if w.GridID != seq.grids[len(seq.grids)-1] {
			return gridSequence{}, fmt.Errorf("%w: well %d not in grid %d", ErrInvalidPath, wellID, seq.grids[len(seq.grids)-1])
		}
		seq.wells = append(seq.wells, wellID)
		seq.grids = append(seq.grids, w.ChildGridID)
	}
	return seq, nil
}

// isWellKind reports whether a tile kind has a child grid that can be
// descended into — the three "well" kinds (interior well, file-well,
// process-well). Thin alias for rpc.IsWellKind so the three-kind set lives
// in one place; kept as a package-local name for the many store callsites.
func isWellKind(kind string) bool {
	return rpc.IsWellKind(kind)
}

// preWriteResult describes the result of preWrite.
type preWriteResult struct {
	GridID       int64
	TargetTileID int64
	Events       []rpc.Event
}

// preWrite ensures that the leaf grid in the descent path is unshared,
// forking grids up the path as needed.
func (s *Store) preWrite(ctx context.Context, tx *sql.Tx, path rpc.Path, targetTileID int64) (*preWriteResult, error) {
	seq, err := s.buildGridSequence(ctx, tx, path)
	if err != nil {
		return nil, err
	}

	// Find the TOPMOST grid in the path whose refcount > 1. Every grid from
	// that index down to the leaf must be forked, because forking a shared
	// ancestor bumps the refcount of every well-child it contains — so any
	// rc=1 descendant becomes rc=2 as soon as its parent forks, and a
	// subsequent write through this path would leak into the other clones
	// of the ancestor.
	//
	// fs/proc-backed grids are excluded: they are shared by identity (path
	// or PID), not by COW. The host filesystem / process table is the
	// single source of truth, so two file-wells at /foo must see the same
	// state by design — forking would invent a divergence that the world
	// outside Gridwell can't honor.
	topForkIdx := -1
	for i := 0; i < len(seq.grids); i++ {
		var (
			rc         int64
			sourceKind sql.NullString
		)
		err := tx.QueryRowContext(ctx, `SELECT refcount, source_kind FROM `+schemaOf(seq.grids[i])+`grids WHERE id = ?`, seq.grids[i]).Scan(&rc, &sourceKind)
		if err != nil {
			return nil, err
		}
		if sourceKind.Valid {
			continue
		}
		if rc > 1 {
			if i == 0 {
				return nil, fmt.Errorf("internal: root grid is shared (refcount=%d)", rc)
			}
			topForkIdx = i
			break
		}
	}

	if topForkIdx == -1 {
		return &preWriteResult{GridID: seq.grids[len(seq.grids)-1], TargetTileID: targetTileID}, nil
	}

	wellObjects := make([]string, len(seq.wells))
	for i, wid := range seq.wells {
		var obj string
		if err := tx.QueryRowContext(ctx, `SELECT object_id FROM `+schemaOf(wid)+`tiles WHERE id = ?`, wid).Scan(&obj); err != nil {
			return nil, err
		}
		wellObjects[i] = obj
	}
	var targetObjectID string
	if targetTileID != 0 {
		err := tx.QueryRowContext(ctx, `SELECT object_id FROM `+schemaOf(targetTileID)+`tiles WHERE id = ?`, targetTileID).Scan(&targetObjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: target tile %d", ErrNotFound, targetTileID)
		}
		if err != nil {
			return nil, err
		}
	}

	events := []rpc.Event{}

	parentWellID := int64(0)
	if topForkIdx > 0 {
		parentWellID = seq.wells[topForkIdx-1]
	}

	for i := topForkIdx; i < len(seq.grids); i++ {
		oldGridID := seq.grids[i]
		// Don't fork fs/proc grids — they're shared by identity. The
		// well above (already remapped into parentWellID) keeps pointing
		// at the same shared grid, which is fine. Stop walking: any
		// further descent stays on the shared grid.
		var sourceKind sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT source_kind FROM `+schemaOf(oldGridID)+`grids WHERE id = ?`, oldGridID).Scan(&sourceKind); err != nil {
			return nil, err
		}
		if sourceKind.Valid {
			break
		}
		newGridID, wellRemap, err := s.forkGrid(ctx, tx, oldGridID)
		if err != nil {
			return nil, fmt.Errorf("fork grid %d: %w", oldGridID, err)
		}

		if parentWellID != 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+schemaOf(parentWellID)+`tiles SET child_grid_id = ?, updated_at = ? WHERE id = ?`,
				newGridID, s.now().Unix(), parentWellID); err != nil {
				return nil, err
			}
			if err := s.decRefcount(ctx, tx, oldGridID); err != nil {
				return nil, err
			}
			if err := s.incRefcount(ctx, tx, newGridID); err != nil {
				return nil, err
			}
			events = append(events, rpc.Event{
				Kind:       rpc.EventGridForked,
				GridForked: &rpc.GridForked{WellID: parentWellID, OldGridID: oldGridID, NewGridID: newGridID},
			})
		}

		if i < len(seq.wells) {
			oldWell := seq.wells[i]
			newWell, ok := wellRemap[oldWell]
			if !ok {
				return nil, fmt.Errorf("internal: well %d (object %s) not remapped", oldWell, wellObjects[i])
			}
			parentWellID = newWell
		}

		seq.grids[i] = newGridID
	}

	newTargetID := targetTileID
	if targetTileID != 0 {
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM `+schemaOf(seq.grids[len(seq.grids)-1])+`tiles WHERE grid_id = ? AND object_id = ?`,
			seq.grids[len(seq.grids)-1], targetObjectID).Scan(&newTargetID)
		if err != nil {
			return nil, fmt.Errorf("relocate target after fork: %w", err)
		}
	}

	return &preWriteResult{
		GridID:       seq.grids[len(seq.grids)-1],
		TargetTileID: newTargetID,
		Events:       events,
	}, nil
}

// forkGrid creates a new grid that is a copy of oldGridID.
func (s *Store) forkGrid(ctx context.Context, tx *sql.Tx, oldGridID int64) (int64, map[int64]int64, error) {
	old, err := s.loadGrid(ctx, tx, oldGridID)
	if err != nil {
		return 0, nil, err
	}

	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (object_id, version, refcount, created_at, updated_at)
		 VALUES (?, ?, 0, ?, ?)`,
		old.ObjectID, old.Version, now, now)
	if err != nil {
		return 0, nil, fmt.Errorf("insert grid: %w", err)
	}
	newGridID, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}

	// tileColumns + ", created_at, updated_at" gives us all the columns we need.
	// grid_id (from tileColumns) is discarded — we assign newGridID on insert.
	rows, err := tx.QueryContext(ctx,
		`SELECT `+tileColumns+`, created_at, updated_at FROM tiles WHERE grid_id = ?`, oldGridID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	type tileCopy struct {
		oldID                int64
		objectID             string
		version              int64
		oldGridID            int64 // discarded; we insert into newGridID
		kind                 string
		x, y, w, h           int64
		viewX, viewY         int64
		viewZoom             float64
		childGrid            sql.NullInt64
		textX, textY         int64
		textW, textH         int64
		textMode             sql.NullString
		blob                 sql.NullInt64
		urlString            sql.NullString
		previewBlob          sql.NullInt64
		fsPath               sql.NullString
		pid                  sql.NullInt64
		sourceKey            sql.NullString
		altText              string
		createdAt, updatedAt int64
	}
	var copies []tileCopy
	for rows.Next() {
		var nc tileCopy
		// Column order matches tileColumns + created_at, updated_at.
		if err := rows.Scan(&nc.oldID, &nc.objectID, &nc.version, &nc.oldGridID, &nc.kind,
			&nc.x, &nc.y, &nc.w, &nc.h,
			&nc.viewX, &nc.viewY, &nc.viewZoom, &nc.childGrid,
			&nc.textX, &nc.textY, &nc.textW, &nc.textH, &nc.textMode, &nc.blob,
			&nc.urlString, &nc.previewBlob, &nc.fsPath, &nc.pid, &nc.sourceKey, &nc.altText,
			&nc.createdAt, &nc.updatedAt); err != nil {
			return 0, nil, err
		}
		copies = append(copies, nc)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	remap := make(map[int64]int64, len(copies))
	for _, nc := range copies {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, version, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id,
				text_x, text_y, text_w, text_h, text_mode, blob_id,
				url_string, preview_blob_id, fs_path, pid, source_key, alt_text,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nc.objectID, nc.version, newGridID, nc.kind, nc.x, nc.y, nc.w, nc.h,
			nc.viewX, nc.viewY, nc.viewZoom, nc.childGrid,
			nc.textX, nc.textY, nc.textW, nc.textH, nc.textMode, nc.blob,
			nc.urlString, nc.previewBlob, nc.fsPath, nc.pid, nc.sourceKey, nc.altText,
			nc.createdAt, now)
		if err != nil {
			return 0, nil, fmt.Errorf("copy tile: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return 0, nil, err
		}
		remap[nc.oldID] = newID
		// Bump the refcount on whatever this copied row also points at —
		// child grid (well / file-well / process-well), text blob, or
		// url/shell preview blob. file-wells and process-wells share the
		// same backing fs/proc grid across clones (identity = path/PID),
		// so a fork still just bumps refcount, same as a regular well.
		if err := s.incTileRefs(ctx, tx, nc.kind,
			nullToInt(nc.childGrid), nullToInt(nc.blob), nullToInt(nc.previewBlob)); err != nil {
			return 0, nil, err
		}
	}
	return newGridID, remap, nil
}

func (s *Store) incRefcount(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE `+schemaOf(gridID)+`grids SET refcount = refcount + 1 WHERE id = ?`, gridID)
	return err
}

func (s *Store) decRefcount(ctx context.Context, tx *sql.Tx, gridID int64) error {
	sc := schemaOf(gridID)
	if _, err := tx.ExecContext(ctx, `UPDATE `+sc+`grids SET refcount = refcount - 1 WHERE id = ?`, gridID); err != nil {
		return err
	}
	var rc int64
	if err := tx.QueryRowContext(ctx, `SELECT refcount FROM `+sc+`grids WHERE id = ?`, gridID).Scan(&rc); err != nil {
		return err
	}
	if rc <= 0 {
		return s.deleteGrid(ctx, tx, gridID)
	}
	return nil
}

func (s *Store) deleteGrid(ctx context.Context, tx *sql.Tx, gridID int64) error {
	sc := schemaOf(gridID)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, kind, child_grid_id, blob_id, preview_blob_id FROM `+sc+`tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type ref struct {
		id      int64
		kind    string
		child   sql.NullInt64
		blob    sql.NullInt64
		preview sql.NullInt64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.kind, &r.child, &r.blob, &r.preview); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+sc+`tiles WHERE id = ?`, r.id); err != nil {
			return err
		}
		// Release every reference this row held: child grid (well /
		// file-well / process-well), text blob, or url/shell preview blob.
		// GC'ing a grid that holds a file-well, process-well, or shell tile
		// used to leak the fs/proc grid refcount or the preview blob — this
		// path now goes through the same table-driven map as fork/clone.
		if err := s.decTileRefs(ctx, tx, r.kind,
			nullToInt(r.child), nullToInt(r.blob), nullToInt(r.preview)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+sc+`grids WHERE id = ?`, gridID); err != nil {
		return err
	}
	return nil
}

// putBlob inserts a blob row if one with the given hash doesn't already exist,
// and returns its id. It does NOT bump the refcount — callers must do that
// explicitly so the refcount semantics remain visible at the call site.
//
// mediaType is the IANA type stamped on a newly-created blob so it is
// self-describing (see schema.go). An already-present blob (same hash) keeps
// its original media_type and created_at — content-addressed blobs are
// immutable, so the first writer's metadata stands.
//
// nil is normalized to an empty (but non-nil) slice before binding:
// database/sql maps nil-bytes to SQL NULL, which would trip the
// data BLOB NOT NULL constraint. Empty-content tiles are a valid use
// case — a fresh palette drop of a markdown tile arrives here with
// Data=nil (proto3 default-value omission round-tripped through the
// wire as a missing field), and that path has to succeed.
// schemaPrefix selects which file the blob lives in ("" main, "cache." for
// ephemeral content like the @info markdown of a cache-resident proc tile).
// Dedup is per-file: the same bytes may exist once in each, which is fine —
// cache blobs are disposable.
func (s *Store) putBlob(ctx context.Context, tx *sql.Tx, schemaPrefix, hash string, data []byte, mediaType string) (int64, error) {
	if data == nil {
		data = []byte{}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM `+schemaPrefix+`blobs WHERE hash = ?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO `+schemaPrefix+`blobs (hash, size, data, refcount, media_type, created_at) VALUES (?, ?, ?, 0, ?, ?)`,
			hash, len(data), data, mediaType, s.now().Unix())
		if err != nil {
			return 0, fmt.Errorf("insert blob: %w", err)
		}
		return res.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) incBlobRefcount(ctx context.Context, tx *sql.Tx, blobID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE `+schemaOf(blobID)+`blobs SET refcount = refcount + 1 WHERE id = ?`, blobID)
	return err
}

// swapTileBlob repoints a tile's blob column at the content-addressed blob
// for `bytes`, keeping refcounts balanced. It hashes + dedupes via putBlob;
// when the resulting blob differs from what the column held it UPDATEs
// tiles.<col> (+ updated_at) and inc-new / dec-old. Identical content is a
// pure no-op — no write, no refcount churn, changed=false — which matches the
// idempotent reconcile path and is harmless for the freeze paths (their
// version bump is independent of updated_at).
//
// This is the single home for the blob-swap dance that SetShellPreview,
// SetURLState (jpeg), UpdateText (blob), and refreshProcInfoBlob each used to
// hand-roll. Callers keep their own version bump and any sibling-column
// writes (alt / url / title); only the blob kernel lives here.
//
// col must be a trusted literal ("blob_id" / "preview_blob_id") — it is
// interpolated into the SQL, never user input. mediaType is stamped on the
// blob if it is newly created (self-describing media).
func (s *Store) swapTileBlob(ctx context.Context, tx *sql.Tx, tileID int64, col string, bytes []byte, mediaType string) (newBlobID int64, changed bool, err error) {
	sc := schemaOf(tileID)
	var oldBlob sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT `+col+` FROM `+sc+`tiles WHERE id = ?`, tileID).Scan(&oldBlob); err != nil {
		return 0, false, err
	}
	// The blob lives in the same file as the tile that references it.
	newBlobID, err = s.putBlob(ctx, tx, sc, hashBytes(bytes), bytes, mediaType)
	if err != nil {
		return 0, false, err
	}
	if oldBlob.Valid && oldBlob.Int64 == newBlobID {
		return newBlobID, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+sc+`tiles SET `+col+` = ?, updated_at = ? WHERE id = ?`,
		newBlobID, s.now().Unix(), tileID); err != nil {
		return 0, false, err
	}
	if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
		return 0, false, err
	}
	if oldBlob.Valid && oldBlob.Int64 != 0 {
		if err := s.decBlobRefcount(ctx, tx, oldBlob.Int64); err != nil {
			return 0, false, err
		}
	}
	return newBlobID, true, nil
}

// nullToInt unwraps a NullInt64, mapping NULL to 0 — the "no reference"
// sentinel the refcount helpers use.
func nullToInt(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// tileRefs is the single source of truth for what a tile holds a refcount
// on: the child grid (well / file-well / process-well), the text blob, or
// the url/shell preview blob — derived from its raw child_grid_id, blob_id,
// and preview_blob_id (0 = none). fork, clone, single-delete, and grid GC
// all route through it so they can never drift per-kind again. They used
// to: three hand-rolled switches disagreed, leaking shell preview blobs on
// fork/clone and fs/proc child grids (plus shell blobs) on GC.
func tileRefs(kind string, childGrid, blob, previewBlob int64) (gridRef, blobRef int64) {
	switch kind {
	case rpc.KindWell, rpc.KindFileWell, rpc.KindProcessWell:
		return childGrid, 0
	case rpc.KindText:
		return 0, blob
	case rpc.KindURL, rpc.KindShell:
		return 0, previewBlob
	}
	return 0, 0
}

// incTileRefs bumps the refcounts a tile of the given kind holds. Called
// when a tile row is materialized by copy (forkGrid) or clone (CloneTile).
func (s *Store) incTileRefs(ctx context.Context, tx *sql.Tx, kind string, childGrid, blob, previewBlob int64) error {
	g, b := tileRefs(kind, childGrid, blob, previewBlob)
	if g != 0 {
		if err := s.incRefcount(ctx, tx, g); err != nil {
			return err
		}
	}
	if b != 0 {
		if err := s.incBlobRefcount(ctx, tx, b); err != nil {
			return err
		}
	}
	return nil
}

// decTileRefs releases the refcounts a tile of the given kind holds. Called
// when a tile row is destroyed by single-delete (dropTileRow) or grid GC
// (deleteGrid).
func (s *Store) decTileRefs(ctx context.Context, tx *sql.Tx, kind string, childGrid, blob, previewBlob int64) error {
	g, b := tileRefs(kind, childGrid, blob, previewBlob)
	if g != 0 {
		if err := s.decRefcount(ctx, tx, g); err != nil {
			return err
		}
	}
	if b != 0 {
		if err := s.decBlobRefcount(ctx, tx, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) decBlobRefcount(ctx context.Context, tx *sql.Tx, blobID int64) error {
	sc := schemaOf(blobID)
	if _, err := tx.ExecContext(ctx, `UPDATE `+sc+`blobs SET refcount = refcount - 1 WHERE id = ?`, blobID); err != nil {
		return err
	}
	var rc int64
	if err := tx.QueryRowContext(ctx, `SELECT refcount FROM `+sc+`blobs WHERE id = ?`, blobID).Scan(&rc); err != nil {
		return err
	}
	if rc <= 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM `+sc+`blobs WHERE id = ?`, blobID)
		return err
	}
	return nil
}
