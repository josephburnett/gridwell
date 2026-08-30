package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// isWellKind reports whether a tile kind has a child grid that can be
// descended into (the "well" kind — interior or, by a cross-plugin
// child_grid_id, an exit well). Thin alias for rpc.IsWellKind so the set lives
// in one place; kept as a package-local name for the many store callsites.
func isWellKind(kind string) bool {
	return rpc.IsWellKind(kind)
}

// cloneSubtree deep-copies a grid and everything beneath it into fresh rows,
// returning the new grid id. It is the eager copy the clone gesture performs
// for an interior well: each grid and tile gets a brand-new row id
// (version preserved), so no tile is ever shared
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
		`INSERT INTO grids (version, created_at, updated_at) VALUES (?, ?, ?)`,
		old.Version, now, now)
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

// childGridForClone returns the child_grid_id a copy of tile n should carry,
// as a value ready to bind into the INSERT (nil → NULL, int64 → a local grid,
// string → a qualified cross-plugin reference):
//   - interior well (numeric child): a deep copy of its subtree (cloneSubtree),
//     returned as the new grid's id;
//   - exit well (qualified "<uuid>/<id>" child): the SAME cross-plugin
//     reference — the child grid is owned by another plugin, not duplicated;
//   - everything else: nil.
func (s *Store) childGridForClone(ctx context.Context, tx *sql.Tx, n *rpc.Tile) (any, error) {
	if n.ChildGridID == "" {
		return nil, nil
	}
	childID, err := strconv.ParseInt(n.ChildGridID, 10, 64)
	if err != nil {
		// Qualified cross-plugin reference: preserve it verbatim.
		return n.ChildGridID, nil
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

// tileCopyColumns is the full set of tiles columns a clone writes, in the
// bind order of insertTileCopy's INSERT. Every schema column must be either
// here or on the deliberate not-copied list in TestTileCopyColumnsAreTotal —
// that drift lint is what makes "add a column, forget the clone path" a loud
// failure instead of a silently incomplete copy.
var tileCopyColumns = []string{
	"version", "grid_id", "kind", "x", "y", "w", "h",
	"view_cx", "view_cy", "view_zoom", "child_grid_id",
	"text_x", "text_y", "text_w", "text_h", "text_mode", "blob_id",
	"url_string", "preview_blob_id", "alt_text", "alt_user",
	"content_zoom", "url_history", "link_target_id", "url_frozen",
	"created_at", "updated_at",
}

// placeholders returns "?, ?, …" with n placeholders.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// insertTileCopy inserts a copy of tile n into gridID at (x, y) with the given
// child grid, preserving version and sharing the
// blob (refcount bumped). The per-kind column nullability mirrors the schema
// CHECK constraint. Used by CloneTile (one tile) and cloneSubtree (every tile
// in a subtree).
func (s *Store) insertTileCopy(ctx context.Context, tx *sql.Tx, gridID int64, n *rpc.Tile, x, y int64, child any, now int64) (int64, error) {
	var (
		blob, previewBlob sql.NullInt64
		urlStr, textMode  sql.NullString
		urlHist           sql.NullString
		linkTarget        sql.NullString
	)
	if n.LinkTargetID != "" {
		// A copy of a LINK is another link to the same target — the link row
		// holds no content, so there is nothing else to copy (the CHECK's
		// link branch requires every content column NULL).
		linkTarget = sql.NullString{String: n.LinkTargetID, Valid: true}
	}
	switch {
	case n.LinkTargetID != "":
		// No content columns on a link row; skip the per-kind content copy.
	case n.Kind == rpc.KindURL:
		urlStr = sql.NullString{String: n.URLString, Valid: true}
		if n.PreviewBlobID != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobID, Valid: true}
		}
		if n.URLHistory != "" {
			urlHist = sql.NullString{String: n.URLHistory, Valid: true}
		}
	case n.Kind == rpc.KindShell:
		// A PTY can't be copied, so a cloned shell is a screenshot: carry the
		// frozen preview blob, but not the live session (keyed by tile id).
		if n.PreviewBlobID != 0 {
			previewBlob = sql.NullInt64{Int64: n.PreviewBlobID, Valid: true}
		}
	case n.Kind == rpc.KindText:
		if n.BlobID != 0 {
			blob = sql.NullInt64{Int64: n.BlobID, Valid: true}
		}
		if n.TextMode != "" {
			textMode = sql.NullString{String: n.TextMode, Valid: true}
		}
	case n.Kind == rpc.KindPane:
		// The layout blob is shared by refcount like a text body; the copy
		// diverges on its first edit (content addressing). NULL (never
		// arranged) copies as NULL.
		if n.BlobID != 0 {
			blob = sql.NullInt64{Int64: n.BlobID, Valid: true}
		}
	}
	// alt_user is storage-only (deliberately not on rpc.Tile), so the latch is
	// read straight from the source row: a user-owned name must stay
	// user-owned on the copy, or the next automatic title capture clobbers it
	// (the issue-#61 class).
	srcID, err := parseID(n.ID)
	if err != nil {
		return 0, fmt.Errorf("tile copy: source id %q: %w", n.ID, err)
	}
	var altUser int64
	if err := tx.QueryRowContext(ctx,
		`SELECT alt_user FROM tiles WHERE id = ?`, srcID).Scan(&altUser); err != nil {
		return 0, fmt.Errorf("tile copy: read alt_user of source %d: %w", srcID, err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO tiles (`+strings.Join(tileCopyColumns, ", ")+`)
		VALUES (`+placeholders(len(tileCopyColumns))+`)`,
		n.Version, gridID, n.Kind, x, y, n.W, n.H,
		n.ViewCx, n.ViewCy, n.ViewZoom, child,
		n.TextX, n.TextY, n.TextW, n.TextH, textMode, blob,
		urlStr, previewBlob, n.AltText, altUser,
		n.ContentZoom, urlHist, linkTarget, boolToInt(n.URLFrozen),
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
// its original media_type — content-addressed blobs are immutable, so the
// first writer's metadata stands.
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
	var linkTarget sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT `+col+`, link_target_id FROM tiles WHERE id = ?`, tileID).Scan(&oldBlob, &linkTarget); err != nil {
		return 0, false, err
	}
	if linkTarget.Valid && linkTarget.String != "" {
		// A LINK row owns no content — its bytes live in the target tile, and
		// content mutations must be routed there (by the qualified target id)
		// or the link and the thing it names silently diverge. Guarded here,
		// in the one blob kernel every content write flows through (text body,
		// pane layout, url/shell previews), rather than per caller.
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
	case rpc.KindText, rpc.KindPane:
		// A pane tile owns its layout blob exactly as a text tile owns its
		// body: clone shares by refcount, delete decs, GC at zero. The
		// PLACES the layout references are not owned — deleting a workspace
		// deletes only the arrangement.
		return 0, blob
	case rpc.KindURL, rpc.KindShell:
		return 0, previewBlob
	}
	return 0, 0
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
