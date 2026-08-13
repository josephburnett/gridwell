// Package fs implements a Gridwell plugin that projects a host directory tree.
// Each plugin instance has its own SQLite DB that maps directory paths to
// stable integer grid IDs and file/dir names to stable integer tile IDs.
// Positions (x,y,w,h) and well view framing are stored in the plugin DB so
// they survive restarts.
package fs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/dbformat"
	"github.com/josephburnett/gridwell/internal/fssource"
	"github.com/josephburnett/gridwell/internal/plugin/griddb"
	"github.com/josephburnett/gridwell/internal/trash"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

const autoGridWidth = 8

// On-disk format identity (see internal/dbformat). fsApplicationID is the
// bytes "GWfs" stamped into the SQLite header so an fs plugin DB is
// recognizable and a foreign file is refused; fsSchemaVersion is the schema
// generation this binary materializes. The fs DB holds forever-data — user
// placement, well framing, and the path→id map saved deep links resolve
// through — so it carries the same contract as the localdb: additive-only
// migrations, never delete the DB to absorb a change.
const (
	fsApplicationID = 0x47576673 // "GWfs"
	fsSchemaVersion = 2
)

// fsMigrations is the ordered additive chain; entry i brings a DB from
// version i+1 to i+2. Every change bumps fsSchemaVersion by one, appends
// one entry here, and updates schemaTemplate — TestFSSchemaEquivalence
// proves template == v1 + chain.
var fsMigrations = []dbformat.Migration{
	// v2 (2026-08-13, framing audit): the ROOT grid's persisted viewport —
	// SetRootView was silently swallowed before (fs never implemented it),
	// so panning an fs root was lost on every re-entry. NULLABLE on
	// purpose: NULL = never set, distinguishable from any real framing
	// (the legacy grids.view_* columns default to values a fresh row
	// already has, so they cannot say "unset" and stay dead).
	{To: 2, Run: func(ctx context.Context, tx *sql.Tx) error {
		for _, ddl := range []string{
			`ALTER TABLE grids ADD COLUMN root_cx REAL`,
			`ALTER TABLE grids ADD COLUMN root_cy REAL`,
			`ALTER TABLE grids ADD COLUMN root_zoom REAL`,
		} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		return nil
	}},
}

// fsLabelCol is the tiles-table column holding a tile's display label for the
// fs plugin (the directory entry name). Passed to the shared griddb helpers.
const fsLabelCol = "name"

// Host is the destructive side-effect surface. Injected so tests never rm
// anything on disk. Production wires osHost; tests wire recordHost.
type Host interface {
	Remove(path string) error
	RemoveAll(path string) error
}

// osHost unlinks paths permanently. It is the default when no Host is wired
// (tests, which operate on temp dirs).
type osHost struct{}

func (osHost) Remove(p string) error    { return os.Remove(p) }
func (osHost) RemoveAll(p string) error { return os.RemoveAll(p) }

// trashHost is the production deletion surface: deleting a file/dir tile moves
// the path into the freedesktop trash so it stays recoverable, rather than an
// irreversible rm. Wired by NewFactory.
type trashHost struct{}

func (trashHost) Remove(p string) error    { return trash.Trash(p) }
func (trashHost) RemoveAll(p string) error { return trash.Trash(p) }

// Plugin implements gridwellv1.GridwellServer for a filesystem source.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	db   *sql.DB
	host Host
	// root is the plugin's configured default directory. Info reports it as the
	// default root grid (there is no Attach — the gRPC connection is the whole
	// lifecycle); it anchors path→grid-id resolution when no explicit path is given.
	root string
	// readDir lists a directory for reconcile. Defaults to fssource.Read;
	// overridable via SetReadDir so tests can simulate a transiently
	// unreadable directory (EACCES, an unmounted share) — tests often run as
	// root, where a chmod-based repro is impossible.
	readDir func(dir string) ([]fssource.Entry, error)
}

// SetRoot sets the configured default directory Info reports as the root when no path is
// supplied. Wired by NewFactory from config["root"].
func (p *Plugin) SetRoot(root string) { p.root = root }

