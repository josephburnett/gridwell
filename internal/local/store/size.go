package store

// What gridwell.db holds, and the one removal of what nothing reaches. A grid
// is reached from a singleton (root, trash, templates) or a files row,
// through the doors that own their grids (tileRefs); an ephemeral visit, a row
// on the scratch grid, is reached while a reached workspace tile's layout
// names it (layoutRefs); a blob is kept while a row names it. The trash and a
// tombstoned file row are reached, by decision.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
)

// Places a tile row is counted under, in report order. Unreached and
// GoneGrid are what nothing reaches.
const (
	PlaceHome       = "home"
	PlaceScratch    = "scratch"
	PlaceTrash      = "trash"
	PlaceTemplates  = "templates"
	PlaceFiles      = "files"
	PlaceTombstoned = "tombstoned"
	PlaceUnreached  = "unreached"
	PlaceGoneGrid   = "gone_grid"
)

// Places is every place, in report order.
var Places = []string{PlaceHome, PlaceScratch, PlaceTrash, PlaceTemplates, PlaceFiles, PlaceTombstoned, PlaceUnreached, PlaceGoneGrid}

// Blobs is a set of blobs: how many, and their data's bytes.
type Blobs struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

func (b *Blobs) add(n int64) { b.Count++; b.Bytes += n }

// TableBytes is one table's pages, its indexes' folded in, by dbstat.
type TableBytes struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Analysis is what gridwell.db holds, read in one snapshot.
type Analysis struct {
	PageSize  int64                     `json:"page_size"`
	Pages     int64                     `json:"pages"`
	FreePages int64                     `json:"free_pages"`
	Tables    []TableBytes              `json:"tables"`
	Kinds     map[string]map[string]int `json:"kinds"`         // kind → place → rows
	Content   Blobs                     `json:"content_blobs"` // named by a kept row's blob_id
	Previews  Blobs                     `json:"preview_blobs"` // named by a kept row's preview_blob_id
	TrashOnly Blobs                     `json:"trash_only_blobs"`
	// What nothing reaches, which Clean removes.
	UnreachedGrids   int   `json:"unreached_grids"`
	UnreachedScratch int   `json:"unreached_scratch"` // ephemeral visits no layout names, among the unreached
	Unnamed          Blobs `json:"unnamed_blobs"`
	UnreachedOnly    Blobs `json:"unreached_only_blobs"` // named only by unreached or gone-grid rows
	// Found and reported, never removed: Clean sets a refcount to its count
	// of naming rows, and a row naming a gone blob or grid is the user's.
	RefcountDrift int `json:"refcount_drift"`
	MissingBlobs  int `json:"rows_naming_a_gone_blob"`
	MissingGrids  int `json:"doors_owning_a_gone_grid"`
}

// Removable is how many rows Clean would remove.
func (a Analysis) Removable() int {
	n := a.UnreachedGrids + a.Unnamed.Count + a.UnreachedOnly.Count
	for _, places := range a.Kinds {
		n += places[PlaceUnreached] + places[PlaceGoneGrid]
	}
	return n
}

// cleanPlan is what analyze found for Clean to remove, and kept, the ids of
// every row and blob it must leave as they are.
type cleanPlan struct {
	goneGrid  []sizeTile
	scratch   []sizeTile // ephemeral visits no layout names
	unreached []int64
	unnamed   []int64
	drift     map[int64]int64
	kept      string
}

type sizeTile struct {
	id, grid      int64
	kind, key     string
	child         sql.NullString
	blob, preview sql.NullInt64
	tombstoned    bool
}

// OpenReadOnly opens the database at path for reading alone, beside a node
// that may be serving it: no migration, no mint, no write.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now, newID: newUUID, hub: eventhub.New(rpc.EventKey)}
	if err := s.applyMigrations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s is not at this binary's format (a serve brings it forward): %w", path, err)
	}
	if err := s.verifySchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Analysis reads what the database holds. A layout names rows by the
// node's config id, so a store not given it (SetNodeID) holds every visit.
func (s *Store) Analysis(ctx context.Context) (Analysis, error) {
	var a Analysis
	err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
		var err error
		a, _, err = analyze(ctx, tx, s.nodeID)
		return err
	})
	return a, err
}

