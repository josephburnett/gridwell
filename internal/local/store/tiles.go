package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
)

// urlSchemeAllowed reports whether u is one of the schemes accepted by
// URL tiles. Only http and https.
func urlSchemeAllowed(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// MaxBlobBytes caps a single uploaded text-tile blob size.
const MaxBlobBytes = 16 * 1024 * 1024

// claimContentVersion loads a tile and verifies the caller's content claim
// against the row. It is the store's only optimistic-concurrency check, and
// its only callers are the writes that change the user's content bytes:
// WriteContent's text and url arms, and RenameTile. "version is the claim for
// content and nothing else" is enforced by who can reach this function.
func (s *Store) claimContentVersion(ctx context.Context, q gridReader, tileID, claimed int64) (*rpc.Tile, error) {
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

// loadForWrite is the shared preamble for an unclaimed single-tile mutation —
// framing, a capture, a layout write: load the row and optionally guard its
// kind. wantKind == "" skips the kind guard, for callers with a multi-kind
// rule that check the returned tile themselves. No version is consulted:
// these writes are last-writer-wins, so there is nothing here for a racing
// capture to conflict with.
func (s *Store) loadForWrite(ctx context.Context, tx *sql.Tx, tileID int64, wantKind string, wrongKindErr error) (*rpc.Tile, error) {
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
// the shared tail of every store write that publishes a tile, and the tail of
// every write that is not a content edit — framing, an automatic capture, a
// layout move — since none of those may bump the version. The event carries
// the whole tile, so a capture still reaches every client; it just arrives as
// last-writer-wins state instead of a new claim. Content writers go through
// finishContentEdit.
func (s *Store) emitTileChanged(ctx context.Context, tx *sql.Tx, tileID int64, events *[]rpc.Event) (*rpc.Tile, error) {
	out, err := s.loadTile(ctx, tx, tileID)
	if err != nil {
		return nil, err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
	return out, nil
}

// finishContentEdit is the coda for a user content mutation: bump the tile's
// version, the optimistic-concurrency key, then publish it through
// emitTileChanged. Keeping "a user content edit bumps, everything else does
// not" as a choice between two named helpers, rather than a per-method
// open-coded bump a new mutation can forget or wrongly add, is what keeps
// that invariant from drifting. Its callers are exactly the content writes,
// and version_rule_test.go pins the whole table.
func (s *Store) finishContentEdit(ctx context.Context, tx *sql.Tx, tileID int64, events *[]rpc.Event) (*rpc.Tile, error) {
	if err := bumpTileVersion(ctx, tx, tileID); err != nil {
		return nil, err
	}
	return s.emitTileChanged(ctx, tx, tileID, events)
}

// createTile is the shared scaffolding for the Create* methods: sequence
// validation, overlap check, kind-specific insert, grid version bump, load,
// publish. The insert closure receives the canonical gridID and the current
// unix timestamp; it inserts the tile row and returns its id.
func (s *Store) createTile(
	ctx context.Context,
	gridIDStr string, x, y, w, h int64,
	insert func(tx *sql.Tx, gridID, now int64) (tileID int64, err error),
) (*rpc.Tile, error) {
	gridID, err := parseID(gridIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		// grid_id is authoritative; no descent path is on the wire. The load
		// refuses a create into a grid that does not exist.
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

// CreateWell creates a new well at (x,y) with footprint (w,h) inside
// req.GridID. The child grid is created empty with no framing on the well:
// view_cx, view_cy, and view_zoom are all zero, which means never visited.
// Label, when set, is stored as the well's alt_text, the user-given name of
// the grid. Wells have no content to derive an alt from, so this is alt's
// only writer.
func (s *Store) CreateWell(ctx context.Context, req *rpc.CreateWellRequest) (*rpc.Tile, error) {
	return s.createTile(ctx, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64) (int64, error) {
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
				gridID, req.X, req.Y, req.W, req.H, childGridID, req.Label, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert well: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateExitWell creates a well tile whose child grid lives in a different
// plugin, such as a file well or a process well. Unlike CreateWell it
// allocates no interior child grid and holds no refcount on the child: the
// child grid is owned by the destination plugin and named by a qualified
// "<uuid>/<id>" string. Deleting the well removes only the reference, never
// the backing directory or process, which is a separate gesture on a tile
// inside the grid. view carries the source's framing when the exit well is a
// cross-plugin clone of a framed well, so the link previews and descends
// exactly where the source did. A zero zoom means never visited, so the
// default view.
func (s *Store) CreateExitWell(ctx context.Context, gridID string, x, y, w, h int64, childGridID, alt string, view rpc.Framing) (*rpc.Tile, error) {
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
func (s *Store) CreateText(ctx context.Context, req *rpc.CreateTextRequest) (*rpc.Tile, error) {
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	hash := hashBytes(req.Data)
	alt := doctype.AltFromSource(string(req.Data))
	return s.createTile(ctx, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64) (int64, error) {
			blobID, err := s.putBlob(ctx, tx, hash, req.Data, mediaMarkdown)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					blob_id, alt_text, created_at, updated_at)
				VALUES (?, 'text', ?, ?, ?, ?, ?, ?, ?, ?)`,
				gridID, req.X, req.Y, req.W, req.H, blobID, alt, now, now)
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

// insertURLRow inserts one url tile row and returns its id. It is the single
// place the url INSERT lives, shared by the on-grid CreateURL and the
// off-grid CreateScratchURL so they cannot drift.
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

// CreateURL creates a url tile pointing at the given URL. An empty url is the
// legal unconfigured state — drop first, prompt on first descent — and the
// address arrives later as the tile's content, through WriteContent's url
// arm.
func (s *Store) CreateURL(ctx context.Context, req *rpc.CreateURLRequest) (*rpc.Tile, error) {
	urlString := strings.TrimSpace(req.URL)
	if urlString != "" && !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	return s.createTile(ctx, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64) (int64, error) {
			return insertURLRow(ctx, tx, gridID, req.X, req.Y, req.W, req.H, urlString, now)
		})
}

// CreateScratchURL creates an ephemeral url tile in the scratch grid, off any
// visible grid, and returns it: the store side of descending into a url
// without placing a tile. Unlike CreateURL it takes no descent path and runs
// no overlap check, because the scratch grid is never rendered, so a tile's
// position there is meaningless and two visits may share a cell. The result
// is otherwise a normal, persistent url tile — durable visited-url history,
// and a resolvable deep link — it just lives off-grid. See ScratchGridID.
func (s *Store) CreateScratchURL(ctx context.Context, url string) (*rpc.Tile, error) {
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
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
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

// CreateScratchShell creates an ephemeral shell tile in the scratch grid: the
// shell twin of CreateScratchURL, off any visible grid, with no descent path
// and no overlap check. Unlike a placed shell it is deleted on ascent — the
// client drives that, and the delete kills the tmux session — so nothing
// persists.
func (s *Store) CreateScratchShell(ctx context.Context) (*rpc.Tile, error) {
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		return nil, err
	}
	gridID, err := parseID(scratch)
	if err != nil {
		return nil, err
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
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

// SetTextView updates a text tile's framed-document window and its rendered
// or text mode. Like SetFraming this is framing, not content: an in-place
// write that carries no version claim and does not bump the tile version.
func (s *Store) SetTextView(ctx context.Context, req *rpc.SetTextViewRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, rpc.KindText, ErrNotTextTile)
		if err != nil {
			return err
		}
		if n.LinkTargetID != "" {
			// A link row persists the framed window only: the CHECK keeps
			// text_mode NULL on a link, because framing is per-link local and
			// the mode is not. Writing it would fail the whole framing save.
			_, err := tx.ExecContext(ctx,
				`UPDATE tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, updated_at = ? WHERE id = ?`,
				req.TextX, req.TextY, req.TextW, req.TextH, s.now().Unix(), tileID)
			if err != nil {
				return err
			}
			out, err = s.emitTileChanged(ctx, tx, tileID, events)
			return err
		}
		var textModeArg any
		if req.TextMode != "" {
			textModeArg = req.TextMode
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ?, updated_at = ? WHERE id = ?`,
			req.TextX, req.TextY, req.TextW, req.TextH, textModeArg, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// DeleteTile is the user's discard gesture, in two stages. A tile on an
// ordinary grid moves into the trashcan's current-month subgrid — same id,
// same row, and links keep resolving, because it moved rather than died — and
// only a tile already inside the trash tree, on the second delete, is
// destroyed for real, releasing the references it held: its blob, its preview
// blob, and, for an interior well, its child grid. Scratch-grid tiles always
// delete for real. An exit well carries a qualified "<uuid>/<id>"
// child_grid_id that does not parse as a local grid id, so no local child is
// collected and only the reference is dropped. Tiles inside a plugin's grids
// are deleted by that plugin, which the server routes to.
func (s *Store) DeleteTile(ctx context.Context, req *rpc.DeleteTileRequest) error {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		srcGrid, err := parseID(t.GridID)
		if err != nil {
			return fmt.Errorf("tile %d: bad grid_id %q: %w", tileID, t.GridID, err)
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
		childGridID, _ := strconv.ParseInt(t.ChildGridID, 10, 64)
		if err := s.decTileRefs(ctx, tx, t.Kind, childGridID, t.BlobID, t.PreviewBlobID); err != nil {
			return err
		}
		gridID, _ := parseID(t.GridID)
		if err := s.bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
		return nil
	})
}
