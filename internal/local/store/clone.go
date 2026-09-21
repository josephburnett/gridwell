package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// isWellKind is an alias for rpc.IsWellKind, so the set lives in one place.
func isWellKind(kind string) bool {
	return rpc.IsWellKind(kind)
}

// cloneSubtree deep-copies a grid and everything beneath it into fresh rows.
// Each grid and tile gets a new row id, with the version preserved, so no tile
// is shared between two clones and editing one can never touch the other.
// Blobs are immutable and shared by refcount; source grids behind file and
// process wells are shared by identity. See childGridForClone.
func (s *Store) cloneSubtree(ctx context.Context, tx *sql.Tx, srcGridID int64) (int64, error) {
	old, err := s.loadGrid(ctx, tx, srcGridID)
	if err != nil {
		return 0, err
	}
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (version, created_at, updated_at) VALUES (?, ?, ?)`,
		old.Version, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert cloned grid: %w", err)
	}
	newGridID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// loadTilesInGrid materializes the rows before recursing, so a live cursor
	// on the single connection is not iterated while nested inserts run.
	tiles, err := s.loadTilesInGrid(ctx, tx, srcGridID)
	if err != nil {
		return 0, err
	}
	for _, src := range tiles {
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

// childGridForClone returns the child_grid_id a copy of tile n should carry,
// ready to bind into the INSERT. An interior well gets a deep copy of its
// subtree; an exit well keeps its qualified cross-plugin reference, since
// another plugin owns that grid; everything else gets nil.
func (s *Store) childGridForClone(ctx context.Context, tx *sql.Tx, n *gridwellv1.Tile) (any, error) {
	if n.ChildGridId == "" {
		return nil, nil
	}
	childID, err := strconv.ParseInt(n.ChildGridId, 10, 64)
	if err != nil {
		return n.ChildGridId, nil
	}
	if n.Kind == rpc.KindWell {
		newChild, err := s.cloneSubtree(ctx, tx, childID)
		if err != nil {
			return nil, err
		}
		return newChild, nil
	}
	return nil, nil
}

// placeholders returns "?, ?, …" with n placeholders.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// insertTileCopy inserts a copy of tile n into gridID at (x, y), preserving
// the version and sharing the blob with its refcount bumped. The per-kind
// column nullability mirrors the schema CHECK constraint.
func (s *Store) insertTileCopy(ctx context.Context, tx *sql.Tx, gridID int64, n *gridwellv1.Tile, x, y int64, child any, now int64) (int64, error) {
	var (
		blob, previewBlob sql.NullInt64
		urlStr, textMode  sql.NullString
		urlHist           sql.NullString
		linkTarget        sql.NullString
	)
	if n.LinkTargetId != "" {
		// A copy of a link is another link to the same target, and the CHECK's
		// link branch requires every content column NULL.
		linkTarget = sql.NullString{String: n.LinkTargetId, Valid: true}
	}
	switch {
	case n.LinkTargetId != "":
	case n.Kind == rpc.KindURL:
		urlStr = sql.NullString{String: n.UrlString, Valid: true}
		if n.PreviewBlobId != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobId, Valid: true}
		}
		if n.UrlHistory != "" {
			urlHist = sql.NullString{String: n.UrlHistory, Valid: true}
		}
	case n.Kind == rpc.KindShell:
		// A PTY cannot be copied, so a cloned shell carries the frozen preview
		// blob but not the live session, which is keyed by tile id.
		if n.PreviewBlobId != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobId, Valid: true}
		}
	case n.Kind == rpc.KindText:
		if n.BlobId != 0 {
			blob = sql.NullInt64{Int64: n.BlobId, Valid: true}
		}
		if n.TextMode != "" {
			textMode = sql.NullString{String: n.TextMode, Valid: true}
		}
	case n.Kind == rpc.KindPane:
		// The layout blob is shared by refcount like a text body, and the copy
		// diverges on its first edit. NULL means never arranged.
		if n.BlobId != 0 {
			blob = sql.NullInt64{Int64: n.BlobId, Valid: true}
		}
	}
	// alt_user is storage-only, so the latch is read straight from the source
	// row: a user-owned name must stay user-owned on the copy, or the next
	// automatic title capture clobbers it.
	srcID, err := parseID(n.Id)
	if err != nil {
		return 0, fmt.Errorf("tile copy: source id %q: %w", n.Id, err)
	}
	var altUser int64
	if err := tx.QueryRowContext(ctx,
		`SELECT alt_user FROM tiles WHERE id = ?`, srcID).Scan(&altUser); err != nil {
		return 0, fmt.Errorf("tile copy: read alt_user of source %d: %w", srcID, err)
	}
	// The copy is written by name: copyBinding renders the column list from
	// the descriptor in columns.go and refuses a map that misses a copied
	// column, so a forgotten clone path is a named error, not a silent gap.
	cols, args, err := copyBinding(map[string]any{
		"version": n.Version, "grid_id": gridID, "kind": n.Kind,
		"x": x, "y": y, "w": n.W, "h": n.H,
		"view_cx": n.ViewCx, "view_cy": n.ViewCy, "view_zoom": n.ViewZoom,
		"child_grid_id": child,
		"text_x":        n.TextX, "text_y": n.TextY, "text_w": n.TextW, "text_h": n.TextH,
		"text_mode": textMode, "blob_id": blob,
		"url_string": urlStr, "preview_blob_id": previewBlob,
		"alt_text": n.AltText, "alt_user": altUser,
		"content_zoom": n.ContentZoom, "url_history": urlHist,
		"link_target_id": linkTarget, "url_frozen": boolToInt(n.UrlFrozen),
		"created_at": now, "updated_at": now,
	})
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO tiles (`+cols+`) VALUES (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("insert tile copy: %w", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
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

// deleteGrid recursively deletes a grid and everything it owns. Source grids
// behind file and process wells are left alone, being shared by identity. An
// owned grid is 1:1 with its parent well, so there is no refcount to consult.
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
	refs, err := collect(rows, func(rows *sql.Rows) (ref, error) {
		var r ref
		err := rows.Scan(&r.id, &r.kind, &r.child, &r.blob, &r.preview)
		return r, err
	})
	if err != nil {
		return err
	}
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

// putBlob inserts a blob row if one with the given hash does not exist and
// returns its id. It does not bump the refcount; callers do that explicitly so
// the semantics stay visible at the call site. An already-present blob keeps
// its original media_type, content-addressed blobs being immutable. nil is
// normalized to an empty non-nil slice, because database/sql maps nil bytes to
// SQL NULL and empty-content tiles are valid.
func (s *Store) putBlob(ctx context.Context, tx *sql.Tx, hash string, data []byte, mediaType string) (int64, error) {
	if data == nil {
		data = []byte{}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO blobs (hash, data, refcount, media_type) VALUES (?, ?, 0, ?)`,
			hash, data, mediaType)
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

