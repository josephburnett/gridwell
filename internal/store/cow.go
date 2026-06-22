package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

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
	for _, wellIDStr := range p.WellIDs {
		wellID, err := parseID(wellIDStr)
		if err != nil {
			return gridSequence{}, fmt.Errorf("%w: well %q: %v", ErrInvalidPath, wellIDStr, err)
		}
		w, err := s.loadTile(ctx, q, wellID)
		if err != nil {
			return gridSequence{}, fmt.Errorf("%w: well %d: %v", ErrInvalidPath, wellID, err)
		}
		if !isWellKind(w.Kind) {
			return gridSequence{}, fmt.Errorf("%w: tile %d is not a well", ErrInvalidPath, wellID)
		}
		if w.GridID != strconv.FormatInt(seq.grids[len(seq.grids)-1], 10) {
			return gridSequence{}, fmt.Errorf("%w: well %d not in grid %d", ErrInvalidPath, wellID, seq.grids[len(seq.grids)-1])
		}
		seq.wells = append(seq.wells, wellID)
		childID, _ := strconv.ParseInt(w.ChildGridID, 10, 64)
		seq.grids = append(seq.grids, childID)
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

// checkPathLeaf validates path and that `tile` lives in its leaf grid,
// returning that leaf grid id. This is the in-place-edit replacement for the
// old preWrite: with copy-on-clone nothing is ever shared, so a content edit
// never forks — it writes the tile exactly where it sits. The path is still
// validated (it's how the pane says where it is) and the tile must be in it.
func (s *Store) checkPathLeaf(ctx context.Context, tx *sql.Tx, path rpc.Path, tile *rpc.Tile) (int64, error) {
	seq, err := s.buildGridSequence(ctx, tx, path)
	if err != nil {
		return 0, err
	}
	leaf := seq.grids[len(seq.grids)-1]
	if tile.GridID != strconv.FormatInt(leaf, 10) {
		return 0, fmt.Errorf("%w: tile %s not in path leaf grid %d", ErrInvalidPath, tile.ID, leaf)
	}
	return leaf, nil
}

// cloneSubtree deep-copies a grid and everything beneath it into fresh rows,
// returning the new grid id. It is the eager copy the clone gesture performs
// for an interior well: each grid and tile gets a brand-new row id (object_id
// and version preserved as a provenance marker), so no tile is ever shared
// between two clones — editing one can never touch the other, and no id is
// ever reassigned. Blobs (immutable content) are shared by reference
// (refcount bumped); host-backed source grids behind file/process wells are
// shared by identity, not copied (see childGridForClone). "Things stay where
// you put them": the copy is what the user explicitly asked for, the original
// is untouched.
func (s *Store) cloneSubtree(ctx context.Context, tx *sql.Tx, srcGridID int64) (int64, error) {
	old, err := s.loadGrid(ctx, tx, srcGridID)
	if err != nil {
		return 0, err
	}
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (object_id, version, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		old.ObjectID, old.Version, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert cloned grid: %w", err)
	}
	newGridID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// loadTilesInGrid materializes the rows before we recurse, so we're not
	// iterating a live cursor on the single connection while issuing nested
	// inserts.
	tiles, err := s.loadTilesInGrid(ctx, tx, srcGridID)
	if err != nil {
		return 0, err
	}
	for i := range tiles {
		src := &tiles[i]
		child, err := s.childGridForClone(ctx, tx, src)
		if err != nil {
			return 0, err
		}
		if _, err := s.insertTileCopy(ctx, tx, newGridID, src, src.X, src.Y, child, now); err != nil {
			return 0, err
		}
	}
	return newGridID, nil
}

// childGridForClone returns the child_grid_id a copy of tile n should carry:
//   - interior well: a deep copy of its subtree (recursive cloneSubtree);
//   - file/process well: the SAME host-backed source grid (shared by identity
//     — you can't deep-copy the filesystem or the process table);
//   - everything else: none.
func (s *Store) childGridForClone(ctx context.Context, tx *sql.Tx, n *rpc.Tile) (sql.NullInt64, error) {
	if n.ChildGridID == "" {
		return sql.NullInt64{}, nil
	}
	childID, _ := strconv.ParseInt(n.ChildGridID, 10, 64)
	switch n.Kind {
	case rpc.KindWell:
		newChild, err := s.cloneSubtree(ctx, tx, childID)
		if err != nil {
			return sql.NullInt64{}, err
		}
		return sql.NullInt64{Int64: newChild, Valid: true}, nil
	case rpc.KindFileWell, rpc.KindProcessWell:
		return sql.NullInt64{Int64: childID, Valid: true}, nil
	}
	return sql.NullInt64{}, nil
}

// insertTileCopy inserts a copy of tile n into gridID at (x, y) with the given
// child grid, preserving object_id + version (provenance) and sharing the
// blob (refcount bumped). The per-kind column nullability mirrors the schema
// CHECK constraint. Used by CloneTile (one tile) and cloneSubtree (every tile
// in a subtree).
func (s *Store) insertTileCopy(ctx context.Context, tx *sql.Tx, gridID int64, n *rpc.Tile, x, y int64, child sql.NullInt64, now int64) (int64, error) {
	var (
		blob, previewBlob, pidNS sql.NullInt64
		urlStr, textMode, fsPath sql.NullString
	)
	switch n.Kind {
	case rpc.KindFileWell:
		fsPath = sql.NullString{String: n.FSPath, Valid: true}
	case rpc.KindProcessWell:
		pidNS = sql.NullInt64{Int64: n.PID, Valid: true}
	case rpc.KindURL:
		urlStr = sql.NullString{String: n.URLString, Valid: true}
		if n.PreviewBlobID != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobID, Valid: true}
		}
	case rpc.KindShell:
		// A PTY can't be copied, so a cloned shell is a screenshot: carry the
		// frozen preview blob, but not the live session (keyed by tile id).
		if n.PreviewBlobID != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobID, Valid: true}
		}
	case rpc.KindText:
		if n.BlobID != 0 {
			blob = sql.NullInt64{Int64: n.BlobID, Valid: true}
		}
		if n.TextMode != "" {
			textMode = sql.NullString{String: n.TextMode, Valid: true}
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tiles (object_id, version, grid_id, kind, x, y, w, h,
			view_x, view_y, view_zoom, child_grid_id,
			text_x, text_y, text_w, text_h, text_mode, blob_id,
			url_string, preview_blob_id, fs_path, pid, source_key, alt_text,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ObjectID, n.Version, gridID, n.Kind, x, y, n.W, n.H,
		n.ViewX, n.ViewY, n.ViewZoom, child,
		n.TextX, n.TextY, n.TextW, n.TextH, textMode, blob,
		urlStr, previewBlob, fsPath, pidNS, nullableString(n.SourceKey), n.AltText,
		now, now)
	if err != nil {
		return 0, fmt.Errorf("insert tile copy: %w", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Bump refcount for whatever blob the new row actually holds.
	if blob.Valid {
		if err := s.incBlobRefcount(ctx, tx, blob.Int64); err != nil {
			return 0, err
		}
	}
	if previewBlob.Valid {
		if err := s.incBlobRefcount(ctx, tx, previewBlob.Int64); err != nil {
			return 0, err
		}
	}
	return newID, nil
}

// deleteGrid recursively deletes a grid and everything it owns: each tile row
// is dropped, interior-well child grids cascade (decTileRefs), and text /
// preview blobs are released. Host-backed source grids behind file/process
// wells are left alone — they're shared by identity and disposable. Owned
// grids are 1:1 with their parent well, so there is no refcount to consult;
// deleting the well deletes the grid.
func (s *Store) deleteGrid(ctx context.Context, tx *sql.Tx, gridID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, kind, child_grid_id, blob_id, preview_blob_id FROM tiles WHERE grid_id = ?`, gridID)
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, r.id); err != nil {
			return err
		}
		if err := s.decTileRefs(ctx, tx, r.kind,
			nullToInt(r.child), nullToInt(r.blob), nullToInt(r.preview)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM grids WHERE id = ?`, gridID); err != nil {
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
func (s *Store) putBlob(ctx context.Context, tx *sql.Tx, hash string, data []byte, mediaType string) (int64, error) {
	if data == nil {
		data = []byte{}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO blobs (hash, size, data, refcount, media_type, created_at) VALUES (?, ?, ?, 0, ?, ?)`,
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
	_, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, blobID)
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
	var oldBlob sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT `+col+` FROM tiles WHERE id = ?`, tileID).Scan(&oldBlob); err != nil {
		return 0, false, err
	}
	newBlobID, err = s.putBlob(ctx, tx, hashBytes(bytes), bytes, mediaType)
	if err != nil {
		return 0, false, err
	}
	if oldBlob.Valid && oldBlob.Int64 == newBlobID {
		return newBlobID, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tiles SET `+col+` = ?, updated_at = ? WHERE id = ?`,
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

// tileRefs is the single source of truth for what a tile *owns*: an interior
// well's child grid (deep-copied on clone, recursively deleted on delete) and
// the blob it holds a refcount on (its text body, or a url/shell preview).
// file/process wells point at a host-backed source grid shared by identity, so
// they own no grid — only their own preview blob (if any), exactly like
// url/shell. Derived from the raw child_grid_id, blob_id, and preview_blob_id
// (0 = none). clone, single-delete, and grid teardown all route through it.
func tileRefs(kind string, childGrid, blob, previewBlob int64) (gridRef, blobRef int64) {
	switch kind {
	case rpc.KindWell:
		return childGrid, 0
	case rpc.KindText:
		return 0, blob
	case rpc.KindURL, rpc.KindShell, rpc.KindFileWell, rpc.KindProcessWell:
		return 0, previewBlob
	}
	return 0, 0
}

// tileBlobRef returns the blob a tile of the given kind holds a refcount on
// (text body or url/shell preview), or 0. The blob half of tileRefs, used when
// materializing a copy.
func tileBlobRef(kind string, blob, previewBlob int64) int64 {
	_, b := tileRefs(kind, 0, blob, previewBlob)
	return b
}

// decTileRefs releases what a deleted tile owned: its interior-well child grid
// (recursively deleted) and its blob (refcount dec, GC'd at zero). Called when
// a tile row is destroyed by single-delete (dropTileRow / deleteFSGridTile) or
// grid teardown (deleteGrid).
func (s *Store) decTileRefs(ctx context.Context, tx *sql.Tx, kind string, childGrid, blob, previewBlob int64) error {
	g, b := tileRefs(kind, childGrid, blob, previewBlob)
	if g != 0 {
		if err := s.deleteGrid(ctx, tx, g); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount - 1 WHERE id = ?`, blobID); err != nil {
		return err
	}
	var rc int64
	if err := tx.QueryRowContext(ctx, `SELECT refcount FROM blobs WHERE id = ?`, blobID).Scan(&rc); err != nil {
		return err
	}
	if rc <= 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, blobID)
		return err
	}
	return nil
}
