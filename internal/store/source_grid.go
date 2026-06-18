package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/internal/fssource"
	"github.com/josephburnett/gridwell/internal/procsource"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// procDisplayName returns a short human-readable label for a process,
// suitable as a tile label inside a process-grid. Prefers the kernel-
// reported Name from /proc/<pid>/status (matches `ps` default), falling
// back to the basename of the first cmdline arg when Name is empty
// (e.g. some sandboxed/renamed processes), then to "pid N" so callers
// always get a usable string they can stamp into alt_text — the client
// renders alt_text verbatim and shouldn't see an empty label for a
// real PID.
func procDisplayName(info procsource.Info) string {
	if info.Name != "" {
		return info.Name
	}
	if info.CmdLine != "" {
		first := strings.Fields(info.CmdLine)[0]
		if base := filepath.Base(first); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("pid %d", info.PID)
}

// FSReader is the surface fssource implements. Stubbable for tests.
type FSReader interface {
	Read(dir string) ([]fssource.Entry, error)
	MetadataMarkdown(e fssource.Entry) string
}

// ProcReader is the surface procsource implements. Stubbable for tests.
type ProcReader interface {
	Children(procRoot string, parentPID int64) ([]procsource.Info, error)
	Get(procRoot string, pid int64) (procsource.Info, error)
	MetadataMarkdown(info procsource.Info) string
}

// realFSReader / realProcReader bind the default package functions to
// the FSReader / ProcReader interfaces — production wiring.
type realFSReader struct{}

func (realFSReader) Read(dir string) ([]fssource.Entry, error) { return fssource.Read(dir) }
func (realFSReader) MetadataMarkdown(e fssource.Entry) string  { return fssource.MetadataMarkdown(e) }

type realProcReader struct{}

func (realProcReader) Children(root string, ppid int64) ([]procsource.Info, error) {
	return procsource.Children(root, ppid)
}
func (realProcReader) Get(root string, pid int64) (procsource.Info, error) {
	return procsource.Get(root, pid)
}
func (realProcReader) MetadataMarkdown(info procsource.Info) string {
	return procsource.MetadataMarkdown(info)
}

// SetSourceReaders overrides the readers used to reconcile fs/proc
// grids. Production uses the package defaults; tests pass stubs that
// return fixed entries.
func (s *Store) SetSourceReaders(fs FSReader, proc ProcReader, procRoot string) {
	if fs != nil {
		s.fsReader = fs
	}
	if proc != nil {
		s.procReader = proc
	}
	if procRoot != "" {
		s.procRoot = procRoot
	}
}

// autoGridWidth is the number of columns the auto-layout wraps at. New
// entries are placed row-major into the next free cell.
const autoGridWidth = 8

// reconcileSourceGrid brings the tile rows in a source-backed grid up to
// date with the host source. Called from GetGrid before tiles are
// returned. No-op for regular Gridwell grids.
func (s *Store) reconcileSourceGrid(ctx context.Context, g *rpc.Grid) error {
	switch g.SourceKind {
	case rpc.GridSourceFS:
		return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
			return s.reconcileFSGrid(ctx, tx, g, events)
		})
	case rpc.GridSourceProc:
		return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
			return s.reconcileProcGrid(ctx, tx, g, events)
		})
	}
	return nil
}