// SetReadDir overrides the directory reader (a test seam, like the Host
// deletion surface). nil restores the default fssource.Read.
func (p *Plugin) SetReadDir(f func(dir string) ([]fssource.Entry, error)) {
	if f == nil {
		f = fssource.Read
	}
	p.readDir = f
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
	if err := dbformat.EnsureVersion(context.Background(), db, fsApplicationID, fsSchemaVersion, fsMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("fs plugin %s: %w", dbPath, err)
	}
	return &Plugin{db: db, host: host, readDir: fssource.Read}, nil
}

// schemaTemplate is the always-current schema a fresh Open materializes
// directly — the single readable description of the present shape. Every
// column added here after v1 must be matched by an entry in fsMigrations.
const schemaTemplate = `
CREATE TABLE IF NOT EXISTS grids (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    path      TEXT NOT NULL UNIQUE,
    view_x    INTEGER NOT NULL DEFAULT 0,
    view_y    INTEGER NOT NULL DEFAULT 0,
    view_zoom REAL NOT NULL DEFAULT 1.0,
    -- The ROOT grid's persisted viewport (v2). NULL = never set.
    root_cx   REAL,
    root_cy   REAL,
    root_zoom REAL
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
);`

// schemaV1 is the FROZEN v1 schema — an immutable byte-for-byte copy of what
// schemaTemplate was at the moment the format was stamped. NEVER edit it (and
// never alias it to schemaTemplate — the freeze must not move when the
// template does): tests build genuine old files from this text and migrate
// them forward. New columns go into schemaTemplate plus a migration — never
// here.
const schemaV1 = `
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
);`

func createSchema(db *sql.DB) error {
	_, err := db.Exec(schemaTemplate)
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
	p, err := Open(dbPath, trashHost{})
	if err != nil {
		return nil, err
	}
	p.SetRoot(cfg.Config["root"])
	return p, nil
}

// Info is the whole handshake: identity plus the default root grid (the
// plugin's configured root directory, resolved to a grid id). No Attach/Detach.
func (p *Plugin) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	resp := &gridwellv1.InfoResponse{Kind: "fs", DisplayName: "files", SchemaVersion: fsSchemaVersion}
	path := filepath.Clean(p.root)
	if path == "" || path == "." {
		return resp, nil // no configured root → no descendable default
	}
	gridID, err := p.getOrCreateGrid(path)
	if err != nil {
		return nil, err
	}
	resp.RootGridId = strconv.FormatInt(gridID, 10)
	if label := filepath.Base(path); label != "/" && label != "." {
		resp.DisplayName = label
	}
	// The root's persisted viewport (v2; framing audit 2026-08-13) — the
	// read side of SetRootView, so re-entry lands where the user left.
	if cx, cy, zoom, ok, err := griddb.RootView(p.db, gridID); err == nil && ok {
		resp.RootViewCx, resp.RootViewCy, resp.RootViewZoom = cx, cy, zoom
	}
	return resp, nil
}

// SetRootView persists the root grid's viewport (framing audit 2026-08-13:
// this was silently swallowed before — pan an fs root, gone on re-entry).
// Framing-class; the server routes here by root_grid_id.
func (p *Plugin) SetRootView(_ context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	gridID, err := strconv.ParseInt(req.RootGridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fs SetRootView: invalid root_grid_id %q", req.RootGridId)
	}
	if err := griddb.SetRootView(p.db, gridID, req.Cx, req.Cy, req.Zoom); err != nil {
		return nil, err
	}
	return &gridwellv1.SetRootViewResponse{}, nil
}

