// Package convert is the one-time v2 migration (docs/v2-design.md §8):
// offline, never in place, identity verbatim. Each function converts one
// legacy database into its v2 home; the caller (the convert-v2 CLI, the
// tests) supplies COPIES and verifies with the parity crawl.
//
// The refuse-the-unknown rule: a converter enumerates exactly the tables
// and columns it understands and REFUSES to run when the source carries
// anything else — an unknown fact must never be silently dropped.
package convert

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	"github.com/josephburnett/gridwell/internal/layout"
	_ "modernc.org/sqlite"
)

// fsKnownTables is the complete v3 fs schema surface the converter
// understands: table → exact column set.
var fsKnownTables = map[string][]string{
	"_gridwell_meta":  {"k", "v"},
	"grids":           {"id", "path", "view_x", "view_y", "view_zoom", "root_cx", "root_cy", "root_zoom"},
	"tiles":           {"id", "grid_id", "name", "kind", "x", "y", "w", "h", "child_grid_id", "view_x", "view_y", "view_zoom", "menu_entry", "params", "target_path"},
	"sqlite_sequence": nil, // system table; columns are sqlite's
}

// FSResult reports what a conversion produced, for the scoped parity
// crawl and the operator's log.
type FSResult struct {
	Grids, Tiles int
	// GridIDs are the LOCAL grid ids materialized in the legacy DB —
	// the exact crawl scope the parity gate should use.
	GridIDs []int64
}

// FS converts a legacy fs plugin DB into a v2 external memory DB at
// outPath. root is the plugin's configured directory (server.yaml);
// legacy absolute paths become the provider's slash-relative keys.
// Identity (uuid/kind) is verified against the legacy pluginmeta rows
// and stamped into the memory DB. The legacy file is opened read-only.
func FS(legacyPath, outPath, uuid, kind, root string) (*FSResult, error) {
	src, err := sql.Open("sqlite", "file:"+legacyPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("convert fs: open %s: %w", legacyPath, err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)

	if err := refuseUnknown(src, "fs", fsKnownTables); err != nil {
		return nil, err
	}
	if err := verifyMeta(src, uuid, kind); err != nil {
		return nil, err
	}
	// Tool rows (#258 search wells) are USER STATE the v2 stack does not
	// carry yet: refuse rather than drop, so a home that uses them waits
	// for the userdocs support instead of losing them silently.
	var toolRows int
	if err := src.QueryRow(`SELECT COUNT(*) FROM tiles WHERE name LIKE char(0)||'%' OR menu_entry != ''`).Scan(&toolRows); err != nil {
		return nil, err
	}
	if toolRows > 0 {
		return nil, fmt.Errorf("convert fs: %d tool rows (search wells) present — v2 userdocs support required before this home converts", toolRows)
	}

	mem, err := layout.OpenVerified(outPath, uuid, kind)
	if err != nil {
		return nil, err
	}
	defer mem.Close()

	root = filepath.Clean(root)
	res := &FSResult{}

	// Contexts: every grids row, path → relative key, root view carried.
	gridKey := map[int64]string{}
	gridPath := map[int64]string{}
	rows, err := src.Query(`SELECT id, path, root_cx, root_cy, root_zoom FROM grids ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type ctxRow struct {
		id             int64
		key, path      string
		rcx, rcy, rzoo *float64
	}
	var ctxs []ctxRow
	for rows.Next() {
		var id int64
		var path string
		var rcx, rcy, rz sql.NullFloat64
		if err := rows.Scan(&id, &path, &rcx, &rcy, &rz); err != nil {
			rows.Close()
			return nil, err
		}
		key, err := relKey(path, root)
		if err != nil {
			rows.Close()
			return nil, err
		}
		c := ctxRow{id: id, key: key, path: path}
		// The root viewport is meaningful only when zoom was set (the
		// fs v2-migration NULL rule).
		if rz.Valid {
			c.rcx, c.rcy, c.rzoo = &rcx.Float64, &rcy.Float64, &rz.Float64
		}
		ctxs = append(ctxs, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range ctxs {
		if err := mem.ImportContext(c.id, c.key, c.rcx, c.rcy, c.rzoo); err != nil {
			return nil, fmt.Errorf("convert fs: grid %d (%s): %w", c.id, c.path, err)
		}
		gridKey[c.id] = c.key
		gridPath[c.id] = c.path
		res.GridIDs = append(res.GridIDs, c.id)
		res.Grids++
	}

	// Tiles: id, placement, framing verbatim; name joins the grid path
	// to form the key; a well's stored child grid must AGREE with the
	// path-derived child context or the conversion refuses (a silent
	// disagreement would re-route stored references).
	type tileRow struct {
		id, gridID, x, y, w, h, vx, vy int64
		vz                             float64
		name, kind                     string
		child                          sql.NullInt64
	}
	trs := []tileRow{}
	rows, err = src.Query(`SELECT id, grid_id, name, kind, x, y, w, h, child_grid_id, view_x, view_y, view_zoom FROM tiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t tileRow
		if err := rows.Scan(&t.id, &t.gridID, &t.name, &t.kind, &t.x, &t.y, &t.w, &t.h, &t.child, &t.vx, &t.vy, &t.vz); err != nil {
			rows.Close()
			return nil, err
		}
		trs = append(trs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	entriesByGrid := map[int64][]*cpv1.Entry{}
	for _, t := range trs {
		dir, ok := gridPath[t.gridID]
		if !ok {
			return nil, fmt.Errorf("convert fs: tile %d references unknown grid %d", t.id, t.gridID)
		}
		key, err := relKey(filepath.Join(dir, t.name), root)
		if err != nil {
			return nil, err
		}
		entry := &cpv1.Entry{Key: key, Kind: t.kind, Label: t.name}
		if t.kind == "well" {
			if !t.child.Valid {
				// A childless legacy well row: the reconcile would have
				// re-minted its grid on next read; carry it as-is (the
				// provider lists the dir as a well and the child context
				// mints on first descent).
			} else {
				wantKey := key
				childKey, ok := gridKey[t.child.Int64]
				if !ok {
					return nil, fmt.Errorf("convert fs: tile %d (well %s) points at unknown grid %d", t.id, key, t.child.Int64)
				}
				if childKey != wantKey {
					return nil, fmt.Errorf("convert fs: tile %d: stored child grid %d is %q, path derives %q — refusing to re-route a stored reference",
						t.id, t.child.Int64, childKey, wantKey)
				}
			}
			entry.ChildContext = key
		}
		if err := mem.ImportTile(t.id, t.gridID, key, t.x, t.y, t.w, t.h, t.vx, t.vy, t.vz); err != nil {
			return nil, fmt.Errorf("convert fs: tile %d (%s): %w", t.id, key, err)
		}
		entriesByGrid[t.gridID] = append(entriesByGrid[t.gridID], entry)
		res.Tiles++
	}

	// Seed the read-through cache with the legacy rows, so a source that
	// is unreadable at first post-cutover read still serves what the
	// legacy stored rows served. (Derived per-name fields — serves_page,
	// text_presentation — are absent here; the first LIVE read supplies
	// them, exactly as the legacy row store never held them either.)
	for _, gid := range res.GridIDs {
		lr := &cpv1.ListResponse{Entries: entriesByGrid[gid], Authoritative: true, SourceLabel: gridPath[gid]}
		blob, err := proto.Marshal(lr)
		if err != nil {
			return nil, err
		}
		if err := mem.CacheListing(gid, blob, true); err != nil {
			return nil, err
		}
	}

	// Pin the AUTOINCREMENT counters to the legacy values.
	ctxSeq, err := readSeq(src, "grids")
	if err != nil {
		return nil, err
	}
	tileSeq, err := readSeq(src, "tiles")
	if err != nil {
		return nil, err
	}
	if err := mem.SetSequences(ctxSeq, tileSeq); err != nil {
		return nil, err
	}
	sort.Slice(res.GridIDs, func(i, j int) bool { return res.GridIDs[i] < res.GridIDs[j] })
	return res, nil
}

// relKey turns a legacy absolute path into the provider's slash-relative
// key under root ("." for the root itself). A path outside the root is
// unknown territory: refuse.
func relKey(path, root string) (string, error) {
	path = filepath.Clean(path)
	if path == root {
		return ".", nil
	}
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("convert fs: path %q outside configured root %q", path, root)
	}
	return filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))), nil
}

