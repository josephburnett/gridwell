package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
)

// urlSchemeAllowed accepts only http and https.
func urlSchemeAllowed(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// MaxBlobBytes caps a single uploaded text-tile blob size; see
// rpc.MaxContentBytes.
const MaxBlobBytes = rpc.MaxContentBytes

// claimContentVersion verifies the caller's content claim against the row. It
// is the store's only optimistic-concurrency check, and "version is the claim
// for content and nothing else" is enforced by who can reach it: WriteContent's
// text and url arms, and RenameTile.
func (s *Store) claimContentVersion(ctx context.Context, q gridReader, tileID, claimed int64) (*gridwellv1.Tile, error) {
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

// loadForWrite is the shared preamble for an unclaimed single-tile mutation:
// load the row and, unless wantKind is "", guard its kind. No version is
// consulted, because these writes are last-writer-wins.
func (s *Store) loadForWrite(ctx context.Context, tx *sql.Tx, tileID int64, wantKind string, wrongKindErr error) (*gridwellv1.Tile, error) {
	n, err := s.loadTile(ctx, tx, tileID)
	if err != nil {
		return nil, err
	}
	if wantKind != "" && n.Kind != wantKind {
		return nil, wrongKindErr
	}
	return n, nil
}

// emitTileChanged reloads tileID and appends a TileChanged event for it. It is
// the tail of every write that is not a content edit, since none of those may
// bump the version; the event still carries the whole tile, so a capture
// reaches every client as last-writer-wins state. Content writers go through
// finishContentEdit.
func (s *Store) emitTileChanged(ctx context.Context, tx *sql.Tx, tileID int64, events *[]*gridwellv1.Event) (*gridwellv1.Tile, error) {
	out, err := s.loadTile(ctx, tx, tileID)
	if err != nil {
		return nil, err
	}
	*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: out}}})
	return out, nil
}

// finishContentEdit is the coda for a user content mutation: bump the version,
// then publish through emitTileChanged. Two named helpers rather than a
// per-method open-coded bump is what keeps "a content edit bumps, everything
// else does not" from drifting; version_rule_test.go pins the table.
func (s *Store) finishContentEdit(ctx context.Context, tx *sql.Tx, tileID int64, events *[]*gridwellv1.Event) (*gridwellv1.Tile, error) {
	if err := bumpTileVersion(ctx, tx, tileID); err != nil {
		return nil, err
	}
	return s.emitTileChanged(ctx, tx, tileID, events)
}

// createTile is the shared scaffolding for the Create* methods. The insert
// closure receives the canonical gridID and the current timestamp, inserts the
// row and returns its id.
func (s *Store) createTile(
	ctx context.Context,
	gridIDStr string, x, y, w, h int64,
	insert func(tx *sql.Tx, gridID, now int64) (tileID int64, err error),
) (*gridwellv1.Tile, error) {
	gridID, err := parseID(gridIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		// grid_id is authoritative; no descent path is on the wire.
		if _, err := s.loadGrid(ctx, tx, gridID); err != nil {
			return fmt.Errorf("%w: grid %d: %v", ErrInvalidArgument, gridID, err)
		}
		gid := gridID

		over, err := overlapsExisting(ctx, tx, gid, x, y, w, h)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		tileID, err := insert(tx, gid, s.now().Unix())
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

// CreateWell creates a well owning a fresh empty child grid, with zero framing,
// which means never visited. Label is stored as alt_text; a well has no content
// to derive an alt from, so this is that column's only writer for wells.
func (s *Store) CreateWell(ctx context.Context, gridID string, x, y, w, h int64, label string) (*gridwellv1.Tile, error) {
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`,
				now, now)
			if err != nil {
				return 0, fmt.Errorf("insert child grid: %w", err)
			}
			childGridID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			res, err = tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					view_cx, view_cy, view_zoom, child_grid_id, alt_text,
					created_at, updated_at)
				VALUES (?, 'well', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)`,
				gid, x, y, w, h, childGridID, label, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert well: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateExitWell creates a well that is a doorway onto an existing grid rather
// than the owner of one. It allocates no child grid and holds no refcount: the
// child is owned by whoever created it and named by a qualified "<uuid>/<id>"
// string, so deleting the well removes only the reference. view carries the
// source's framing when the well is a cross-plugin clone of a framed one, so
// the link previews and descends where the source did; zero zoom means never
// visited.
func (s *Store) CreateExitWell(ctx context.Context, gridID string, x, y, w, h int64, childGridID, alt string, view rpc.Framing) (*gridwellv1.Tile, error) {
	if childGridID == "" {
		return nil, fmt.Errorf("%w: child_grid_id required", ErrInvalidArgument)
	}
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					view_cx, view_cy, view_zoom, child_grid_id, alt_text,
					created_at, updated_at)
				VALUES (?, 'well', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				gid, x, y, w, h, view.Cx, view.Cy, view.Zoom, childGridID, alt, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert exit well: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateText creates a markdown text tile.
func (s *Store) CreateText(ctx context.Context, gridID string, x, y, w, h int64, data []byte) (*gridwellv1.Tile, error) {
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	hash := hashBytes(data)
	alt := doctype.AltFromSource(string(data))
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			blobID, err := s.putBlob(ctx, tx, hash, data, mediaMarkdown)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					blob_id, alt_text, created_at, updated_at)
				VALUES (?, 'text', ?, ?, ?, ?, ?, ?, ?, ?)`,
				gid, x, y, w, h, blobID, alt, now, now)
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

// insertURLRow is the single place the url INSERT lives, shared by CreateURL
// and CreateScratchURL so they cannot drift.
func insertURLRow(ctx context.Context, tx *sql.Tx, gridID, x, y, w, h int64, url string, now int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tiles (grid_id, kind, x, y, w, h,
			url_string, created_at, updated_at)
		VALUES (?, 'url', ?, ?, ?, ?, ?, ?, ?)`,
		gridID, x, y, w, h, url, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert url tile: %w", err)
	}
	return res.LastInsertId()
}

// CreateURL creates a url tile. An empty url is the legal unconfigured state:
// drop first, prompt on first descent, and the address arrives later through
// WriteContent's url arm.
func (s *Store) CreateURL(ctx context.Context, gridID string, x, y, w, h int64, url string) (*gridwellv1.Tile, error) {
	urlString := strings.TrimSpace(url)
	if urlString != "" && !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			return insertURLRow(ctx, tx, gid, x, y, w, h, urlString, now)
		})
}

