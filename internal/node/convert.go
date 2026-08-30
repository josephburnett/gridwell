package node

// Convert folds a pre-one-node home (docs/one-node.md §2.6) into the one
// database: <home>/db/<id>/store.db (the home store) becomes
// <home>/gridwell.db; every plugin's memory DB (db/<plugin-id>/store.db —
// contexts, idmap, layout, cache_listings) becomes rows under its
// namespace; the transport's connections (db/<id>/remote.db) come along.
// Home keeps every id. A plugin's grids and tiles are RE-MINTED (one table,
// one sequence), and the references INTO them — home tiles' child_grid_id
// (exit wells) and link_target_id, and the anchors/paths inside pane-layout
// blobs — are rewritten through the mapping. The old files are renamed
// aside (db.pre-one-node), never deleted; the source cache is disposable and
// goes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/remote"
)

// idMap remaps one plugin's old grid and tile ids to the new ones.
type idMap struct {
	grids map[int64]int64
	tiles map[int64]int64
}

// Convert builds <home>/gridwell.db from the legacy layout. It is
// idempotent in effect: a home with no db/ dir is not converted, and a
// finished conversion leaves db.pre-one-node behind.
func Convert(home string, cfg *config.ServerConfig) error {
	ctx := context.Background()
	oldHome := filepath.Join(config.LegacyDBDir(home, cfg.ID), "store.db")
	if _, err := os.Stat(oldHome); err != nil {
		return fmt.Errorf("convert: %s has a db/ directory but no home store at %s — is `id` the home's id?", home, oldHome)
	}
	target := config.DBFile(home)
	log.Printf("gridwell: converting %s to the one-database layout (%s)", home, target)

	// 1. The home store, verbatim: a consistent snapshot into the new
	// file (VACUUM INTO — safe against WAL), then the current schema
	// migrates it in place on Open.
	if err := snapshot(oldHome, target); err != nil {
		return fmt.Errorf("convert: home store: %w", err)
	}
	st, err := store.Open(target)
	if err != nil {
		return fmt.Errorf("convert: open %s: %w", target, err)
	}
	defer st.Close()
	if err := pluginmeta.Create(target, cfg.ID, "home"); err != nil {
		return err
	}

	// 2. Each plugin's memory.
	maps := map[string]*idMap{}
	for _, pc := range cfg.Plugins {
		if pc.ID == "" {
			continue
		}
		mem := filepath.Join(config.LegacyDBDir(home, pc.ID), "store.db")
		if _, err := os.Stat(mem); errors.Is(err, fs.ErrNotExist) {
			continue // never served
		}
		m, err := importMemory(ctx, st, pc.ID, mem)
		if err != nil {
			return fmt.Errorf("convert: plugin %s: %w", pc.ID, err)
		}
		maps[pc.ID] = m
		log.Printf("gridwell: convert: plugin %s: %d grids, %d tiles", pc.ID, len(m.grids), len(m.tiles))
	}

	// 3. References into plugins, rewritten.
	if err := rewriteReferences(ctx, st, maps); err != nil {
		return fmt.Errorf("convert: references: %w", err)
	}

	// 4. The connections.
	oldRemote := filepath.Join(config.LegacyDBDir(home, cfg.ID), "remote.db")
	if _, err := os.Stat(oldRemote); err == nil {
		if err := importConnections(ctx, st, oldRemote); err != nil {
			return fmt.Errorf("convert: connections: %w", err)
		}
	}

	// 5. The old layout aside; the cache gone.
	if err := os.Rename(filepath.Join(home, "db"), filepath.Join(home, "db.pre-one-node")); err != nil {
		return fmt.Errorf("convert: set the old db/ aside: %w", err)
	}
	_ = os.RemoveAll(filepath.Join(home, "cache"))
	log.Printf("gridwell: converted; the old files are in %s (delete when satisfied)", filepath.Join(home, "db.pre-one-node"))
	return nil
}

// snapshot copies a SQLite file consistently (VACUUM INTO).
func snapshot(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	return err
}

