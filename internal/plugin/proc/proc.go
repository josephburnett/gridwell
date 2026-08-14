// Package proc implements a Gridwell plugin that projects the host process
// table. Each plugin instance has its own SQLite DB that maps process PIDs to
// stable integer IDs. When a process exits its tile is swept on the next
// GetGrid call; a new process with the same PID gets a fresh tile ID.
//
// The root grid for a process represents that process's direct children. A
// synthetic "@info" text tile carries the process's own metadata. Listings are
// non-authoritative: a child unreadable this pass is kept until Probe confirms
// it is definitively gone.
package proc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"syscall"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/dbformat"
	"github.com/josephburnett/gridwell/internal/plugin/griddb"
	"github.com/josephburnett/gridwell/internal/procsource"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

const autoGridWidth = 8

// procLabelCol is the tiles-table column holding a tile's display label for
// the proc plugin (the source key: a PID string or "@info"). Passed to the
// shared griddb helpers.
const procLabelCol = "key"

// infoKey is the stable source key for the tile representing the root
// process's own metadata.
const infoKey = "@info"

// Killer is the signal interface. Injected so tests never signal real
// processes. Production uses syscall.Kill.
type Killer interface {
	Kill(pid int64, sig syscall.Signal) error
}

type sysKiller struct{}

func (sysKiller) Kill(pid int64, sig syscall.Signal) error {
	return syscall.Kill(int(pid), sig)
}

// Plugin implements gridwellv1.GridwellServer for the process table.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	db       *sql.DB
	procRoot string
	killer   Killer
	// rootPID is the process the default root grid is rooted at (Info). 0/unset
	// means pid 1. Set via SetRootPID (config["pid"]); mirrors fs's SetRoot.
	rootPID int64
}

// SetRootPID configures the pid Info reports as the default root grid. Used by
// the launcher-mount path and tests; defaults to pid 1.
func (p *Plugin) SetRootPID(pid int64) { p.rootPID = pid }