// swapTileBlob repoints a tile's blob column at the content-addressed blob for
// bytes, keeping refcounts balanced. Identical content is a pure no-op, which
// is what the idempotent reconcile path needs. It is the single home for the
// blob swap every content write flows through; callers keep their own version
// bump and sibling-column writes. col must be a trusted literal, "blob_id" or
// "preview_blob_id": it is interpolated into the SQL, never user input.
func (s *Store) swapTileBlob(ctx context.Context, tx *sql.Tx, tileID int64, col string, bytes []byte, mediaType string) (newBlobID int64, changed bool, err error) {
	var oldBlob sql.NullInt64
	var linkTarget sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT `+col+`, link_target_id FROM tiles WHERE id = ?`, tileID).Scan(&oldBlob, &linkTarget); err != nil {
		return 0, false, err
	}
	if linkTarget.Valid && linkTarget.String != "" {
		// A link row owns no content: its bytes live in the target tile, and a
		// content mutation must be routed there or the link and the thing it
		// names silently diverge. The guard is in the one blob kernel rather
		// than per caller.
		return 0, false, fmt.Errorf("%w: tile %d is a link; content lives in its target %s",
			ErrInvalidArgument, tileID, linkTarget.String)
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

// nullToInt maps NULL to 0, the "no reference" sentinel the refcount helpers
// use.
func nullToInt(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// tileRefs is the one description of what a tile owns: an interior well's
// child grid, and the blob it holds a refcount on. A file or process well
// points at a source grid shared by identity, so it owns no grid. Clone,
// single-delete and grid teardown all route through it.
func tileRefs(kind string, childGrid, blob, previewBlob int64) (gridRef, blobRef int64) {
	switch {
	case isWellKind(kind):
		return childGrid, 0
	case rpc.IsBodyKind(kind):
		// The places a pane layout references are not owned, so deleting a
		// pane tile deletes only the arrangement.
		return 0, blob
	case kind == rpc.KindURL || kind == rpc.KindShell:
		return 0, previewBlob
	}
	return 0, 0
}

// decTileRefs releases what a deleted tile owned: its interior-well child
// grid, recursively, and its blob, collected at refcount zero.
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
