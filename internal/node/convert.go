package node

// Convert folds a home laid out as one database per namespace into the single
// database. <home>/db/<id>/store.db, the home store, becomes
// <home>/gridwell.db; every plugin's memory DB becomes rows under its
// namespace; the transport's connections come along. Home keeps every id. A
// plugin's grids and tiles are re-minted, since there is now one table and
// one sequence, and the references into them — home tiles' child_grid_id for
// exit wells, link_target_id, and the anchors and paths inside pane-layout
// blobs — are rewritten through the mapping. The old files are renamed aside
// as db.pre-one-node, never deleted, and the disposable source cache goes.

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

// Convert builds <home>/gridwell.db from the per-namespace layout. It is
// idempotent in effect: a home with no db/ dir is not converted, and a
// finished conversion leaves db.pre-one-node behind.
//
// It is crash-safe. Everything is built into <home>/gridwell.db.converting;
// the real file appears only when the whole conversion has succeeded, through
// one rename of a checkpointed, closed, fsynced file. A kill anywhere before
// that rename leaves only the temp, which the next attempt removes and
// rebuilds from the untouched db/ — never a half-converted store the next
// boot would silently open. A kill in the one remaining window, between that
// rename and setting db/ aside, leaves a COMPLETE store beside the old
// layout; ensureStore finishes the set-aside rather than converting again.
func Convert(home string, cfg *config.ServerConfig) error {
	return convert(home, cfg, nil)
}

// The steps a conversion passes through, in order. They are the points a
// SIGTERM can land on, so they are also what the crash tests abort after.
const (
	stepSnapshot    = "snapshot"    // the home store copied into the temp
	stepIdentity    = "identity"    // the temp stamped as this node's home
	stepPlugin      = "plugin:"     // + the plugin id, once per imported memory
	stepReferences  = "references"  // home references rewritten through the maps
	stepConnections = "connections" // the transport's rows carried over
	stepCommitted   = "committed"   // the temp renamed onto gridwell.db
)

// convert is Convert with a test hook. afterStep runs after each named step
// above and its error aborts the conversion right there, which is how
// convert_crash_test.go stands in for a kill at that point. Production passes
// nil.
func convert(home string, cfg *config.ServerConfig, afterStep func(step string) error) error {
	ctx := context.Background()
	step := func(name string) error {
		if afterStep == nil {
			return nil
		}
		return afterStep(name)
	}
	oldHome := filepath.Join(config.LegacyDBDir(home, cfg.ID), "store.db")
	if _, err := os.Stat(oldHome); err != nil {
		return fmt.Errorf("convert: %s has a db/ directory but no home store at %s — is `id` the home's id?", home, oldHome)
	}
	target := config.DBFile(home)
	tmp := target + ".converting"
	log.Printf("gridwell: converting %s to the one-database layout (%s)", home, target)

	// A temp left by an interrupted attempt is scrap: the old layout it was
	// built from is still there, untouched, so the only safe thing to do with
	// a partial file is throw it away and start over. VACUUM INTO refuses an
	// existing destination anyway.
	if err := removeSQLiteFile(tmp); err != nil {
		return fmt.Errorf("convert: clear a previous attempt at %s: %w", tmp, err)
	}

	// 1. The home store, verbatim: a consistent snapshot into the new file
	// through VACUUM INTO, which is safe against WAL, then the current
	// schema migrates it in place on Open.
	if err := snapshot(oldHome, tmp); err != nil {
		return fmt.Errorf("convert: home store: %w", err)
	}
	if err := step(stepSnapshot); err != nil {
		return err
	}
	st, err := store.Open(tmp)
	if err != nil {
		return fmt.Errorf("convert: open %s: %w", tmp, err)
	}
	open := true
	defer func() {
		if open {
			st.Close()
		}
	}()
	if err := pluginmeta.Create(tmp, cfg.ID, "home"); err != nil {
		return err
	}
	if err := step(stepIdentity); err != nil {
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
			continue // this plugin never served
		}
		m, err := importMemory(ctx, st, pc.ID, mem)
		if err != nil {
			return fmt.Errorf("convert: plugin %s: %w", pc.ID, err)
		}
		maps[pc.ID] = m
		log.Printf("gridwell: convert: plugin %s: %d grids, %d tiles", pc.ID, len(m.grids), len(m.tiles))
		if err := step(stepPlugin + pc.ID); err != nil {
			return err
		}
	}

	// 3. References into plugins, rewritten.
	if err := rewriteReferences(ctx, st, maps); err != nil {
		return fmt.Errorf("convert: references: %w", err)
	}
	if err := step(stepReferences); err != nil {
		return err
	}

	// 4. The connections.
	oldRemote := filepath.Join(config.LegacyDBDir(home, cfg.ID), "remote.db")
	if _, err := os.Stat(oldRemote); err == nil {
		if err := importConnections(ctx, st, oldRemote); err != nil {
			return fmt.Errorf("convert: connections: %w", err)
		}
	}
	if err := step(stepConnections); err != nil {
		return err
	}

	// 5. Everything succeeded: fold the WAL back into the file, close it, get
	// the bytes on the platter, and publish it with one rename. This is the
	// instant the conversion becomes real.
	if err := checkpointAndClose(st, tmp); err != nil {
		return fmt.Errorf("convert: finish %s: %w", tmp, err)
	}
	open = false
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("convert: publish %s: %w", target, err)
	}
	if err := syncDir(home); err != nil {
		return fmt.Errorf("convert: sync %s: %w", home, err)
	}
	if err := step(stepCommitted); err != nil {
		return err
	}

	// 6. The old layout aside; the cache gone.
	if err := setAsideOldLayout(home); err != nil {
		return err
	}
	log.Printf("gridwell: converted; the old files are in %s (delete when satisfied)", filepath.Join(home, "db.pre-one-node"))
	return nil
}

