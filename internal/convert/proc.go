package convert

// Proc converts a legacy proc plugin DB into a v2 external memory DB —
// the fs converter's twin. Contexts are pids; tile keys are pid strings,
// with the legacy per-grid "@info" label becoming the globally-unique
// "info:<pid>" key the v2 provider uses.

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"

	"google.golang.org/protobuf/proto"

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	"github.com/josephburnett/gridwell/internal/layout"
	_ "modernc.org/sqlite"
)

var procKnownTables = map[string][]string{
	"_gridwell_meta":  {"k", "v"},
	"grids":           {"id", "pid", "view_x", "view_y", "view_zoom", "root_cx", "root_cy", "root_zoom"},
	"tiles":           {"id", "grid_id", "key", "pid", "kind", "x", "y", "w", "h", "child_grid_id", "view_x", "view_y", "view_zoom"},
	"sqlite_sequence": nil,
}

// Proc converts a legacy proc DB at legacyPath into a memory DB at
// outPath. Same contract as FS: identity verbatim, refuse the unknown.
func Proc(legacyPath, outPath, uuid, kind string) (*Result, error) {
	src, err := sql.Open("sqlite", "file:"+legacyPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("convert proc: open %s: %w", legacyPath, err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)
	if err := refuseUnknown(src, "proc", procKnownTables); err != nil {
		return nil, err
	}
	if err := verifyMeta(src, uuid, kind); err != nil {
		return nil, err
	}
	// A never-served plugin's DB is pluginmeta-only (init stamps identity;
	// the schema materializes on first open): nothing to convert — an
	// empty memory DB is the correct v2 twin.
	if ok, err := tableExists(src, "grids"); err != nil {
		return nil, err
	} else if !ok {
		mem, err := layout.OpenVerified(outPath, uuid, kind)
		if err != nil {
			return nil, err
		}
		_ = mem.Close()
		return &Result{}, nil
	}
	mem, err := layout.OpenVerified(outPath, uuid, kind)
	if err != nil {
		return nil, err
	}
	defer mem.Close()

	res := &Result{}
	gridPID := map[int64]int64{}
	gridByPID := map[int64]int64{}
	rows, err := src.Query(`SELECT id, pid, root_cx, root_cy, root_zoom FROM grids ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type ctxRow struct {
		id, pid        int64
		rcx, rcy, rzoo *float64
	}
	var ctxs []ctxRow
	for rows.Next() {
		var c ctxRow
		var rcx, rcy, rz sql.NullFloat64
		if err := rows.Scan(&c.id, &c.pid, &rcx, &rcy, &rz); err != nil {
			rows.Close()
			return nil, err
		}
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
		if err := mem.ImportContext(c.id, strconv.FormatInt(c.pid, 10), c.rcx, c.rcy, c.rzoo); err != nil {
			return nil, fmt.Errorf("convert proc: grid %d: %w", c.id, err)
		}
		gridPID[c.id] = c.pid
		gridByPID[c.pid] = c.id
		res.GridIDs = append(res.GridIDs, c.id)
		res.Grids++
	}

	rows, err = src.Query(`SELECT id, grid_id, key, pid, kind, x, y, w, h, child_grid_id, view_x, view_y, view_zoom FROM tiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type tileRow struct {
		id, gridID, pid, x, y, w, h, vx, vy int64
		vz                                  float64
		key, kind                           string
		child                               sql.NullInt64
	}
	var trs []tileRow
	for rows.Next() {
		var t tileRow
		if err := rows.Scan(&t.id, &t.gridID, &t.key, &t.pid, &t.kind, &t.x, &t.y, &t.w, &t.h, &t.child, &t.vx, &t.vy, &t.vz); err != nil {
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
		parentPID, ok := gridPID[t.gridID]
		if !ok {
			return nil, fmt.Errorf("convert proc: tile %d references unknown grid %d", t.id, t.gridID)
		}
		key := t.key
		label := t.key
		if t.key == "@info" {
			key = "info:" + strconv.FormatInt(parentPID, 10)
		}
		entry := &cpv1.Entry{Key: key, Kind: t.kind, Label: label}
		if t.kind == "well" {
			if t.child.Valid {
				childPID, ok := gridPID[t.child.Int64]
				if !ok {
					return nil, fmt.Errorf("convert proc: tile %d points at unknown grid %d", t.id, t.child.Int64)
				}
				if strconv.FormatInt(childPID, 10) != key {
					return nil, fmt.Errorf("convert proc: tile %d: stored child grid %d is pid %d, key derives %q — refusing to re-route",
						t.id, t.child.Int64, childPID, key)
				}
			}
			entry.ChildContext = key
		}
		if err := mem.ImportTile(t.id, t.gridID, key, t.x, t.y, t.w, t.h, t.vx, t.vy, t.vz); err != nil {
			return nil, fmt.Errorf("convert proc: tile %d (%s): %w", t.id, key, err)
		}
		entriesByGrid[t.gridID] = append(entriesByGrid[t.gridID], entry)
		res.Tiles++
	}
	_ = gridByPID

	for _, gid := range res.GridIDs {
		lr := &cpv1.ListResponse{Entries: entriesByGrid[gid], Authoritative: false,
			SourceLabel: strconv.FormatInt(gridPID[gid], 10)}
		blob, err := proto.Marshal(lr)
		if err != nil {
			return nil, err
		}
		if err := mem.CacheListing(gid, blob, false); err != nil {
			return nil, err
		}
	}

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