// reconcileFSGrid reads g.SourceID as a directory path and reconciles
// the grid's tile list against it. Subdirectories become file-well
// tiles; everything else becomes a text tile pointing at a synthesized
// metadata blob. Layout is sticky per source_key: existing tiles keep
// their positions, new tiles take the next free auto-grid cell. The
// grid version is only bumped if a real change happened — otherwise
// the SSE fan-out would fire on every read.
func (s *Store) reconcileFSGrid(ctx context.Context, tx *sql.Tx, g *rpc.Grid, events *[]rpc.Event) error {
	// (A mtime-based pre-check was tried first but WSL2 ext4 doesn't
	// advance dir mtime reliably on add/remove — even at nanosecond
	// resolution — so a stale match would hide real changes. We read
	// the directory every time and rely on the no-change short-circuit
	// below to avoid spurious version bumps.)
	entries, err := s.fsReader.Read(g.SourceID)
	if err != nil {
		// A directory that vanished underneath us is not an error: the
		// grid is just empty until the dir returns.
		return nil
	}
	existing, err := loadFSGridTilesByName(ctx, tx, g.ID)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	seen := make(map[string]bool, len(entries))
	layout := newLayoutTracker(existing)
	changed := false

	for _, e := range entries {
		seen[e.Name] = true
		want := desiredFSTileKind(e)
		cur, ok := existing[e.Name]
		if !ok {
			if err := s.insertFSGridTile(ctx, tx, g.ID, e, layout.next(), now, events); err != nil {
				return err
			}
			changed = true
			continue
		}
		// Existing tile: if its kind matches what the entry now needs,
		// keep it. If it shifted (e.g. file replaced by a dir of the
		// same name), drop the old and insert fresh.
		if cur.Kind != want {
			if err := s.deleteFSGridTile(ctx, tx, cur, events); err != nil {
				return err
			}
			if err := s.insertFSGridTile(ctx, tx, g.ID, e, position{cur.X, cur.Y}, now, events); err != nil {
				return err
			}
			changed = true
			continue
		}
		// V1: file tiles in fs-grids carry no content blob. Reading file
		// contents on every reconcile would re-hash and re-store blobs
		// for every directory entry on every GetGrid — too expensive for
		// the "list / contents" descent. Lazy content loading is a
		// follow-up; the structural tile is enough to render the swatch
		// and label.
		_ = cur
		_ = want
	}
	// Remove tiles whose underlying entry is gone.
	for name, cur := range existing {
		if seen[name] {
			continue
		}
		if err := s.deleteFSGridTile(ctx, tx, cur, events); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.bumpGridVersion(ctx, tx, g.ID)
}

// reconcileProcGrid mirrors reconcileFSGrid for the process tree.
// SourceID is the parent PID as a decimal string. Each direct child
// becomes a process-well tile; the parent itself surfaces as a synthetic
// "info" text tile at (0,0) so descent always shows what you are looking
// at.
func (s *Store) reconcileProcGrid(ctx context.Context, tx *sql.Tx, g *rpc.Grid, events *[]rpc.Event) error {
	parentPID, err := strconv.ParseInt(g.SourceID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid proc source_id %q: %w", g.SourceID, err)
	}
	infoSelf, infoErr := s.procReader.Get(s.procRoot, parentPID)
	children, err := s.procReader.Children(s.procRoot, parentPID)
	if err != nil {
		return nil
	}
	existing, err := loadFSGridTilesByName(ctx, tx, g.ID)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	seen := make(map[string]bool, len(children)+1)
	layout := newLayoutTracker(existing)
	changed := false

	// Synthetic info tile for the well's own PID. Naming it "@info"
	// (a name no real PID can collide with) keeps the same per-name
	// reconcile lookup that fs grids use. Body is live /proc metadata
	// rendered to markdown — refreshed on every reconcile, so each
	// descent into the proc-well sees current state (memory, cwd,
	// state). When the process has gone (infoErr != nil from Get) the
	// @info tile is left as-is until the parent well itself goes away.
	if infoErr == nil {
		seen["@info"] = true
		body := []byte(s.procReader.MetadataMarkdown(infoSelf))
		if cur, ok := existing["@info"]; ok {
			refreshed, err := s.refreshProcInfoBlob(ctx, tx, cur, body, events)
			if err != nil {
				return err
			}
			if refreshed {
				changed = true
			}
		} else {
			if err := s.insertProcInfoTile(ctx, tx, g.ID, layout.next(), body, now, events); err != nil {
				return err
			}
			changed = true
		}
	}

	for _, info := range children {
		name := strconv.FormatInt(info.PID, 10)
		seen[name] = true
		want := procDisplayName(info)
		if cur, ok := existing[name]; ok {
			// Existing tile: refresh alt_text if the live process name
			// differs from what's on disk. Catches tiles inserted before
			// alt_text was populated (older DBs), and the case where a
			// process renames itself between reconciles or a PID gets
			// reused for a different command.
			if cur.AltText != want {
				if err := s.updateTileAltText(ctx, tx, cur.ID, want, now, events); err != nil {
					return err
				}
				changed = true
			}
			continue
		}
		if err := s.insertProcChildTile(ctx, tx, g.ID, info, layout.next(), now, events); err != nil {
			return err
		}
		changed = true
	}
	for name, cur := range existing {
		if seen[name] {
			continue
		}
		if err := s.deleteFSGridTile(ctx, tx, cur, events); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.bumpGridVersion(ctx, tx, g.ID)
}

// desiredFSTileKind picks the tile kind for one fssource Entry: a
// directory becomes file-well, anything else becomes a text tile (the
// V1 universal file representation — descent renders metadata).
func desiredFSTileKind(e fssource.Entry) string {
	if e.Kind == fssource.KindDir {
		return rpc.KindFileWell
	}
	return rpc.KindText
}

// loadFSGridTilesByName returns the tiles in a grid keyed by source_key.
// Used only when reconciling source grids — regular grids don't set
// source_key.
func loadFSGridTilesByName(ctx context.Context, q gridReader, gridID int64) (map[string]*rpc.Tile, error) {
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

// emitInsertedTile resolves the row id from a just-executed INSERT, loads the
// tile, and appends a TileChanged event — the shared tail of every reconcile
// insert helper (fs file / fs sub-well / proc @info / proc child).
func (s *Store) emitInsertedTile(ctx context.Context, tx *sql.Tx, res sql.Result, events *[]rpc.Event) error {
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.emitTileChanged(ctx, tx, id, events)
	return err
}

func (s *Store) insertFSGridTile(ctx context.Context, tx *sql.Tx, gridID int64, e fssource.Entry, pos position, now int64, events *[]rpc.Event) error {
	objID := s.newID()
	if e.Kind == fssource.KindDir {
		childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceFS, e.AbsPath, now)
		if err != nil {
			return err
		}
		// alt_text duplicates e.Name today, but source_key is the
		// reconciler's dedup key (identity) and alt_text is the
		// client-rendered label — keep the roles separate.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id, fs_path, source_key,
				alt_text, created_at, updated_at)
			VALUES (?, ?, 'file-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?, ?)`,
			objID, gridID, pos.x, pos.y, childGridID, e.AbsPath, e.Name, e.Name, now, now)
		if err != nil {
			return fmt.Errorf("insert fs sub-well: %w", err)
		}
		return s.emitInsertedTile(ctx, tx, res, events)
	}
	// File tile: no blob until the user actually descends and asks for
	// content. blob_id stays NULL — the relaxed text-kind CHECK allows
	// it.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
			source_key, alt_text, created_at, updated_at)
		VALUES (?, ?, 'text', ?, ?, 1, 1, ?, ?, ?, ?)`,
		objID, gridID, pos.x, pos.y, e.Name, e.Name, now, now)
	if err != nil {
		return fmt.Errorf("insert fs file tile: %w", err)
	}
	return s.emitInsertedTile(ctx, tx, res, events)
}

// updateTileAltText overwrites the alt_text on one tile and emits a
// TileChanged event. Used by the proc reconciler when an existing tile's
// stored display name is stale (e.g. the row pre-dates alt_text being
// populated, or a process renamed itself between reconciles).
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

// deleteFSGridTile drops a tile that no longer has a backing entry,
// decrementing the child grid or blob refcount as appropriate. Mirrors
// the kind-aware cleanup that DeleteTile does for regular tiles.
func (s *Store) deleteFSGridTile(ctx context.Context, tx *sql.Tx, t *rpc.Tile, events *[]rpc.Event) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+schemaOf(t.ID)+`tiles WHERE id = ?`, t.ID); err != nil {
		return err
	}
	// Release every reference this row held through the single table-driven
	// map (child grid / text blob / preview blob). This used to hand-roll
	// per-kind decrements and ignored preview_blob_id, drifting from the
	// tileRefs source of truth that fork/clone/GC use.
	if err := s.decTileRefs(ctx, tx, t.Kind, t.ChildGridID, t.BlobID, t.PreviewBlobID); err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
	return nil
}

// insertProcInfoTile inserts the synthetic "@info" tile for a
// process-well's own PID with `body` as its content blob. The blob is
// content-hashed and refcounted exactly like a user-created text tile,
// so future reconciles that produce identical markdown dedupe to the
// same blob row.
func (s *Store) insertProcInfoTile(ctx context.Context, tx *sql.Tx, gridID int64, pos position, body []byte, now int64, events *[]rpc.Event) error {
	hash := hashBytes(body)
	blobID, err := s.putBlob(ctx, tx, schemaOf(gridID), hash, body, mediaMarkdown)
	if err != nil {
		return err
	}
	objID := s.newID()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
			blob_id, source_key, alt_text, created_at, updated_at)
		VALUES (?, ?, 'text', ?, ?, 1, 1, ?, '@info', ?, ?, ?)`,
		objID, gridID, pos.x, pos.y, blobID, rpc.AltInfo, now, now)
	if err != nil {
		return fmt.Errorf("insert proc info tile: %w", err)
	}
	if err := s.incBlobRefcount(ctx, tx, blobID); err != nil {
		return err
	}
	return s.emitInsertedTile(ctx, tx, res, events)
}

