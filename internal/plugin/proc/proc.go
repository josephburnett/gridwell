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
	"github.com/josephburnett/gridwell/internal/procsource"
	_ "modernc.org/sqlite"
)

const autoGridWidth = 8

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
}

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
	if _, err := db.Exec(`
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
);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("proc plugin schema: %w", err)
	}
	return &Plugin{db: db, procRoot: procRoot, killer: killer}, nil
}

// Close closes the underlying database.
func (p *Plugin) Close() error { return p.db.Close() }

// NewFactory returns a ServerFactory for the "proc" kind.
// config["db_file"] is the path to the plugin's SQLite DB.
func NewFactory(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
	dbPath := cfg.Config["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("proc plugin %q: db_file config key required", cfg.Name)
	}
	return Open(dbPath, "", nil)
}

// Info returns the static plugin descriptor.
func (p *Plugin) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{
		Kind:          "proc",
		DisplayName:   "processes",
		SchemaVersion: 1,
	}, nil
}

// Attach turns config["pid"] (default "1") into a root grid for that PID.
func (p *Plugin) Attach(_ context.Context, req *gridwellv1.AttachRequest) (*gridwellv1.AttachResponse, error) {
	pidStr := req.Config["pid"]
	if pidStr == "" {
		pidStr = "1"
	}
	pid, err := strconv.ParseInt(pidStr, 10, 64)
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("proc plugin: invalid pid %q", pidStr)
	}
	gridID, err := p.getOrCreateGrid(pid)
	if err != nil {
		return nil, err
	}
	label := "processes"
	if pid != 1 {
		label = fmt.Sprintf("pid %d", pid)
	}
	return &gridwellv1.AttachResponse{
		RootGridId: strconv.FormatInt(gridID, 10),
		Label:      label,
		Caps:       &gridwellv1.PluginCaps{},
		HasSession: false,
	}, nil
}

// Detach is a no-op for the proc plugin.
func (p *Plugin) Detach(_ context.Context, _ *gridwellv1.DetachRequest) (*gridwellv1.DetachResponse, error) {
	return &gridwellv1.DetachResponse{}, nil
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

	tiles, err := p.loadTiles(gridID)
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
	rows, err := tx.Query(`SELECT id, key, pid, x, y FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type existing struct{ id, pid, x, y int64 }
	existingByKey := map[string]existing{}
	for rows.Next() {
		var id, pid, x, y int64
		var key string
		if err := rows.Scan(&id, &key, &pid, &x, &y); err != nil {
			rows.Close()
			return err
		}
		existingByKey[key] = existing{id, pid, x, y}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Auto-layout tracker.
	occupied := map[[2]int64]bool{}
	for _, e := range existingByKey {
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

// loadTiles loads tile rows for a grid and converts them to proto Tiles.
func (p *Plugin) loadTiles(gridID int64) ([]*gridwellv1.Tile, error) {
	rows, err := p.db.Query(`
		SELECT id, key, pid, kind, x, y, w, h,
		       COALESCE(child_grid_id,0), view_x, view_y, view_zoom
		FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*gridwellv1.Tile
	for rows.Next() {
		var id, pid, x, y, w, h, childGrid, vx, vy int64
		var vz float64
		var key, kind string
		if err := rows.Scan(&id, &key, &pid, &kind, &x, &y, &w, &h, &childGrid, &vx, &vy, &vz); err != nil {
			return nil, err
		}
		t := &gridwellv1.Tile{
			Id:          strconv.FormatInt(id, 10),
			GridId:      strconv.FormatInt(gridID, 10),
			Kind:        kind,
			X:           x,
			Y:           y,
			W:           w,
			H:           h,
			AltText:     key,
			ChildGridId: strconv.FormatInt(childGrid, 10),
			ViewX:       vx,
			ViewY:       vy,
			ViewZoom:    vz,
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