// GetGrid reads the directory for the given grid_id, reconciles tile rows
// against the current directory contents, and returns the resulting tiles.
// A missing (definitively gone) directory returns an empty grid without
// error; a directory that exists but cannot be read this pass returns the
// stored rows untouched — see the sweep-policy split in the body.
func (p *Plugin) GetGrid(_ context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	gridID, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fs GetGrid: invalid grid_id %q", req.GridId)
	}

	path, err := p.gridPath(gridID)
	if err != nil {
		return nil, fmt.Errorf("fs GetGrid %d: %w", gridID, err)
	}

	entries, readErr := p.readDir(path)
	if readErr != nil {
		if errors.Is(readErr, iofs.ErrNotExist) {
			// The directory is definitively GONE: an empty listing is
			// authoritative, and the reconcile below sweeps its rows.
			entries = nil
		} else {
			// Transiently unreadable (EACCES, an unmounted network share):
			// NOT authoritative. A failed read must never sweep a tile —
			// only a definite gone does (I12; the proc policy). Skip the
			// reconcile and serve the stored rows exactly as they are, so
			// positions and ids survive until the source is readable again.
			tiles, err := griddb.LoadTiles(p.db, fsLabelCol, gridID)
			if err != nil {
				return nil, err
			}
			stampServesPage(tiles)
			grid := &gridwellv1.Grid{Id: req.GridId, SourceKind: "fs", SourceId: path}
			return &gridwellv1.GetGridResponse{Grid: grid, Tiles: tiles}, nil
		}
	}

	if err := p.reconcileTiles(gridID, path, entries); err != nil {
		return nil, err
	}

	tiles, err := griddb.LoadTiles(p.db, fsLabelCol, gridID)
	if err != nil {
		return nil, err
	}
	stampServesPage(tiles)

	grid := &gridwellv1.Grid{Id: req.GridId, SourceKind: "fs", SourceId: path}
	return &gridwellv1.GetGridResponse{Grid: grid, Tiles: tiles}, nil
}

// GetTile returns one tile row by id — cloneAcrossPlugins' first call against
// the source plugin when a tile is right-dragged into another plugin's grid
// (issue #171). The row was materialized by the GetGrid that rendered it.
func (p *Plugin) GetTile(_ context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	resp, err := griddb.ApplyGetTile(p.db, fsLabelCol, req)
	if err == nil && resp.GetTile() != nil {
		stampServesPage([]*gridwellv1.Tile{resp.Tile})
	}
	return resp, err
}

// PlaceTile is the single placement writeback: in-grid only (a cross-grid
// placement would be an on-disk move, which fs does not perform).
func (p *Plugin) PlaceTile(_ context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplyPlace(p.db, fsLabelCol, req)
}

// SetTile persists a directory well's preview framing so descent and ascent
// restore the same view. fs supports framing only on its directory wells;
// other kinds/writebacks are not applicable.
func (p *Plugin) SetTile(_ context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	if k := req.GetTile().GetKind(); k != "well" {
		return nil, fmt.Errorf("fs SetTile: only well framing supported, got %q", k)
	}
	return griddb.ApplySetWellView(p.db, fsLabelCol, req)
}

// renderableBodyCap bounds how much of a renderable file the descent body
// carries (issue #236): a document view, not a file transfer. Files past
// the cap fall back to the metadata summary.
const renderableBodyCap = 4 << 20

// ContentBody returns the descent body for a file tile. A RENDERABLE file
// (markdown.Renderable — the same rule the client colors tiles by, issue
// #236) serves its real bytes, so descending shows the document itself;
// everything else serves the small metadata summary. Directories,
// unreadable paths, and unknown ids return empty content rather than an
// error.
func (p *Plugin) ContentBody(tileIDStr string) (data []byte, mediaType string, err error) {
	tileID, err := strconv.ParseInt(tileIDStr, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("fs ReadContent: invalid tile_id %q", tileIDStr)
	}
	var gridID int64
	var name, kind string
	err = p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if kind != "text" {
		return nil, "", nil
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return nil, "", err
	}
	fullPath := filepath.Join(dirPath, name)
	entry, err := fssource.Stat(fullPath)
	if err != nil {
		return nil, "", nil
	}
	if markdown.Renderable(name) && entry.Size <= renderableBodyCap {
		if body, readErr := os.ReadFile(fullPath); readErr == nil {
			return body, "text/markdown", nil
		}
		// Unreadable despite the stat: the metadata summary still tells
		// the user what is here instead of a blank pane (charter §6).
	}
	return []byte(fssource.MetadataMarkdown(entry)), "text/markdown", nil
}

// ReadContent streams a file tile's descent body (one chunk; fs bodies are
// small metadata summaries, version 0 — not version-edited). The one content
// read.
func (p *Plugin) ReadContent(req *gridwellv1.ReadContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	data, mediaType, err := p.ContentBody(req.TileId)
	if err != nil {
		return err
	}
	return stream.Send(&gridwellv1.ContentChunk{Data: data, MediaType: mediaType})
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
	nextCell := func() (int64, int64) { return griddb.NextEmptyCell(occupied, autoGridWidth) }

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