// refreshProcInfoBlob rebinds an existing @info tile to a new content
// blob when the live /proc data has changed. Returns true if the tile
// was rewritten (caller bumps the grid version) or false if the
// markdown was byte-identical to what was already stored.
//
// Idempotent: identical input hashes resolve to the same blob row via
// putBlob, so the dec-then-inc dance is skipped when nothing changed.
func (s *Store) refreshProcInfoBlob(ctx context.Context, tx *sql.Tx, cur *rpc.Tile, body []byte, events *[]rpc.Event) (bool, error) {
	_, changed, err := s.swapTileBlob(ctx, tx, cur.ID, "blob_id", body, mediaMarkdown)
	if err != nil {
		return false, fmt.Errorf("refresh proc info blob: %w", err)
	}
	if !changed {
		return false, nil
	}
	if err := bumpTileVersion(ctx, tx, cur.ID); err != nil {
		return false, err
	}
	t, err := s.loadTile(ctx, tx, cur.ID)
	if err != nil {
		return false, err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
	return true, nil
}

func (s *Store) insertProcChildTile(ctx context.Context, tx *sql.Tx, gridID int64, info procsource.Info, pos position, now int64, events *[]rpc.Event) error {
	pidStr := strconv.FormatInt(info.PID, 10)
	childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceProc, pidStr, now)
	if err != nil {
		return err
	}
	objID := s.newID()
	// alt_text carries the process name for client-side labeling. source_key
	// stays the PID string because the reconciler dedupes by it and PIDs
	// are the only per-parent unique identifier (two children can share
	// Name="bash").
	res, err := tx.ExecContext(ctx, `
		INSERT INTO `+schemaOf(gridID)+`tiles (object_id, grid_id, kind, x, y, w, h,
			view_x, view_y, view_zoom, child_grid_id, pid, source_key,
			alt_text, created_at, updated_at)
		VALUES (?, ?, 'process-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?, ?)`,
		objID, gridID, pos.x, pos.y, childGridID, info.PID, pidStr, procDisplayName(info), now, now)
	if err != nil {
		return fmt.Errorf("insert proc child tile: %w", err)
	}
	return s.emitInsertedTile(ctx, tx, res, events)
}
