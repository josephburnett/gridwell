package fs

// The SEARCH tool (issue #258's worked example): fs declares one (+) menu
// creation entry — drop a "search" well into any fs grid, descend, type a
// query, and the well's child grid fills with LINK tiles to the matches
// (dirs as wells sharing the real directory grid; files as leaf links to
// the real file tile). Results are a SNAPSHOT taken when the query
// commits — placement then persists like any grid, links keep resolving
// wherever they are copied (they carry ordinary fs ids the router already
// routes), and re-running the search is re-committing the query.
//
// Storage: tool rows live in the SAME tiles table as projected entries,
// under the reserved name prefix "\x00" — real paths can never contain a
// NUL, so the reconcile sweep is taught exactly one rule: reserved rows
// are user state, never swept against the directory listing. A search
// well's child grid is a synthetic grids row ("\x00search:<tile-id>"),
// which GetGrid serves from rows alone (never readDir — the path is not a
// directory).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	"github.com/josephburnett/gridwell/plugins/griddb"
)

// MenuEntrySearch is the entry id fs declares (Tile.menu_entry carries it).
const MenuEntrySearch = "search"

// searchParamSchema is the #198-subset form the client prompts with on
// first descent.
const searchParamSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "title": "name contains"}
  },
  "required": ["query"]
}`

// searchMenuEntries is fs's declared (+) menu addition, stamped onto every
// fs grid by the serving node.
func searchMenuEntries() []*gridwellv1.MenuEntry {
	return []*gridwellv1.MenuEntry{{
		Id:          MenuEntrySearch,
		Label:       "search",
		Glyph:       "", // the globe — a lens glyph can join the vocabulary later
		Kind:        "well",
		ParamSchema: searchParamSchema,
	}}
}

// reservedName reports a tool row (never swept against the directory).
func reservedName(name string) bool { return strings.HasPrefix(name, "\x00") }

// searchGridPath is the synthetic grids.path for a search well's results.
func searchGridPath(tileID int64) string { return "\x00search:" + strconv.FormatInt(tileID, 10) }

// isToolGridPath reports a synthetic (non-directory) grid.
func isToolGridPath(path string) bool { return strings.HasPrefix(path, "\x00") }

// searchLimit caps a snapshot; searchDepth bounds the walk.
const (
	searchLimit = 50
	searchDepth = 6
)

// CreateTile accepts exactly the tiles fs itself offers: a search well
// (menu_entry = "search"). Everything else stays refused — fs is a
// read-only projection; the entry declaration IS the permission.
func (p *Plugin) CreateTile(_ context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.GetTile()
	if t.GetMenuEntry() != MenuEntrySearch || t.GetKind() != "well" {
		return nil, fmt.Errorf("fs: only the declared menu entries create here")
	}
	gridID, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fs CreateTile: invalid grid_id %q", req.GridId)
	}
	if path, perr := p.gridPath(gridID); perr != nil || isToolGridPath(path) {
		return nil, fmt.Errorf("fs: search wells live in directory grids")
	}
	// Reserved name from the row id: insert with a placeholder, then
	// stamp — UNIQUE(grid_id, name) holds without a second id space.
	res, err := p.db.Exec(`INSERT INTO tiles (grid_id, name, kind, x, y, w, h, menu_entry)
		VALUES (?, ?, 'well', ?, ?, ?, ?, ?)`,
		gridID, "\x00pending", t.GetX(), t.GetY(), max64(t.GetW(), 1), max64(t.GetH(), 1), MenuEntrySearch)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := p.db.Exec(`UPDATE tiles SET name = ? WHERE id = ?`, "\x00search:"+strconv.FormatInt(id, 10), id); err != nil {
		return nil, err
	}
	return p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
}

// searchTileMeta loads a tool row's entry/params, reporting ok=false for
// ordinary rows.
func (p *Plugin) searchTileMeta(tileID int64) (gridID int64, params string, ok bool, err error) {
	var entry string
	err = p.db.QueryRow(`SELECT grid_id, COALESCE(menu_entry,''), COALESCE(params,'') FROM tiles WHERE id = ?`,
		tileID).Scan(&gridID, &entry, &params)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return gridID, params, entry == MenuEntrySearch, nil
}

// commitSearch is the WriteContent arm for a search well: validate the
// params, run the snapshot, (re)fill the child grid.
func (p *Plugin) commitSearch(tileID int64, data []byte) (*gridwellv1.TileResponse, error) {
	var q struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		return nil, fmt.Errorf("fs search: params must be JSON: %w", err)
	}
	q.Query = strings.TrimSpace(q.Query)
	if q.Query == "" {
		return nil, fmt.Errorf("fs search: query is required")
	}
	gridID, _, isSearch, err := p.searchTileMeta(tileID)
	if err != nil || !isSearch {
		return nil, fmt.Errorf("fs search: not a search tile")
	}
	rootDir, err := p.gridPath(gridID)
	if err != nil {
		return nil, err
	}

	matches := p.runSearch(rootDir, q.Query)

	// The child grid: a synthetic grids row, its OLD snapshot replaced
	// wholesale (re-running a search is re-committing the query).
	childGrid, err := p.getOrCreateGrid(searchGridPath(tileID))
	if err != nil {
		return nil, err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tiles WHERE grid_id = ?`, childGrid); err != nil {
		return nil, err
	}
	occupied := map[[2]int64]bool{}
	for _, m := range matches {
		x, y := griddb.NextEmptyCell(occupied, autoGridWidth)
		griddb.OccupyRect(occupied, x, y, 1, 1)
		// A result row remembers the TARGET PATH; the read side
		// synthesizes the link (dir → shared child grid; file → leaf
		// link to the real tile, materialized below).
		if _, err := tx.Exec(`INSERT INTO tiles (grid_id, name, kind, x, y, target_path)
			VALUES (?, ?, ?, ?, ?, ?)`,
			childGrid, "\x00hit:"+m.rel, m.kind, x, y, m.abs); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE tiles SET params = ?, child_grid_id = ? WHERE id = ?`,
		string(data), childGrid, tileID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Materialize the FILE targets' own rows so leaf links resolve even
	// for directories never yet visited: one reconcile per distinct
	// parent (bounded by the result cap).
	parents := map[string]bool{}
	for _, m := range matches {
		if m.kind == "text" {
			parents[filepath.Dir(m.abs)] = true
		}
	}
	for dir := range parents {
		if gid, err := p.getOrCreateGrid(dir); err == nil {
			if entries, rerr := p.readDir(dir); rerr == nil {
				_ = p.reconcileTiles(gid, dir, entries)
			}
		}
	}
	return p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(tileID, 10)})
}

type searchMatch struct {
	rel, abs, kind string
}

// runSearch walks rootDir (bounded depth/count) matching names by
// case-insensitive substring. A snapshot, deliberately: results are what
// the walk saw at commit; the grid then stays as arranged.
func (p *Plugin) runSearch(rootDir, query string) []searchMatch {
	needle := strings.ToLower(query)
	var out []searchMatch
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if depth > searchDepth || len(out) >= searchLimit {
			return
		}
		entries, err := p.readDir(dir)
		if err != nil {
			return // unreadable subtree: skip, never fail the search
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, e := range entries {
			if len(out) >= searchLimit {
				return
			}
			r := filepath.Join(rel, e.Name)
			kind := "text"
			if e.Kind == fssource.KindDir {
				kind = "well"
			}
			if strings.Contains(strings.ToLower(e.Name), needle) {
				out = append(out, searchMatch{rel: r, abs: e.AbsPath, kind: kind})
			}
			if kind == "well" {
				walk(e.AbsPath, r, depth+1)
			}
		}
	}
	walk(rootDir, "", 0)
	return out
}

// serveToolGrid answers GetGrid for a synthetic tool grid: stored rows
// only (the path is not a directory — never readDir, never sweep), each
// row synthesized into its link form.
func (p *Plugin) serveToolGrid(gridID int64, path string) (*gridwellv1.GetGridResponse, error) {
	// Scan first, resolve after: the pool is one connection
	// (SetMaxOpenConns(1)), so a nested query while this cursor is open
	// would deadlock the plugin.
	type row struct {
		id, x, y, w, h     int64
		name, kind, target string
	}
	rows, err := p.db.Query(`SELECT id, name, kind, x, y, w, h, COALESCE(target_path,'')
		FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	var stored []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.kind, &r.x, &r.y, &r.w, &r.h, &r.target); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var tiles []*gridwellv1.Tile
	for _, r := range stored {
		t := &gridwellv1.Tile{
			Id: strconv.FormatInt(r.id, 10), GridId: strconv.FormatInt(gridID, 10),
			Kind: r.kind, X: r.x, Y: r.y, W: max64(r.w, 1), H: max64(r.h, 1),
			AltText: strings.TrimPrefix(strings.TrimPrefix(r.name, "\x00hit:"), "\x00"),
		}
		if r.target != "" {
			if r.kind == "well" {
				// A dir hit shares the REAL directory grid — descending
				// the result is descending the directory.
				if cg, err := p.getOrCreateGrid(r.target); err == nil {
					t.ChildGridId = strconv.FormatInt(cg, 10)
				}
			} else if tid, ok := p.tileIDForPath(r.target); ok {
				// A file hit is a leaf LINK to the real tile: readers
				// resolve content/preview through it, and a copy carried
				// anywhere keeps routing (the id is ordinary fs id space).
				t.LinkTargetId = strconv.FormatInt(tid, 10)
			}
		}
		tiles = append(tiles, t)
	}
	return &gridwellv1.GetGridResponse{
		Grid:  &gridwellv1.Grid{Id: strconv.FormatInt(gridID, 10), SourceKind: "fs", SourceId: path},
		Tiles: tiles,
	}, nil
}

// tileIDForPath finds the projected tile row for an absolute file path
// (its parent grid's row by name), if materialized.
func (p *Plugin) tileIDForPath(abs string) (int64, bool) {
	dir, name := filepath.Split(abs)
	dir = filepath.Clean(dir)
	var id int64
	err := p.db.QueryRow(`SELECT t.id FROM tiles t JOIN grids g ON t.grid_id = g.id
		WHERE g.path = ? AND t.name = ?`, dir, name).Scan(&id)
	return id, err == nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