// Clean removes what nothing reaches, in one transaction, through the same
// teardown a delete runs, and refuses whole when the removal would touch a
// kept row or blob.
func (s *Store) Clean(ctx context.Context) error {
	return s.withMutation(ctx, "Clean", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		_, p, err := analyze(ctx, tx, s.nodeID)
		if err != nil {
			return err
		}
		for id, n := range p.drift {
			if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = ? WHERE id = ?`, n, id); err != nil {
				return err
			}
		}
		for _, t := range p.goneGrid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, t.id); err != nil {
				return err
			}
			if err := s.decTileRefs(ctx, tx, t.kind, t.child.String, nullToInt(t.blob), nullToInt(t.preview)); err != nil {
				return err
			}
		}
		for _, t := range p.scratch {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, t.id); err != nil {
				return err
			}
			if err := s.decTileRefs(ctx, tx, t.kind, t.child.String, nullToInt(t.blob), nullToInt(t.preview)); err != nil {
				return err
			}
			if _, _, err := leaveSelection(ctx, tx, t.grid, t.id); err != nil {
				return err
			}
		}
		for _, g := range p.unreached {
			if err := s.deleteGrid(ctx, tx, g); err != nil {
				return err
			}
		}
		for _, b := range p.unnamed {
			if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, b); err != nil {
				return err
			}
		}
		a, after, err := analyze(ctx, tx, s.nodeID)
		if err != nil {
			return err
		}
		if after.kept != p.kept || a.Removable() != 0 {
			return fmt.Errorf("store: clean would change what is reached, so nothing is removed")
		}
		return nil
	})
}

// Compact rewrites the file without its free pages. It is the one write on
// the handle beside withMutation: VACUUM rewrites pages, not rows, and
// cannot run in a transaction.
func (s *Store) Compact(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func analyze(ctx context.Context, q gridReader, node string) (Analysis, cleanPlan, error) {
	a := Analysis{Kinds: map[string]map[string]int{}}
	p := cleanPlan{drift: map[int64]int64{}}
	for _, pr := range []struct {
		name string
		v    *int64
	}{{"page_size", &a.PageSize}, {"page_count", &a.Pages}, {"freelist_count", &a.FreePages}} {
		if err := q.QueryRowContext(ctx, "PRAGMA "+pr.name).Scan(pr.v); err != nil {
			return a, p, err
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT coalesce(s.tbl_name, d.name), sum(d.pgsize) FROM dbstat d
		LEFT JOIN sqlite_schema s ON s.name = d.name GROUP BY 1 ORDER BY 2 DESC, 1`)
	if err != nil {
		return a, p, fmt.Errorf("dbstat: %w", err)
	}
	if a.Tables, err = collect(rows, func(r *sql.Rows) (TableBytes, error) {
		var t TableBytes
		return t, r.Scan(&t.Name, &t.Bytes)
	}); err != nil {
		return a, p, err
	}

	place := map[int64]string{} // reached grid → its place
	var queue []int64
	reach := func(g int64, where string) {
		if _, ok := place[g]; !ok {
			place[g] = where
			queue = append(queue, g)
		}
	}
	for key, where := range map[string]string{systemKeyRootGridID: PlaceHome, systemKeyTrashGridID: PlaceTrash,
		systemKeyTemplatesGridID: PlaceTemplates} {
		g, err := singletonGrid(ctx, q, key)
		if err != nil {
			return a, p, err
		}
		reach(g, where)
	}
	// The scratch grid is kept; its rows are reached one by one, below.
	scratch, err := singletonGrid(ctx, q, systemKeyScratchGridID)
	if err != nil {
		return a, p, err
	}
	place[scratch] = PlaceScratch
	rows, err = q.QueryContext(ctx, `SELECT id, key FROM grids`)
	if err != nil {
		return a, p, err
	}
	type gridRow struct {
		id  int64
		key string
	}
	grids, err := collect(rows, func(r *sql.Rows) (gridRow, error) {
		var g gridRow
		return g, r.Scan(&g.id, &g.key)
	})
	if err != nil {
		return a, p, err
	}
	exists := map[int64]bool{}
	for _, g := range grids {
		exists[g.id] = true
		if g.key != "" {
			reach(g.id, PlaceFiles)
		}
	}

	rows, err = q.QueryContext(ctx, `SELECT id, grid_id, kind, key, child_grid_id, blob_id, preview_blob_id, tombstoned FROM tiles`)
	if err != nil {
		return a, p, err
	}
	tiles, err := collect(rows, func(r *sql.Rows) (sizeTile, error) {
		var t sizeTile
		return t, r.Scan(&t.id, &t.grid, &t.kind, &t.key, &t.child, &t.blob, &t.preview, &t.tombstoned)
	})
	if err != nil {
		return a, p, err
	}
	onGrid := map[int64][]sizeTile{}
	for _, t := range tiles {
		onGrid[t.grid] = append(onGrid[t.grid], t)
	}
	layoutOf, err := workspaceLayouts(ctx, q)
	if err != nil {
		return a, p, err
	}
	visit := map[int64]bool{}
	for _, t := range onGrid[scratch] {
		visit[t.id] = true
	}
	// A visit a reached layout names is held, and its door reaches its grid,
	// whose workspace tiles may hold more. A layout that will not read could
	// name any visit, so then every one is held, as the boot sweep then
	// reaps none; so does every layout when node is unknown.
	held, named, scanned, unreadable := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}, node == ""
	for progress := true; progress; {
		progress = false
		for ; len(queue) > 0; queue = queue[1:] {
			for _, t := range onGrid[queue[0]] {
				if g, _ := tileRefs(t.kind, t.child.String, 0, 0); g != 0 && exists[g] {
					reach(g, place[queue[0]])
				}
			}
		}
		for _, t := range tiles {
			data, ok := layoutOf[t.id]
			if !ok || scanned[t.id] || place[t.grid] == "" || !exists[t.grid] || (visit[t.id] && !held[t.id]) {
				continue
			}
			scanned[t.id], progress = true, true
			ids, readable := layoutRefs(node, data)
			unreadable = unreadable || !readable
			for _, id := range ids {
				if n, err := parseID(id); err == nil {
					named[n] = true
				}
			}
		}
		for _, t := range onGrid[scratch] {
			if !held[t.id] && (unreadable || named[t.id]) {
				held[t.id], progress = true, true
				if g, _ := tileRefs(t.kind, t.child.String, 0, 0); g != 0 && exists[g] {
					reach(g, PlaceScratch)
				}
			}
		}
	}

	rows, err = q.QueryContext(ctx, `SELECT id, length(data), refcount FROM blobs`)
	if err != nil {
		return a, p, err
	}
	type blobRow struct{ id, bytes, refcount int64 }
	blobs, err := collect(rows, func(r *sql.Rows) (blobRow, error) {
		var b blobRow
		return b, r.Scan(&b.id, &b.bytes, &b.refcount)
	})
	if err != nil {
		return a, p, err
	}
	size := map[int64]int64{}
	for _, b := range blobs {
		size[b.id] = b.bytes
	}

	type naming struct {
		rows, kept, trash int64
		content, preview  bool
	}
	names := map[int64]naming{}
	var kept []string
	for _, t := range tiles {
		where := place[t.grid]
		switch {
		case t.tombstoned:
			where = PlaceTombstoned
		case !exists[t.grid]:
			where = PlaceGoneGrid
			p.goneGrid = append(p.goneGrid, t)
		case where == "", visit[t.id] && !held[t.id]:
			where = PlaceUnreached
			if visit[t.id] {
				a.UnreachedScratch++
				p.scratch = append(p.scratch, t)
			}
		case t.key != "":
			where = PlaceFiles
		}
		if a.Kinds[t.kind] == nil {
			a.Kinds[t.kind] = map[string]int{}
		}
		a.Kinds[t.kind][where]++
		keep := where != PlaceUnreached && where != PlaceGoneGrid
		if keep {
			kept = append(kept, "t"+strconv.FormatInt(t.id, 10))
			if g, _ := tileRefs(t.kind, t.child.String, 0, 0); g != 0 && !exists[g] {
				a.MissingGrids++
			}
		}
		for i, col := range []sql.NullInt64{t.blob, t.preview} {
			if _, ok := size[col.Int64]; !col.Valid || !ok {
				a.MissingBlobs += int(boolToInt(col.Valid))
				continue
			}
			n := names[col.Int64]
			n.rows++
			if keep {
				n.kept++
				n.content, n.preview = n.content || i == 0, n.preview || i == 1
				n.trash += boolToInt(where == PlaceTrash)
			}
			names[col.Int64] = n
		}
	}
	for _, b := range blobs {
		n, named := names[b.id]
		switch {
		case !named:
			a.Unnamed.add(b.bytes)
			p.unnamed = append(p.unnamed, b.id)
		case n.kept == 0:
			a.UnreachedOnly.add(b.bytes)
		default:
			kept = append(kept, "b"+strconv.FormatInt(b.id, 10))
		}
		if n.content {
			a.Content.add(b.bytes)
		}
		if n.preview {
			a.Previews.add(b.bytes)
		}
		if n.trash > 0 && n.trash == n.kept {
			a.TrashOnly.add(b.bytes)
		}
		if b.refcount != n.rows {
			a.RefcountDrift++
			p.drift[b.id] = n.rows
		}
	}
	for _, g := range grids {
		if _, ok := place[g.id]; ok {
			kept = append(kept, "g"+strconv.FormatInt(g.id, 10))
		} else {
			a.UnreachedGrids++
			p.unreached = append(p.unreached, g.id)
		}
	}
	sort.Strings(kept)
	p.kept = strings.Join(kept, ",")
	return a, p, nil
}