// CreateScratchURL creates a url tile in the scratch grid: descending into a
// url without placing a tile. It runs no overlap check, because the scratch
// grid is never rendered and two visits may share a cell. The tile is
// otherwise normal and persistent. See ScratchGridID.
func (s *Store) CreateScratchURL(ctx context.Context, url string) (*gridwellv1.Tile, error) {
	urlString := strings.TrimSpace(url)
	if !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		return nil, err
	}
	gridID, err := parseID(scratch)
	if err != nil {
		return nil, err
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		now := s.now().Unix()
		tileID, err := insertURLRow(ctx, tx, gridID, 0, 0, 1, 1, urlString, now)
		if err != nil {
			return err
		}
		if err := s.bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// CreateScratchShell is CreateScratchURL's shell twin. Unlike a placed shell
// it is deleted on ascent, which kills the tmux session, so nothing persists.
func (s *Store) CreateScratchShell(ctx context.Context) (*gridwellv1.Tile, error) {
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		return nil, err
	}
	gridID, err := parseID(scratch)
	if err != nil {
		return nil, err
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		now := s.now().Unix()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (grid_id, kind, x, y, w, h,
				alt_text, created_at, updated_at)
			VALUES (?, 'shell', 0, 0, 1, 1, 'shell', ?, ?)`,
			gridID, now, now)
		if err != nil {
			return fmt.Errorf("insert scratch shell tile: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := s.bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// SetTextView updates a text tile's framed window and its rendered or text
// mode. Like SetFraming this is framing, not content: no claim, no bump.
func (s *Store) SetTextView(ctx context.Context, tileIDStr string, textX, textY, textW, textH int64, textMode string) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, rpc.KindText, ErrNotTextTile)
		if err != nil {
			return err
		}
		if n.LinkTargetId != "" {
			// A link row persists the framed window only: the CHECK keeps
			// text_mode NULL on a link, because framing is per-link local and
			// the mode is not.
			_, err := tx.ExecContext(ctx,
				`UPDATE tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, updated_at = ? WHERE id = ?`,
				textX, textY, textW, textH, s.now().Unix(), tileID)
			if err != nil {
				return err
			}
			out, err = s.emitTileChanged(ctx, tx, tileID, events)
			return err
		}
		var textModeArg any
		if textMode != "" {
			textModeArg = textMode
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ?, updated_at = ? WHERE id = ?`,
			textX, textY, textW, textH, textModeArg, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// DeleteTile is the user's discard gesture, in two stages. A tile on an
// ordinary grid moves into the trashcan's current-month subgrid, same id and
// same row, so links keep resolving; a second delete, on a tile already in the
// trash tree, destroys it and releases its blobs and its interior child grid.
// Scratch-grid tiles always delete for real. An exit well's qualified
// child_grid_id does not parse as a local grid id, so only the reference is
// dropped.
func (s *Store) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) error {
	tileID, err := parseID(req.TileId)
	if err != nil {
		return fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		t, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		srcGrid, err := parseID(t.GridId)
		if err != nil {
			return fmt.Errorf("tile %d: bad grid_id %q: %w", tileID, t.GridId, err)
		}
		bypass, err := s.deleteBypassesTrash(ctx, tx, srcGrid)
		if err != nil {
			return err
		}
		if !bypass {
			return s.moveTileToTrash(ctx, tx, events, t)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, tileID); err != nil {
			return err
		}
		childGridID, _ := strconv.ParseInt(t.ChildGridId, 10, 64)
		if err := s.decTileRefs(ctx, tx, t.Kind, childGridID, t.BlobId, t.PreviewBlobId); err != nil {
			return err
		}
		gridID, _ := parseID(t.GridId)
		if err := s.bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: t.GridId, TileId: t.Id}}})
		return nil
	})
}
