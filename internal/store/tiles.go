package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// urlSchemeAllowed reports whether u is one of the schemes accepted by
// URL tiles. Only http and https.
func urlSchemeAllowed(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// MaxBlobBytes caps a single uploaded text-tile blob size.
const MaxBlobBytes = 16 * 1024 * 1024

// checkTileVersion loads a tile and verifies its claimed version matches.
// Returns the loaded tile.
func (s *Store) checkTileVersion(ctx context.Context, q gridReader, tileID, claimed int64) (*rpc.Tile, error) {
	t, err := s.loadTile(ctx, q, tileID)
	if err != nil {
		return nil, err
	}
	if t.Version != claimed {
		return nil, fmt.Errorf("%w: tile %d at version %d, claimed %d",
			ErrVersionConflict, tileID, t.Version, claimed)
	}
	return t, nil
}

// loadForEdit is the shared preamble for a versioned single-tile mutation:
// version-check the tile (optimistic concurrency), optionally guard its kind,
// and validate it lives in the path's leaf grid (the in-place-edit check — no
// fork). wantKind == "" skips the kind guard (callers with a multi-kind rule,
// e.g. any well, do their own check on the returned tile). Returns the loaded
// tile plus its leaf grid id (the grid the overlap/insert checks run against).
//
// Folding these three steps into one call keeps the path-leaf validation from
// being silently dropped by a new mutation that copies only the version check.
func (s *Store) loadForEdit(ctx context.Context, tx *sql.Tx, path rpc.Path, tileID, version int64, wantKind string, wrongKindErr error) (*rpc.Tile, int64, error) {
	n, err := s.checkTileVersion(ctx, tx, tileID, version)
	if err != nil {
		return nil, 0, err
	}
	if wantKind != "" && n.Kind != wantKind {
		return nil, 0, wrongKindErr
	}
	leaf, err := s.checkPathLeaf(ctx, tx, path, n)
	if err != nil {
		return nil, 0, err
	}
	return n, leaf, nil
}

// emitTileChanged reloads tileID and appends a TileChanged event for it. It is
// the shared tail of every store write that publishes a tile. Framing setters
// (SetWellView / SetTextView) call it directly — re-framing is NOT a content
// edit, so it must not bump the version (CLAUDE.md). Content writers go through
// finishContentEdit instead.
func (s *Store) emitTileChanged(ctx context.Context, tx *sql.Tx, tileID int64, events *[]rpc.Event) (*rpc.Tile, error) {
	out, err := s.loadTile(ctx, tx, tileID)
	if err != nil {
		return nil, err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
	return out, nil
}

// finishContentEdit is the coda for a content mutation: bump the tile's version
// (the optimistic-concurrency key + edit-history spine) then publish it via
// emitTileChanged. Keeping the "content edit bumps version, framing edit does
// not" rule as a choice between two named helpers — rather than a per-method
// open-coded bump that a new mutation can forget or wrongly add — is what keeps
// that invariant from drifting.
func (s *Store) finishContentEdit(ctx context.Context, tx *sql.Tx, tileID int64, events *[]rpc.Event) (*rpc.Tile, error) {
	if err := bumpTileVersion(ctx, tx, tileID); err != nil {
		return nil, err
	}
	return s.emitTileChanged(ctx, tx, tileID, events)
}

// createTile is the shared scaffolding for the four Create* methods:
// sequence validation → overlap check → kind-specific insert → grid version
// bump → load → publish. The insert closure receives the canonical gridID, the
// current unix timestamp, and a fresh object_id; it inserts the tile row and
// returns its id.
func (s *Store) createTile(
	ctx context.Context,
	path rpc.Path, gridID, x, y, w, h int64,
	insert func(tx *sql.Tx, gridID, now int64, objID string) (tileID int64, err error),
) (*rpc.Tile, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		seq, err := s.buildGridSequence(ctx, tx, path)
		if err != nil {
			return err
		}
		if err := checkLeafGrid(seq, gridID); err != nil {
			return err
		}
		// The grid the pane is in IS where the tile lands — copy-on-clone
		// never shares a grid, so creation writes in place.
		gid := gridID

		over, err := overlapsExisting(ctx, tx, gid, x, y, w, h)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		tileID, err := insert(tx, gid, s.now().Unix(), s.newID())
		if err != nil {
			return err
		}
		if err := s.bumpGridVersion(ctx, tx, gid); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// CreateWell creates a new well at (x,y) with footprint (w,h) inside the leaf
// grid of req.Path. The child grid is created empty with no framing on the
// well (view_x/y/zoom all zero).
func (s *Store) CreateWell(ctx context.Context, req *rpc.CreateWellRequest) (*rpc.Tile, error) {
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			childObj := s.newID()
			res, err := tx.ExecContext(ctx,
				`INSERT INTO grids (object_id, created_at, updated_at) VALUES (?, ?, ?)`,
				childObj, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert child grid: %w", err)
			}
			childGridID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			res, err = tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					view_x, view_y, view_zoom, child_grid_id,
					created_at, updated_at)
				VALUES (?, ?, 'well', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, childGridID, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert well: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateText creates a markdown text tile.
func (s *Store) CreateText(ctx context.Context, req *rpc.CreateTextRequest) (*rpc.Tile, error) {
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	hash := hashBytes(req.Data)
	alt := markdown.AltFromSource(string(req.Data))
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			blobID, err := s.putBlob(ctx, tx, schemaOf(gridID), hash, req.Data, mediaMarkdown)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					blob_id, alt_text, created_at, updated_at)
				VALUES (?, ?, 'text', ?, ?, ?, ?, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, blobID, alt, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert text tile: %w", err)
			}
			tileID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			if err := s.incBlobRefcount(ctx, tx, blobID); err != nil {
				return 0, err
			}
			return tileID, nil
		})
}

// nullableString returns sql.NullString — empty values stored as NULL
// keep the schema honest about "no derived alt yet."
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// CreateURL creates a URL tile pointing at the given URL.
func (s *Store) CreateURL(ctx context.Context, req *rpc.CreateURLRequest) (*rpc.Tile, error) {
	urlString := strings.TrimSpace(req.URL)
	if !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					url_string, created_at, updated_at)
				VALUES (?, ?, 'url', ?, ?, ?, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, urlString, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert url tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateBlackHole creates a blackhole tile — a deletion sink.
func (s *Store) CreateBlackHole(ctx context.Context, req *rpc.CreateBlackHoleRequest) (*rpc.Tile, error) {
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					alt_text, created_at, updated_at)
				VALUES (?, ?, 'blackhole', ?, ?, ?, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, rpc.AltNull, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert blackhole tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// ResizeTile changes a tile's footprint to (X, Y, W, H).
func (s *Store) ResizeTile(ctx context.Context, req *rpc.ResizeTileRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		_, gridID, err := s.loadForEdit(ctx, tx, req.Path, req.TileID, req.Version, "", nil)
		if err != nil {
			return err
		}
		tileID := req.TileID

		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H, tileID)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+schemaOf(tileID)+`tiles SET x = ?, y = ?, w = ?, h = ?, updated_at = ? WHERE id = ?`,
			req.X, req.Y, req.W, req.H, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// SetWellView updates a well tile's framing (view_x/view_y/view_zoom).
//
// Framing is not a content edit: re-framing does NOT bump the tile version.
// It's an in-place write to this tile's row (copy-on-clone means clones are
// already independent, so there is nothing to fork) — the framing stays
// exactly as you left it.
func (s *Store) SetWellView(ctx context.Context, req *rpc.SetWellViewRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, _, err := s.loadForEdit(ctx, tx, req.Path, req.TileID, req.Version, "", nil)
		if err != nil {
			return err
		}
		if !isWellKind(n.Kind) {
			return ErrNotWellTile
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+schemaOf(req.TileID)+`tiles SET view_x = ?, view_y = ?, view_zoom = ?, updated_at = ? WHERE id = ?`,
			req.ViewX, req.ViewY, req.ViewZoom, s.now().Unix(), req.TileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, req.TileID, events)
		return err
	})
	return out, err
}

// SetTextView updates a text tile's framed-document window and rendered/text
// mode. Like SetWellView this is framing, not content: an in-place write that
// does NOT bump the tile version.
func (s *Store) SetTextView(ctx context.Context, req *rpc.SetTextViewRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, _, err := s.loadForEdit(ctx, tx, req.Path, req.TileID, req.Version, rpc.KindText, ErrNotTextTile); err != nil {
			return err
		}
		var textModeArg any
		if req.TextMode != "" {
			textModeArg = req.TextMode
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+schemaOf(req.TileID)+`tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ?, updated_at = ? WHERE id = ?`,
			req.TextX, req.TextY, req.TextW, req.TextH, textModeArg, s.now().Unix(), req.TileID); err != nil {
			return err
		}
		var err error
		out, err = s.emitTileChanged(ctx, tx, req.TileID, events)
		return err
	})
	return out, err
}

// DeleteTile removes a single tile by ID. Tiles inside fs / proc-backed
// grids are routed through deleteSourceTile so the host-side artifact
// (file, directory, process) is removed too.
func (s *Store) DeleteTile(ctx context.Context, req *rpc.DeleteTileRequest) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, _, err := s.loadForEdit(ctx, tx, req.Path, req.TileID, req.Version, "", nil)
		if err != nil {
			return err
		}
		parent, err := s.loadGrid(ctx, tx, t.GridID)
		if err != nil {
			return err
		}
		if parent.SourceKind != "" {
			handled, err := s.deleteSourceTile(ctx, tx, t, parent, events)
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
		}
		return s.dropTileRow(ctx, tx, t, events)
	})
}
