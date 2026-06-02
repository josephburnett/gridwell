package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/internal/fssource"
	"github.com/josephburnett/gridwell/internal/procsource"
	"github.com/josephburnett/gridwell/internal/rpc"
)

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
// metadata blob. Layout is sticky per fs_name: existing tiles keep
// their positions, new tiles take the next free auto-grid cell.
func (s *Store) reconcileFSGrid(ctx context.Context, tx *sql.Tx, g *rpc.Grid, events *[]rpc.Event) error {
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

	for _, e := range entries {
		seen[e.Name] = true
		want := desiredFSTileKind(e)
		cur, ok := existing[e.Name]
		if !ok {
			if err := s.insertFSGridTile(ctx, tx, g.ID, e, layout.next(), now, events); err != nil {
				return err
			}
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
			continue
		}
		// File tile: refresh metadata blob if it changed.
		if want == rpc.KindText {
			if err := s.refreshFSFileBlob(ctx, tx, cur, e, now, events); err != nil {
				return err
			}
		}
	}
	// Remove tiles whose underlying entry is gone.
	for name, cur := range existing {
		if seen[name] {
			continue
		}
		if err := s.deleteFSGridTile(ctx, tx, cur, events); err != nil {
			return err
		}
	}
	return bumpGridVersion(ctx, tx, g.ID)
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

	// Synthetic info tile for the well's own PID. Naming it "@info"
	// (a name no real PID can collide with) keeps the same per-name
	// reconcile lookup that fs grids use.
	if infoErr == nil {
		seen["@info"] = true
		if err := s.upsertProcInfoTile(ctx, tx, g.ID, infoSelf, existing["@info"], layout, now, events); err != nil {
			return err
		}
	}

	for _, info := range children {
		name := strconv.FormatInt(info.PID, 10)
		seen[name] = true
		cur, ok := existing[name]
		if !ok {
			if err := s.insertProcChildTile(ctx, tx, g.ID, info, layout.next(), now, events); err != nil {
				return err
			}
			continue
		}
		_ = cur // process-well tiles carry no per-reconcile state to refresh
	}
	for name, cur := range existing {
		if seen[name] {
			continue
		}
		if err := s.deleteFSGridTile(ctx, tx, cur, events); err != nil {
			return err
		}
	}
	return bumpGridVersion(ctx, tx, g.ID)
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

// loadFSGridTilesByName returns the tiles in a grid keyed by fs_name.
// Used only when reconciling source grids — regular grids don't set
// fs_name.
func loadFSGridTilesByName(ctx context.Context, q gridReader, gridID int64) (map[string]*rpc.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ?`, gridID)
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
		if t.FSName == "" {
			continue
		}
		out[t.FSName] = t
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

func (s *Store) insertFSGridTile(ctx context.Context, tx *sql.Tx, gridID int64, e fssource.Entry, pos position, now int64, events *[]rpc.Event) error {
	objID := s.newID()
	if e.Kind == fssource.KindDir {
		childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceFS, e.AbsPath, now)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id, fs_path, fs_name,
				created_at, updated_at)
			VALUES (?, ?, 'file-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?)`,
			objID, gridID, pos.x, pos.y, childGridID, e.AbsPath, e.Name, now, now)
		if err != nil {
			return fmt.Errorf("insert fs sub-well: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		t, err := s.loadTile(ctx, tx, id)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
		return nil
	}
	blobID, err := s.putMetadataBlob(ctx, tx, s.fsReader.MetadataMarkdown(e))
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
			blob_id, fs_name, created_at, updated_at)
		VALUES (?, ?, 'text', ?, ?, 1, 1, ?, ?, ?, ?)`,
		objID, gridID, pos.x, pos.y, blobID, e.Name, now, now)
	if err != nil {
		return fmt.Errorf("insert fs file tile: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := s.incBlobRefcount(ctx, tx, blobID); err != nil {
		return err
	}
	t, err := s.loadTile(ctx, tx, id)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, t.ID); err != nil {
		return err
	}
	if (t.Kind == rpc.KindFileWell || t.Kind == rpc.KindProcessWell || t.Kind == rpc.KindWell) && t.ChildGridID != 0 {
		if err := s.decRefcount(ctx, tx, t.ChildGridID); err != nil {
			return err
		}
	}
	if t.Kind == rpc.KindText && t.BlobID != 0 {
		if err := s.decBlobRefcount(ctx, tx, t.BlobID); err != nil {
			return err
		}
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
	return nil
}

// refreshFSFileBlob points the tile at the current metadata blob; the
// blob system dedupes by hash so unchanged files reuse the same row.
func (s *Store) refreshFSFileBlob(ctx context.Context, tx *sql.Tx, cur *rpc.Tile, e fssource.Entry, now int64, events *[]rpc.Event) error {
	newBlobID, err := s.putMetadataBlob(ctx, tx, s.fsReader.MetadataMarkdown(e))
	if err != nil {
		return err
	}
	if newBlobID == cur.BlobID {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tiles SET blob_id = ?, updated_at = ? WHERE id = ?`,
		newBlobID, now, cur.ID); err != nil {
		return err
	}
	if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
		return err
	}
	if cur.BlobID != 0 {
		if err := s.decBlobRefcount(ctx, tx, cur.BlobID); err != nil {
			return err
		}
	}
	if err := bumpTileVersion(ctx, tx, cur.ID); err != nil {
		return err
	}
	t, err := s.loadTile(ctx, tx, cur.ID)
	if err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
	return nil
}