// setAsideOldLayout retires the per-namespace layout once gridwell.db is the
// real file, and drops the disposable source cache. It is the second half of
// the commit, and ensureStore calls it too: a kill between the two renames
// leaves a complete store beside a db/ that still needs retiring, and
// finishing that is the only correct response — never a second conversion
// over live data. The old files are renamed, never deleted, so a name already
// taken by an earlier fold is stepped past rather than overwritten.
func setAsideOldLayout(home string) error {
	old := filepath.Join(home, "db")
	aside := filepath.Join(home, "db.pre-one-node")
	for i := 2; ; i++ {
		if _, err := os.Stat(aside); errors.Is(err, fs.ErrNotExist) {
			break
		}
		aside = filepath.Join(home, "db.pre-one-node."+strconv.Itoa(i))
	}
	if err := os.Rename(old, aside); err != nil {
		return fmt.Errorf("convert: set the old db/ aside: %w", err)
	}
	_ = os.RemoveAll(filepath.Join(home, "cache"))
	return nil
}

// checkpointAndClose folds the WAL back into the main file and closes the
// store, so the single file about to be renamed carries every write. A -wal
// or -shm sidecar does not survive the rename with its database, and a
// checkpointed one has nothing left to say.
func checkpointAndClose(st *store.Store, path string) error {
	if _, err := st.SQL().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		st.Close()
		return err
	}
	if err := st.Close(); err != nil {
		return err
	}
	if err := syncFile(path); err != nil {
		return err
	}
	// A clean close of the last connection removes them; anything left is
	// empty after the TRUNCATE checkpoint above, and must not travel.
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// removeSQLiteFile removes a database and its WAL sidecars.
func removeSQLiteFile(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// syncFile / syncDir put bytes and directory entries on the platter, so the
// rename that publishes the conversion cannot be reordered ahead of the
// contents it publishes.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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

	// Contexts become grids, keyed by context_key, with the root framing
	// alongside. The old layout stored a root as the origin of the 1x1
	// synthetic doorway the client framed it through, and the client read it
	// back as origin + 1/2, so the center this store keeps is + 0.5: the
	// picture the user actually had.
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
	// child context, and url — facts that become durable rows here. A key
	// with no remembered entry converts as text named by its key, and the
	// next listing refreshes the facts. The listing blob itself does not come
	// along: what the source last said is cache, and the durable file keeps
	// none of it.
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

	// idmap and layout become tiles, id-for-id in the old order, so the
	// output is stable.
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
		// The old layout row's view_x and view_y were a window origin; the
		// center this store keeps is origin + footprint/2, the same
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

// rewriteReferences maps every home reference into a plugin — an exit well's
// child_grid_id, a leaf link's link_target_id, a pane layout's anchors and
// paths — through the plugin's id map. A reference with no mapping is left as
// it was: dangling, never re-pointed.
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
		return nil // an older transport store with no connections table
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