// importMemory copies one plugin's memory DB into the store under ns,
// re-minting ids; the returned map is what references rewrite through.
func importMemory(ctx context.Context, st *store.Store, ns, path string) (*idMap, error) {
	old, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer old.Close()
	m := &idMap{grids: map[int64]int64{}, tiles: map[int64]int64{}}
	n := st.Namespace(ns)

	// Contexts → grids (context_key), root framing along. A pre-one-node
	// plugin stored a root as the ORIGIN of the 1×1 synthetic doorway the
	// client framed it through, and the client read it back as
	// origin + 1/2 — so the center this store keeps (schema v11) is
	// + 0.5, the picture the user actually had.
	type ctxRow struct {
		id              int64
		key             string
		rcx, rcy, rzoom sql.NullFloat64
	}
	var ctxs []ctxRow
	rows, err := old.QueryContext(ctx, `SELECT grid_id, key, root_cx, root_cy, root_zoom FROM contexts ORDER BY grid_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c ctxRow
		if err := rows.Scan(&c.id, &c.key, &c.rcx, &c.rcy, &c.rzoom); err != nil {
			rows.Close()
			return nil, err
		}
		ctxs = append(ctxs, c)
	}
	rows.Close()
	for _, c := range ctxs {
		gid, err := n.ContextID(c.key)
		if err != nil {
			return nil, err
		}
		m.grids[c.id] = gid
		if c.rzoom.Valid {
			if err := n.SetFraming(0, gid, rpc.Framing{
				Cx: c.rcx.Float64 + 0.5, Cy: c.rcy.Float64 + 0.5, Zoom: c.rzoom.Float64,
			}); err != nil {
				return nil, err
			}
		}
	}

	// The old file's remembered listings give each key its kind, label,
	// child context and url — facts that become DURABLE ROWS here; a key
	// with no remembered entry converts as text named by its key (the
	// next listing refreshes the facts). The listing blob itself does not
	// come along: what the source last said is cache, and the new home
	// keeps none (docs/simplify-plan.md S7).
	facts := map[int64]map[string]*pluginv1.Entry{}
	lrows, err := old.QueryContext(ctx, `SELECT grid_id, entries FROM cache_listings`)
	if err != nil {
		return nil, err
	}
	type listing struct {
		gid  int64
		blob []byte
	}
	var listings []listing
	for lrows.Next() {
		var l listing
		if err := lrows.Scan(&l.gid, &l.blob); err != nil {
			lrows.Close()
			return nil, err
		}
		listings = append(listings, l)
	}
	lrows.Close()
	for _, l := range listings {
		resp := &pluginv1.ListResponse{}
		if err := proto.Unmarshal(l.blob, resp); err != nil {
			continue
		}
		byKey := map[string]*pluginv1.Entry{}
		for _, e := range resp.Entries {
			byKey[e.Key] = e
		}
		facts[l.gid] = byKey
	}

	// idmap + layout → tiles, id-for-id in old order (stable output).
	type tileRow struct {
		id, gid            int64
		key                string
		tomb               int64
		x, y, w, h, vx, vy int64
		vz                 float64
		tx, ty, tw, th     int64
		mode               string
		cz                 float64
	}
	var tiles []tileRow
	trows, err := old.QueryContext(ctx, `SELECT i.tile_id, i.grid_id, i.key, i.tombstoned,
		l.x, l.y, l.w, l.h, l.view_x, l.view_y, l.view_zoom, l.text_x, l.text_y, l.text_w, l.text_h, l.text_mode, l.content_zoom
		FROM idmap i JOIN layout l ON l.tile_id = i.tile_id ORDER BY i.tile_id`)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var t tileRow
		if err := trows.Scan(&t.id, &t.gid, &t.key, &t.tomb, &t.x, &t.y, &t.w, &t.h, &t.vx, &t.vy, &t.vz,
			&t.tx, &t.ty, &t.tw, &t.th, &t.mode, &t.cz); err != nil {
			trows.Close()
			return nil, err
		}
		tiles = append(tiles, t)
	}
	trows.Close()
	for _, t := range tiles {
		gid, ok := m.grids[t.gid]
		if !ok {
			return nil, fmt.Errorf("tile %d names unknown context %d", t.id, t.gid)
		}
		kind, label, child, url := "text", t.key, int64(0), ""
		if e := facts[t.gid][t.key]; e != nil {
			if e.Kind != "" {
				kind = e.Kind
			}
			if e.Label != "" {
				label = e.Label
			}
			url = e.UrlString
			if kind == "well" {
				if e.ChildContext == "" {
					kind = "text"
				} else {
					cg, err := n.ContextID(e.ChildContext)
					if err != nil {
						return nil, err
					}
					child = cg
				}
			}
		}
		if kind == "url" && url == "" {
			kind = "text"
		}
		// The old layout row's view_x/view_y was a window ORIGIN; the
		// center this store keeps is origin + footprint/2 — the same
		// arithmetic schema v11's migration applies to a home file.
		newID, err := st.InsertExternalRow(ctx, ns, gid, t.key, kind, label, child, url, t.tomb != 0,
			[4]int64{t.x, t.y, t.w, t.h},
			rpc.Framing{Cx: float64(t.vx) + float64(t.w)/2, Cy: float64(t.vy) + float64(t.h)/2, Zoom: t.vz},
			[4]int64{t.tx, t.ty, t.tw, t.th}, t.mode, t.cz)
		if err != nil {
			return nil, fmt.Errorf("tile %d (%s): %w", t.id, t.key, err)
		}
		m.tiles[t.id] = newID
	}
	return m, nil
}

// rewriteReferences maps every home reference into a plugin (exit wells'
// child_grid_id, leaf links' link_target_id, pane-layout anchors and
// paths) through the plugin's id map. A reference with no mapping is
// left as it was — dangling, never re-pointed.
func rewriteReferences(ctx context.Context, st *store.Store, maps map[string]*idMap) error {
	if len(maps) == 0 {
		return nil
	}
	remap := func(ref string, grid bool) (string, bool) {
		ns, local, ok := strings.Cut(ref, "/")
		if !ok {
			return ref, false
		}
		m, ok := maps[ns]
		if !ok {
			return ref, false
		}
		n, err := strconv.ParseInt(local, 10, 64)
		if err != nil {
			return ref, false
		}
		table := m.tiles
		if grid {
			table = m.grids
		}
		if nn, ok := table[n]; ok {
			return ns + "/" + strconv.FormatInt(nn, 10), true
		}
		return ref, false
	}
	db := st.SQL()
	for _, col := range []struct {
		name string
		grid bool
	}{{"child_grid_id", true}, {"link_target_id", false}} {
		rows, err := db.QueryContext(ctx, `SELECT id, `+col.name+` FROM tiles WHERE ns = '' AND typeof(`+col.name+`) = 'text' AND instr(`+col.name+`, '/') > 0`)
		if err != nil {
			return err
		}
		type upd struct {
			id  int64
			ref string
		}
		var upds []upd
		for rows.Next() {
			var u upd
			if err := rows.Scan(&u.id, &u.ref); err != nil {
				rows.Close()
				return err
			}
			if nr, ok := remap(u.ref, col.grid); ok {
				upds = append(upds, upd{u.id, nr})
			}
		}
		rows.Close()
		for _, u := range upds {
			if _, err := db.ExecContext(ctx, `UPDATE tiles SET `+col.name+` = ? WHERE id = ?`, u.ref, u.id); err != nil {
				return err
			}
		}
	}
	// Pane-layout blobs: anchors are grids, paths are tiles.
	rows, err := db.QueryContext(ctx, `SELECT b.id, b.data FROM blobs b JOIN tiles t ON t.blob_id = b.id WHERE t.ns = '' AND t.kind = 'pane'`)
	if err != nil {
		return err
	}
	type blobUpd struct {
		id   int64
		data []byte
	}
	var blobs []blobUpd
	for rows.Next() {
		var b blobUpd
		if err := rows.Scan(&b.id, &b.data); err != nil {
			rows.Close()
			return err
		}
		blobs = append(blobs, b)
	}
	rows.Close()
	for _, b := range blobs {
		var doc any
		if err := json.Unmarshal(b.data, &doc); err != nil {
			continue
		}
		changed := false
		var walk func(v any, key string) any
		walk = func(v any, key string) any {
			switch x := v.(type) {
			case map[string]any:
				for k, vv := range x {
					x[k] = walk(vv, k)
				}
				return x
			case []any:
				for i := range x {
					x[i] = walk(x[i], key)
				}
				return x
			case string:
				switch key {
				case "anchor":
					if nr, ok := remap(x, true); ok {
						changed = true
						return nr
					}
				case "path":
					if nr, ok := remap(x, false); ok {
						changed = true
						return nr
					}
				}
			}
			return v
		}
		doc = walk(doc, "")
		if !changed {
			continue
		}
		out, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `UPDATE blobs SET data = ? WHERE id = ?`, out, b.id); err != nil {
			return err
		}
	}
	return nil
}

// importConnections copies the transport's remembered rows.
func importConnections(ctx context.Context, st *store.Store, path string) error {
	old, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer old.Close()
	db, err := remote.NewDB(st.SQL())
	if err != nil {
		return err
	}
	rows, err := old.QueryContext(ctx, `SELECT name, remote_root, deleted FROM connections`)
	if err != nil {
		return nil // an older transport store (no connections table): nothing remembered
	}
	type row struct {
		name, root string
		del        int64
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.root, &r.del); err != nil {
			rows.Close()
			return err
		}
		rs = append(rs, r)
	}
	rows.Close()
	for _, r := range rs {
		if err := db.Ensure(ctx, r.name); err != nil {
			return err
		}
		if r.root != "" {
			if err := db.SetRemoteRoot(ctx, r.name, r.root); err != nil {
				return err
			}
		}
		if r.del != 0 {
			if err := db.Tombstone(ctx, r.name); err != nil {
				return err
			}
		}
	}
	return nil
}