// upsertProcInfoTile inserts or refreshes the synthetic "@info" tile for
// a process-well's own PID. Acts like a one-off file tile with name
// "@info" and content = procsource.MetadataMarkdown.
func (s *Store) upsertProcInfoTile(ctx context.Context, tx *sql.Tx, gridID int64, info procsource.Info, cur *rpc.Tile, layout *layoutTracker, now int64, events *[]rpc.Event) error {
	if cur == nil {
		blobID, err := s.putMetadataBlob(ctx, tx, s.procReader.MetadataMarkdown(info))
		if err != nil {
			return err
		}
		objID := s.newID()
		pos := layout.next()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				blob_id, fs_name, created_at, updated_at)
			VALUES (?, ?, 'text', ?, ?, 2, 1, ?, '@info', ?, ?)`,
			objID, gridID, pos.x, pos.y, blobID, now, now)
		if err != nil {
			return fmt.Errorf("insert proc info tile: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := s.incBlobRefcount(ctx, tx, blobID); err != nil {
			return err
		}
		t, err := s.loadTile(ctx, tx, id)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
		return nil
	}
	newBlobID, err := s.putMetadataBlob(ctx, tx, s.procReader.MetadataMarkdown(info))
	if err != nil {
		return err
	}
	if newBlobID == cur.BlobID {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tiles SET blob_id = ?, updated_at = ? WHERE id = ?`,
		newBlobID, now, cur.ID); err != nil {
		return err
	}
	if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
		return err
	}
	if cur.BlobID != 0 {
		if err := s.decBlobRefcount(ctx, tx, cur.BlobID); err != nil {
			return err
		}
	}
	return bumpTileVersion(ctx, tx, cur.ID)
}

func (s *Store) insertProcChildTile(ctx context.Context, tx *sql.Tx, gridID int64, info procsource.Info, pos position, now int64, events *[]rpc.Event) error {
	pidStr := strconv.FormatInt(info.PID, 10)
	childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceProc, pidStr, now)
	if err != nil {
		return err
	}
	objID := s.newID()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
			view_x, view_y, view_zoom, child_grid_id, pid, fs_name,
			created_at, updated_at)
		VALUES (?, ?, 'process-well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?, ?)`,
		objID, gridID, pos.x, pos.y, childGridID, info.PID, pidStr, now, now)
	if err != nil {
		return fmt.Errorf("insert proc child tile: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	t, err := s.loadTile(ctx, tx, id)
	if err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *t}})
	return nil
}

// putMetadataBlob stores a synthesized metadata-markdown blob and
// returns its id. Dedupe falls out of the hash-keyed blobs table; an
// unchanged Entry produces the same hash and the same blob id.
func (s *Store) putMetadataBlob(ctx context.Context, tx *sql.Tx, md string) (int64, error) {
	data := []byte(md)
	return putBlob(ctx, tx, hashBytes(data), data)
}