// refuseUnknown verifies the source's table/column surface is EXACTLY
// what this converter understands.
func refuseUnknown(db *sql.DB, what string, known map[string][]string) error {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, tbl := range tables {
		want, ok := known[tbl]
		if !ok {
			return fmt.Errorf("convert %s: unknown table %q — refusing to drop what I don't understand", what, tbl)
		}
		if want == nil {
			continue // system table
		}
		cols, err := tableColumns(db, tbl)
		if err != nil {
			return err
		}
		if strings.Join(cols, ",") != strings.Join(want, ",") {
			return fmt.Errorf("convert %s: table %q columns %v, expected %v — refusing to drop what I don't understand",
				what, tbl, cols, want)
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols = append(cols, n)
	}
	return cols, rows.Err()
}

// verifyMeta checks the legacy pluginmeta identity against the target.
func verifyMeta(db *sql.DB, uuid, kind string) error {
	get := func(k string) (string, error) {
		var v string
		err := db.QueryRow(`SELECT v FROM _gridwell_meta WHERE k = ?`, k).Scan(&v)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return v, err
	}
	gotUUID, err := get("id")
	if err != nil {
		return err
	}
	gotKind, err := get("kind")
	if err != nil {
		return err
	}
	if gotUUID != uuid || gotKind != kind {
		return fmt.Errorf("convert: source identity %s/%s does not match target %s/%s", gotUUID, gotKind, uuid, kind)
	}
	return nil
}

func readSeq(db *sql.DB, table string) (int64, error) {
	var seq int64
	err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}