// Open opens or creates the plugin SQLite DB at dbPath. An empty procRoot
// uses the default "/proc". A nil killer uses syscall.Kill.
func Open(dbPath, procRoot string, killer Killer) (*Plugin, error) {
	if procRoot == "" {
		procRoot = procsource.DefaultRoot
	}
	if killer == nil {
		killer = sysKiller{}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("proc plugin open %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("proc plugin pragmas: %w", err)
	}
	if _, err := db.Exec(schemaTemplate); err != nil {
		db.Close()
		return nil, fmt.Errorf("proc plugin schema: %w", err)
	}
	if err := dbformat.EnsureVersion(context.Background(), db, procApplicationID, procSchemaVersion, procMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("proc plugin %s: %w", dbPath, err)
	}
	return &Plugin{db: db, procRoot: procRoot, killer: killer}, nil
}

// On-disk format identity (see internal/dbformat). procApplicationID is the
// bytes "GWpc" stamped into the SQLite header; procSchemaVersion is the
// schema generation this binary materializes. The proc DB holds forever-data
// (placement, framing, the pid→id identity map), so it carries the same
// contract as the localdb: additive-only migrations, never delete the DB to
// absorb a change.
const (
	procApplicationID = 0x47577063 // "GWpc"
	procSchemaVersion = 2
)

// procMigrations is the ordered additive chain; entry i brings a DB from
// version i+1 to i+2. Every change bumps procSchemaVersion by one, appends
// one entry here, and updates schemaTemplate — TestProcSchemaEquivalence
// proves template == v1 + chain.
var procMigrations = []dbformat.Migration{
	// v2 (2026-08-13, framing audit): the ROOT grid's persisted viewport —
	// same shape and rationale as the fs v2 migration (NULL = never set).
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

// schemaTemplate is the always-current schema a fresh Open materializes
// directly. Every column added here after v1 must be matched by an entry in
// procMigrations.
const schemaTemplate = `
CREATE TABLE IF NOT EXISTS grids (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    pid       INTEGER NOT NULL UNIQUE,
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
    key           TEXT NOT NULL,
    pid           INTEGER NOT NULL DEFAULT 0,
    kind          TEXT NOT NULL CHECK (kind IN ('well','text')),
    x             INTEGER NOT NULL DEFAULT 0,
    y             INTEGER NOT NULL DEFAULT 0,
    w             INTEGER NOT NULL DEFAULT 1,
    h             INTEGER NOT NULL DEFAULT 1,
    child_grid_id INTEGER,
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 1.0,
    UNIQUE (grid_id, key)
);`

// schemaV1 is the FROZEN v1 schema — an immutable byte-for-byte copy of what
// schemaTemplate was when the format was stamped. NEVER edit it (and never
// alias it to schemaTemplate — the freeze must not move when the template
// does): tests build genuine old files from this text and migrate them
// forward.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS grids (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    pid       INTEGER NOT NULL UNIQUE,
    view_x    INTEGER NOT NULL DEFAULT 0,
    view_y    INTEGER NOT NULL DEFAULT 0,
    view_zoom REAL NOT NULL DEFAULT 1.0
);
CREATE TABLE IF NOT EXISTS tiles (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    key           TEXT NOT NULL,
    pid           INTEGER NOT NULL DEFAULT 0,
    kind          TEXT NOT NULL CHECK (kind IN ('well','text')),
    x             INTEGER NOT NULL DEFAULT 0,
    y             INTEGER NOT NULL DEFAULT 0,
    w             INTEGER NOT NULL DEFAULT 1,
    h             INTEGER NOT NULL DEFAULT 1,
    child_grid_id INTEGER,
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 1.0,
    UNIQUE (grid_id, key)
);`

// Close closes the underlying database.
func (p *Plugin) Close() error { return p.db.Close() }

// NewFactory returns a ServerFactory for the "proc" kind. config["db_file"] is
// the plugin's SQLite DB; config["pid"] (optional, default 1) is the root pid
// Info reports as the default root grid.
func NewFactory(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
	dbPath := cfg.Config["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("proc plugin %q: db_file config key required", cfg.Name)
	}
	p, err := Open(dbPath, "", nil)
	if err != nil {
		return nil, err
	}
	if pidStr := cfg.Config["pid"]; pidStr != "" {
		pid, err := strconv.ParseInt(pidStr, 10, 64)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("proc plugin %q: invalid pid %q", cfg.Name, pidStr)
		}
		p.SetRootPID(pid)
	}
	return p, nil
}

// Info is the whole handshake: identity plus the default root grid (the process
// table rooted at the configured root pid, default 1). No Attach/Detach.
func (p *Plugin) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	pid := p.rootPID
	if pid <= 0 {
		pid = 1
	}
	gridID, err := p.getOrCreateGrid(pid)
	if err != nil {
		return nil, err
	}
	label := "processes"
	if pid != 1 {
		label = fmt.Sprintf("pid %d", pid)
	}
	resp := &gridwellv1.InfoResponse{
		Kind:          "proc",
		DisplayName:   label,
		SchemaVersion: procSchemaVersion,
		RootGridId:    strconv.FormatInt(gridID, 10),
	}
	// The root's persisted viewport (v2; framing audit 2026-08-13).
	if cx, cy, zoom, ok, err := griddb.RootView(p.db, gridID); err == nil && ok {
		resp.RootViewCx, resp.RootViewCy, resp.RootViewZoom = cx, cy, zoom
	}
	return resp, nil
}

// SetRootView persists the root grid's viewport (framing audit 2026-08-13
// — it was silently swallowed before). Framing-class.
func (p *Plugin) SetRootView(_ context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	gridID, err := strconv.ParseInt(req.RootGridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("proc SetRootView: invalid root_grid_id %q", req.RootGridId)
	}
	if err := griddb.SetRootView(p.db, gridID, req.Cx, req.Cy, req.Zoom); err != nil {
		return nil, err
	}
	return &gridwellv1.SetRootViewResponse{}, nil
}

// GetGrid reads the process's children, reconciles tile rows, and returns tiles.
// Missing or unreadable processes return an empty grid without error.
// Reconciliation is non-authoritative: tiles for processes unreadable this pass
// are kept; tiles for definitively-gone PIDs are swept.
func (p *Plugin) GetGrid(_ context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	gridID, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("proc GetGrid: invalid grid_id %q", req.GridId)
	}
	rootPID, err := p.gridPID(gridID)
	if err != nil {
		return nil, fmt.Errorf("proc GetGrid %d: %w", gridID, err)
	}

	if err := p.reconcileTiles(gridID, rootPID); err != nil {
		return nil, err
	}

	tiles, err := griddb.LoadTiles(p.db, procLabelCol, gridID)
	if err != nil {
		return nil, err
	}

	grid := &gridwellv1.Grid{
		Id:         req.GridId,
		SourceKind: "proc",
		SourceId:   strconv.FormatInt(rootPID, 10),
	}
	return &gridwellv1.GetGridResponse{Grid: grid, Tiles: tiles}, nil
}

// MoveTile repositions a process tile within its grid and persists the new
// position so it survives the next GetGrid and a restart.
// GetTile returns one tile row by id — cloneAcrossPlugins' first call against
// the source plugin when a tile is right-dragged into another plugin's grid
// (issue #171; same class as fs).
func (p *Plugin) GetTile(_ context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplyGetTile(p.db, procLabelCol, req)
}

// PlaceTile is the single placement writeback: in-grid only (a process tile
// cannot change parent).
func (p *Plugin) PlaceTile(_ context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return griddb.ApplyPlace(p.db, procLabelCol, req)
}

// SetTile persists a process well's preview framing so descent and ascent
// restore the same view. proc supports framing only on its process wells.
func (p *Plugin) SetTile(_ context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	if k := req.GetTile().GetKind(); k != "well" {
		return nil, fmt.Errorf("proc SetTile: only well framing supported, got %q", k)
	}
	return griddb.ApplySetWellView(p.db, procLabelCol, req)
}

// ContentBody returns the descent body for the @info tile: a markdown
// summary of the grid's root process metadata. Non-@info tiles and gone
// processes return empty content rather than an error.
func (p *Plugin) ContentBody(tileIDStr string) (data []byte, mediaType string, err error) {
	tileID, err := strconv.ParseInt(tileIDStr, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("proc ReadContent: invalid tile_id %q", tileIDStr)
	}
	var gridID int64
	var key, kind string
	err = p.db.QueryRow(`SELECT grid_id, key, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &key, &kind)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if kind != "text" || key != infoKey {
		return nil, "", nil
	}
	rootPID, err := p.gridPID(gridID)
	if err != nil {
		return nil, "", err
	}
	info, err := procsource.Get(p.procRoot, rootPID)
	if err != nil {
		return nil, "", nil
	}
	return []byte(procsource.MetadataMarkdown(info)), "text/markdown", nil
}

