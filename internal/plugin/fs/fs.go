// Package fs implements a Gridwell plugin that projects a host directory tree.
// Each plugin instance has its own SQLite DB that maps directory paths to
// stable integer grid IDs and file/dir names to stable integer tile IDs.
// Positions (x,y,w,h) and well view framing are stored in the plugin DB so
// they survive restarts.
package fs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/fssource"
	"github.com/josephburnett/gridwell/internal/plugin/griddb"
	_ "modernc.org/sqlite"
)

const autoGridWidth = 8

// fsLabelCol is the tiles-table column holding a tile's display label for the
// fs plugin (the directory entry name). Passed to the shared griddb helpers.
const fsLabelCol = "name"

// Host is the destructive side-effect surface. Injected so tests never rm
// anything on disk. Production wires osHost; tests wire recordHost.
type Host interface {
	Remove(path string) error
	RemoveAll(path string) error
}

type osHost struct{}

func (osHost) Remove(p string) error    { return os.Remove(p) }
func (osHost) RemoveAll(p string) error { return os.RemoveAll(p) }

// Plugin implements gridwellv1.GridwellServer for a filesystem source.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	db   *sql.DB
	host Host
}

// Open opens (or creates) the plugin SQLite DB at dbPath. A nil host uses
// plain os.Remove/os.RemoveAll.
func Open(dbPath string, host Host) (*Plugin, error) {
	if host == nil {
		host = osHost{}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("fs plugin open %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("fs plugin pragmas: %w", err)
	}
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Plugin{db: db, host: host}, nil
}

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS grids (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    path      TEXT NOT NULL UNIQUE,
    view_x    INTEGER NOT NULL DEFAULT 0,
    view_y    INTEGER NOT NULL DEFAULT 0,
    view_zoom REAL NOT NULL DEFAULT 1.0
);
CREATE TABLE IF NOT EXISTS tiles (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('well','text')),
    x             INTEGER NOT NULL DEFAULT 0,
    y             INTEGER NOT NULL DEFAULT 0,
    w             INTEGER NOT NULL DEFAULT 1,
    h             INTEGER NOT NULL DEFAULT 1,
    child_grid_id INTEGER,
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 1.0,
    UNIQUE (grid_id, name)
);`)
	return err
}

// Close closes the underlying database.
func (p *Plugin) Close() error { return p.db.Close() }

// NewFactory returns a ServerFactory for the "fs" kind.
// config["db_file"] is the path to the plugin's SQLite DB.
func NewFactory(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
	dbPath := cfg.Config["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("fs plugin %q: db_file config key required", cfg.Name)
	}
	return Open(dbPath, nil)
}

// Info returns the static plugin descriptor.
func (p *Plugin) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{
		Kind:          "fs",
		DisplayName:   "files",
		SchemaVersion: 1,
	}, nil
}

// Attach turns config["path"] into a root grid in the plugin's namespace.
func (p *Plugin) Attach(_ context.Context, req *gridwellv1.AttachRequest) (*gridwellv1.AttachResponse, error) {
	path := filepath.Clean(req.Config["path"])
	if path == "" || path == "." {
		return nil, fmt.Errorf("fs plugin: Attach requires a non-empty path")
	}
	gridID, err := p.getOrCreateGrid(path)
	if err != nil {
		return nil, err
	}
	label := filepath.Base(path)
	if label == "/" || label == "." {
		label = "files"
	}
	return &gridwellv1.AttachResponse{
		RootGridId: strconv.FormatInt(gridID, 10),
		Label:      label,
		Caps:       &gridwellv1.PluginCaps{},
		HasSession: false,
	}, nil
}

// Detach is a no-op for the fs plugin.
func (p *Plugin) Detach(_ context.Context, _ *gridwellv1.DetachRequest) (*gridwellv1.DetachResponse, error) {
	return &gridwellv1.DetachResponse{}, nil
}

// GetGrid reads the directory for the given grid_id, reconciles tile rows
// against the current directory contents, and returns the resulting tiles.
// Missing directories return an empty grid without error.
func (p *Plugin) GetGrid(_ context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	gridID, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fs GetGrid: invalid grid_id %q", req.GridId)
	}

	path, err := p.gridPath(gridID)
	if err != nil {
		return nil, fmt.Errorf("fs GetGrid %d: %w", gridID, err)
	}

	entries, readErr := fssource.Read(path)
	// A missing/unreadable directory is not an error — return empty authoritative.
	if readErr != nil {
		entries = nil
	}

	if err := p.reconcileTiles(gridID, path, entries); err != nil {
		return nil, err
	}

	tiles, err := griddb.LoadTiles(p.db, fsLabelCol, gridID)
	if err != nil {
		return nil, err
	}

	grid := &gridwellv1.Grid{Id: req.GridId, SourceKind: "fs", SourceId: path}
	return &gridwellv1.GetGridResponse{Grid: grid, Tiles: tiles}, nil
}

// MoveTile repositions a tile within its directory grid and persists the new
// position so it survives the next GetGrid and a restart.
func (p *Plugin) MoveTile(_ context.Context, req *gridwellv1.MoveTileRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplyMove(p.db, fsLabelCol, req)
}

// ResizeTile persists a new footprint for a file/dir tile.
func (p *Plugin) ResizeTile(_ context.Context, req *gridwellv1.ResizeTileRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplyResize(p.db, fsLabelCol, req)
}

// SetWellView persists a directory well's preview framing so descent and
// ascent restore the same view.
func (p *Plugin) SetWellView(_ context.Context, req *gridwellv1.SetWellViewRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplySetWellView(p.db, fsLabelCol, req)
}

// GetTileContent returns the descent body for a file tile: a small markdown
// summary of the file's metadata (the same body the legacy source reconciler
// produced). Directories and unreadable paths return empty content rather than
// an error.
func (p *Plugin) GetTileContent(_ context.Context, req *gridwellv1.GetTileContentRequest) (*gridwellv1.GetTileContentResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fs GetTileContent: invalid tile_id %q", req.TileId)
	}
	var gridID int64
	var name, kind string
	err = p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind)
	if err == sql.ErrNoRows {
		return &gridwellv1.GetTileContentResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	if kind != "text" {
		return &gridwellv1.GetTileContentResponse{}, nil
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return nil, err
	}
	entry, err := fssource.Stat(filepath.Join(dirPath, name))
	if err != nil {
		return &gridwellv1.GetTileContentResponse{}, nil
	}
	return &gridwellv1.GetTileContentResponse{
		Data:      []byte(fssource.MetadataMarkdown(entry)),
		MediaType: "text/markdown",
	}, nil
}

// Probe checks whether the tile at tile_id still has its backing path on disk.
func (p *Plugin) Probe(_ context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	var gridID int64
	var name string
	err = p.db.QueryRow(`SELECT t.grid_id, t.name FROM tiles t WHERE t.id = ?`, tileID).Scan(&gridID, &name)
	if err == sql.ErrNoRows {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	fullPath := filepath.Join(dirPath, name)
	_, statErr := os.Lstat(fullPath)
	switch {
	case statErr == nil:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case os.IsNotExist(statErr):
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// DeleteTile removes the file or directory from disk (via Host), then drops
// the tile row from the plugin DB.
func (p *Plugin) DeleteTile(_ context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	var gridID int64
	var name, kind string
	err = p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind)
	if err == sql.ErrNoRows {
		return &gridwellv1.DeleteTileResponse{}, nil // already gone
	}
	if err != nil {
		return nil, err
	}

	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return nil, err
	}
	fullPath := filepath.Join(dirPath, name)

	info, statErr := os.Lstat(fullPath)
	if statErr == nil {
		if info.IsDir() {
			if err := p.host.RemoveAll(fullPath); err != nil {
				return nil, fmt.Errorf("fs DeleteTile RemoveAll %s: %w", fullPath, err)
			}
		} else {
			if err := p.host.Remove(fullPath); err != nil {
				return nil, fmt.Errorf("fs DeleteTile Remove %s: %w", fullPath, err)
			}
		}
	}
	// Remove the tile row and any child grid it pointed at.
	if _, err := p.db.Exec(`DELETE FROM tiles WHERE id = ?`, tileID); err != nil {
		return nil, err
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (p *Plugin) getOrCreateGrid(path string) (int64, error) {
	var id int64
	err := p.db.QueryRow(`SELECT id FROM grids WHERE path = ?`, path).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := p.db.Exec(`INSERT INTO grids (path) VALUES (?)`, path)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (p *Plugin) gridPath(gridID int64) (string, error) {
	var path string
	err := p.db.QueryRow(`SELECT path FROM grids WHERE id = ?`, gridID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("grid %d not found", gridID)
	}
	return path, err
}

// reconcileTiles upserts tile rows for current entries and deletes rows for
// names no longer present on disk.
func (p *Plugin) reconcileTiles(gridID int64, dirPath string, entries []fssource.Entry) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load existing tile rows (name → id).
	rows, err := tx.Query(`SELECT id, name, x, y FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type existing struct{ id, x, y int64 }
	existingByName := map[string]existing{}
	for rows.Next() {
		var id, x, y int64
		var name string
		if err := rows.Scan(&id, &name, &x, &y); err != nil {
			rows.Close()
			return err
		}
		existingByName[name] = existing{id, x, y}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Build occupied-cell set for auto-layout.
	occupied := map[[2]int64]bool{}
	for _, e := range existingByName {
		occupied[[2]int64{e.x, e.y}] = true
	}
	nextCell := func() (int64, int64) {
		var cx, cy int64
		for {
			if !occupied[[2]int64{cx, cy}] {
				occupied[[2]int64{cx, cy}] = true
				return cx, cy
			}
			cx++
			if cx >= autoGridWidth {
				cx = 0
				cy++
			}
		}
	}

	// Upsert entries.
	presentNames := map[string]bool{}
	for _, e := range entries {
		presentNames[e.Name] = true
		if _, exists := existingByName[e.Name]; exists {
			continue // already in DB; keep existing position
		}
		kind := "text"
		var childGridID int64
		if e.Kind == fssource.KindDir {
			kind = "well"
			childPath := e.AbsPath
			// Get or create grid row for the subdirectory.
			var cgid int64
			err2 := tx.QueryRow(`SELECT id FROM grids WHERE path = ?`, childPath).Scan(&cgid)
			if err2 == sql.ErrNoRows {
				res2, err3 := tx.Exec(`INSERT INTO grids (path) VALUES (?)`, childPath)
				if err3 != nil {
					return err3
				}
				cgid, _ = res2.LastInsertId()
			} else if err2 != nil {
				return err2
			}
			childGridID = cgid
		}
		x, y := nextCell()
		if kind == "well" {
			_, err = tx.Exec(`INSERT INTO tiles (grid_id, name, kind, x, y, child_grid_id) VALUES (?, ?, ?, ?, ?, ?)`,
				gridID, e.Name, kind, x, y, childGridID)
		} else {
			_, err = tx.Exec(`INSERT INTO tiles (grid_id, name, kind, x, y) VALUES (?, ?, ?, ?, ?)`,
				gridID, e.Name, kind, x, y)
		}
		if err != nil {
			return err
		}
	}

	// Delete tiles for names no longer on disk.
	for name, ex := range existingByName {
		if !presentNames[name] {
			if _, err := tx.Exec(`DELETE FROM tiles WHERE id = ?`, ex.id); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