// ReadContent streams a process tile's descent body (one chunk; proc bodies
// are small @info summaries, version 0 — not version-edited). The one
// content read.
func (p *Plugin) ReadContent(req *gridwellv1.ReadContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	data, mediaType, err := p.ContentBody(req.TileId)
	if err != nil {
		return err
	}
	return stream.Send(&gridwellv1.ContentChunk{Data: data, MediaType: mediaType})
}

// Probe checks whether the process backing tile_id still exists.
func (p *Plugin) Probe(_ context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	var pid int64
	var key string
	err = p.db.QueryRow(`SELECT pid, key FROM tiles WHERE id = ?`, tileID).Scan(&pid, &key)
	if err == sql.ErrNoRows {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	if key == infoKey {
		// @info maps to the root PID of the parent grid.
		var gridID int64
		if err := p.db.QueryRow(`SELECT grid_id FROM tiles WHERE id = ?`, tileID).Scan(&gridID); err != nil {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
		}
		var rootPID int64
		if err := p.db.QueryRow(`SELECT pid FROM grids WHERE id = ?`, gridID).Scan(&rootPID); err != nil {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
		}
		pid = rootPID
	}
	present, err := procsource.Exists(p.procRoot, pid)
	switch {
	case err != nil:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	case present:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
	default:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
}

// DeleteTile sends SIGTERM to the process backing tile_id. settled=false:
// SIGTERM is best-effort; the tile is swept on the next GetGrid call once
// the process is definitively gone.
func (p *Plugin) DeleteTile(_ context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	var pid int64
	var key string
	var gridID int64
	err = p.db.QueryRow(`SELECT grid_id, pid, key FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &pid, &key)
	if err == sql.ErrNoRows {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	if key == infoKey {
		// Deleting @info signals the grid's own PID.
		if err := p.db.QueryRow(`SELECT pid FROM grids WHERE id = ?`, gridID).Scan(&pid); err != nil {
			return nil, err
		}
	}
	if pid <= 0 {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	if err := p.killer.Kill(pid, syscall.SIGTERM); err != nil {
		return nil, fmt.Errorf("proc DeleteTile kill %d: %w", pid, err)
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (p *Plugin) getOrCreateGrid(pid int64) (int64, error) {
	var id int64
	err := p.db.QueryRow(`SELECT id FROM grids WHERE pid = ?`, pid).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := p.db.Exec(`INSERT INTO grids (pid) VALUES (?)`, pid)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (p *Plugin) gridPID(gridID int64) (int64, error) {
	var pid int64
	err := p.db.QueryRow(`SELECT pid FROM grids WHERE id = ?`, gridID).Scan(&pid)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("grid %d not found", gridID)
	}
	return pid, err
}

// reconcileTiles reads the live child processes of rootPID and updates the
// tiles table: upsert for present children, delete tiles for definitively-gone
// PIDs. Non-authoritative: a process unreadable this pass is kept.
func (p *Plugin) reconcileTiles(gridID, rootPID int64) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load existing tile rows (key → {id, pid, x, y}).
	rows, err := tx.Query(`SELECT id, key, pid, x, y, w, h FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type existing struct{ id, pid, x, y, w, h int64 }
	existingByKey := map[string]existing{}
	for rows.Next() {
		var id, pid, x, y, w, h int64
		var key string
		if err := rows.Scan(&id, &key, &pid, &x, &y, &w, &h); err != nil {
			rows.Close()
			return err
		}
		existingByKey[key] = existing{id, pid, x, y, w, h}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Auto-layout tracker.
	occupied := map[[2]int64]bool{}
	for _, e := range existingByKey {
		// Full footprint (griddb.OccupyRect): a resized tile's interior is
		// NOT free — seeding only origins dropped new processes inside it.
		griddb.OccupyRect(occupied, e.x, e.y, e.w, e.h)
	}
	nextCell := func() (int64, int64) { return griddb.NextEmptyCell(occupied, autoGridWidth) }

	// @info tile for the root process's own metadata.
	if _, exists := existingByKey[infoKey]; !exists {
		info, err := procsource.Get(p.procRoot, rootPID)
		if err == nil {
			x, y := nextCell()
			_ = info
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO tiles (grid_id, key, pid, kind, x, y) VALUES (?, ?, ?, 'text', ?, ?)`,
				gridID, infoKey, 0, x, y); err != nil {
				return err
			}
		}
	}

	// Child process tiles.
	children, childErr := procsource.Children(p.procRoot, rootPID)
	presentKeys := map[string]bool{infoKey: true}
	if childErr == nil {
		for _, c := range children {
			key := strconv.FormatInt(c.PID, 10)
			presentKeys[key] = true
			if _, exists := existingByKey[key]; exists {
				continue
			}
			// New child: get or create a grid for its PID.
			var cgid int64
			err2 := tx.QueryRow(`SELECT id FROM grids WHERE pid = ?`, c.PID).Scan(&cgid)
			if err2 == sql.ErrNoRows {
				res2, err3 := tx.Exec(`INSERT INTO grids (pid) VALUES (?)`, c.PID)
				if err3 != nil {
					return err3
				}
				cgid, _ = res2.LastInsertId()
			} else if err2 != nil {
				return err2
			}
			x, y := nextCell()
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO tiles (grid_id, key, pid, kind, x, y, child_grid_id) VALUES (?, ?, ?, 'well', ?, ?, ?)`,
				gridID, key, c.PID, x, y, cgid); err != nil {
				return err
			}
		}
	}

	// Non-authoritative sweep: delete tiles only for definitively-gone PIDs.
	for key, ex := range existingByKey {
		if presentKeys[key] {
			continue
		}
		if key == infoKey {
			continue
		}
		pid, _ := strconv.ParseInt(key, 10, 64)
		present, err := procsource.Exists(p.procRoot, pid)
		if err != nil || present {
			continue // uncertain or still alive: keep
		}
		if _, err := tx.Exec(`DELETE FROM tiles WHERE id = ?`, ex.id); err != nil {
			return err
		}
	}

	return tx.Commit()
}
