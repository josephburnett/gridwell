package store

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/eventhub"
)

// This file is the migration test harness: it builds genuine "old files" from
// the frozen tablesV1 text, walks them forward with the real production engine
// (Store.migrateUp), and fingerprints schemas so a migrated DB can be compared
// to a fresh one. The Test* functions that use it live in migrations_test.go.

// ── schema fingerprint (order-insensitive) ───────────────────────────────────
//
// colFP and the column/table readers are the production guard's (schema_check.go)
// — shared so the equivalence tests and the startup check fingerprint columns the
// same way.

// idxFP is an index's identity: uniqueness + its ordered column list.
type idxFP struct {
	unique bool
	cols   string
}

// tableFP fingerprints one table: its columns by name, its indexes by name, and
// how many foreign keys it declares (count is enough for additive evolution).
type tableFP struct {
	columns map[string]colFP
	indexes map[string]idxFP
	fkCount int
}

// schemaFingerprint reads a semantic fingerprint of every user table via PRAGMA
// introspection. Compared with compareFingerprints, never by raw sqlite_master
// SQL text (which diverges by construction between inline DDL and ADD COLUMN).
func schemaFingerprint(t *testing.T, db *sql.DB) map[string]tableFP {
	t.Helper()
	out := map[string]tableFP{}
	for _, name := range userTables(t, db) {
		out[name] = tableFP{
			columns: tableColumnsFP(t, db, name),
			indexes: tableIndexesFP(t, db, name),
			fkCount: foreignKeyCount(t, db, name),
		}
	}
	return out
}

// userTables / tableColumnsFP are t.Fatal-on-error wrappers over the production
// readers in schema_check.go, so the fingerprint tests share one column-reading
// path with the startup guard.
func userTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	names, err := userTableNames(context.Background(), db)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return names
}

func tableColumnsFP(t *testing.T, db *sql.DB, table string) map[string]colFP {
	t.Helper()
	cols, err := tableColumnFPs(context.Background(), db, table)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	return cols
}

func tableIndexesFP(t *testing.T, db *sql.DB, table string) map[string]idxFP {
	t.Helper()
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		t.Fatalf("index_list %s: %v", table, err)
	}
	type idxRow struct {
		name   string
		unique bool
	}
	var idxs []idxRow
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list: %v", err)
		}
		idxs = append(idxs, idxRow{name: name, unique: unique != 0})
	}
	rows.Close()

	out := map[string]idxFP{}
	for _, ix := range idxs {
		out[ix.name] = idxFP{unique: ix.unique, cols: indexColumns(t, db, ix.name)}
	}
	return out
}

func indexColumns(t *testing.T, db *sql.DB, index string) string {
	t.Helper()
	rows, err := db.Query("PRAGMA index_info(" + index + ")")
	if err != nil {
		t.Fatalf("index_info %s: %v", index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
		)
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info: %v", err)
		}
		cols = append(cols, name.String) // index_info yields columns in order
	}
	return strings.Join(cols, ",")
}

func foreignKeyCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatalf("foreign_key_list %s: %v", table, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n
}

// compareFingerprints fails t for every divergence between a fresh schema
// (want) and a migrated one (got), naming the offending table.column so a
// forgotten inline-DDL edit or migration is immediately diagnosable.
func compareFingerprints(t *testing.T, want, got map[string]tableFP) {
	t.Helper()
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("table %q present in fresh schema, missing after migration", name)
			continue
		}
		for col, wc := range w.columns {
			gc, ok := g.columns[col]
			if !ok {
				t.Errorf("%s.%s missing after migration", name, col)
				continue
			}
			if wc != gc {
				t.Errorf("%s.%s differs: fresh=%+v migrated=%+v", name, col, wc, gc)
			}
		}
		for col := range g.columns {
			if _, ok := w.columns[col]; !ok {
				t.Errorf("%s.%s present after migration but absent in fresh schema", name, col)
			}
		}
		if !reflect.DeepEqual(w.indexes, g.indexes) {
			t.Errorf("%s indexes differ: fresh=%v migrated=%v", name, w.indexes, g.indexes)
		}
		if w.fkCount != g.fkCount {
			t.Errorf("%s foreign-key count differs: fresh=%d migrated=%d", name, w.fkCount, g.fkCount)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("table %q present after migration but absent in fresh schema", name)
		}
	}
}

// ── building and migrating old DBs ────────────────────────────────────────────

// storeOver wraps an already-open *sql.DB in a Store with deterministic
// clock/IDs, so the harness can drive real internal methods (bootstrapRoot,
// migrateUp) against a hand-built file.
func storeOver(db *sql.DB) *Store {
	s := &Store{db: db, hub: eventhub.New(eventKey)}
	seedDeterministic(s)
	return s
}

// sessionDDLV1 is the FROZEN `session` table as every pre-v10 binary
// created it: one Chromium session per DB, moved by GetSession/PutSession
// (both RPCs retired 2026-07-26). Production stopped creating the table at
// schema v10 and the v10 migration DROPS it — but a genuine old file HAS
// it, so the harness must still build one that does, or the drop would be
// tested against a table that was never there.
const sessionDDLV1 = `
CREATE TABLE IF NOT EXISTS session (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    data BLOB NOT NULL
);
`

// buildDBAtV1 creates a file DB whose tables come from the FROZEN tablesV1 text
// (never tablesTemplate), bootstraps a root grid, and stamps our application_id
// + user_version=1 — a faithful "DB written by a v1 binary". Returns the open
// DB (closed via t.Cleanup) and the root grid id.
func buildDBAtV1(t *testing.T, path string) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v1 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	for _, ddl := range []string{pragmas, systemDDL, tablesV1, sessionDDLV1} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply v1 ddl: %v", err)
		}
	}
	// The v1 root is seeded with RAW inserts against the v1 columns, not
	// through bootstrapRoot: the production bootstrap writes the CURRENT
	// grids shape, which no longer carries the (NOT NULL) v1 object_id.
	// A genuine old file is built from the frozen text, not from today's
	// writer.
	res, err := db.ExecContext(ctx,
		`INSERT INTO grids (object_id, created_at, updated_at) VALUES ('v1-root', 100, 100)`)
	if err != nil {
		t.Fatalf("seed v1 root grid: %v", err)
	}
	rootRow := mustID(t, res)
	// The root framing keys a v1 binary wrote. They are string literals
	// because the constants are gone: schema v11 moved home's root
	// framing onto its grid row and retired the keys. A genuine old file
	// still has them, so the chain must still be fed them.
	for _, kv := range [][2]string{
		{systemKeyRootGridID, strconv.FormatInt(rootRow, 10)},
		{"root_view_cx", "0"},
		{"root_view_cy", "0"},
		{"root_zoom", "0"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO system (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			t.Fatalf("seed v1 system key %s: %v", kv[0], err)
		}
	}
	s := storeOver(db)
	if err := s.setPragmaInt(ctx, "application_id", applicationID); err != nil {
		t.Fatalf("stamp application_id: %v", err)
	}
	if err := s.setPragmaInt(ctx, "user_version", 1); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	root, err := rootGridID(ctx, db)
	if err != nil {
		t.Fatalf("root grid id: %v", err)
	}
	return db, strconv.FormatInt(root, 10)
}

// applyMigrationsUpTo runs the real production engine (migrateUp) over the
// canonical migration list, stopping at version n. Used to place a DB at a
// specific historical version for per-migration tests.
func applyMigrationsUpTo(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	if err := storeOver(db).migrateUp(context.Background(), migrations, n); err != nil {
		t.Fatalf("migrate up to v%d: %v", n, err)
	}
}

// assertColumn fails unless table has a column of the given name.
func assertColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	if _, ok := tableColumnsFP(t, db, table)[column]; !ok {
		t.Errorf("%s.%s: expected column not present after migration", table, column)
	}
}

// addColumn builds a migration run-func that executes a single DDL statement —
// the common ALTER TABLE ADD COLUMN shape.
func addColumn(ddl string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}
}

// ── representative v1 data ────────────────────────────────────────────────────

// v1Seed captures the ids and expected values of rows seeded into a v1 DB, so a
// later verify can assert they survived a migration byte-for-byte.
type v1Seed struct {
	textTileID  int64
	textBlobID  int64
	textBytes   []byte
	textMedia   string
	urlTileID   int64
	urlString   string
	wellTileID  int64
	childGridID int64
}

// seedV1 inserts one of each data kind the user cares about — a text tile (+its
// content blob), a url tile, and an interior well (+its child grid) — into the
// root grid using raw INSERTs against the frozen v1 columns. Raw (not Store
// methods) so it keeps working unchanged as the live schema grows past v1.
func seedV1(t *testing.T, db *sql.DB, rootID string) v1Seed {
	t.Helper()
	ctx := context.Background()
	root, err := strconv.ParseInt(rootID, 10, 64)
	if err != nil {
		t.Fatalf("parse root id: %v", err)
	}
	const now = 1735689600 // 2025-01-01, fixed
	fx := v1Seed{
		textBytes: []byte("# seeded markdown"),
		textMedia: "text/markdown",
		urlString: "https://seed.example",
	}

	res, err := db.ExecContext(ctx,
		`INSERT INTO blobs (hash, data, refcount, media_type) VALUES (?, ?, 1, ?)`,
		"seedhash", fx.textBytes, fx.textMedia)
	if err != nil {
		t.Fatalf("seed blob: %v", err)
	}
	fx.textBlobID = mustID(t, res)

	res, err = db.ExecContext(ctx,
		`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, blob_id, alt_text, created_at, updated_at)
		 VALUES ('seed-text', ?, 'text', 0, 0, 1, 1, ?, 'seed', ?, ?)`,
		root, fx.textBlobID, now, now)
	if err != nil {
		t.Fatalf("seed text tile: %v", err)
	}
	fx.textTileID = mustID(t, res)

	res, err = db.ExecContext(ctx,
		`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, url_string, created_at, updated_at)
		 VALUES ('seed-url', ?, 'url', 2, 0, 1, 1, ?, ?, ?)`,
		root, fx.urlString, now, now)
	if err != nil {
		t.Fatalf("seed url tile: %v", err)
	}
	fx.urlTileID = mustID(t, res)

	res, err = db.ExecContext(ctx,
		`INSERT INTO grids (object_id, created_at, updated_at) VALUES ('seed-child', ?, ?)`,
		now, now)
	if err != nil {
		t.Fatalf("seed child grid: %v", err)
	}
	fx.childGridID = mustID(t, res)

	res, err = db.ExecContext(ctx,
		`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, child_grid_id, created_at, updated_at)
		 VALUES ('seed-well', ?, 'well', 4, 0, 1, 1, ?, ?, ?)`,
		root, fx.childGridID, now, now)
	if err != nil {
		t.Fatalf("seed well tile: %v", err)
	}
	fx.wellTileID = mustID(t, res)
	return fx
}

// verifyV1Survived re-reads the seeded rows with raw SELECTs over the v1 columns
// (so it works at any schema version) and asserts every value is unchanged.
func verifyV1Survived(t *testing.T, db *sql.DB, fx v1Seed) {
	t.Helper()
	ctx := context.Background()

	var (
		data  []byte
		media string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT data, media_type FROM blobs WHERE id = ?`, fx.textBlobID).Scan(&data, &media); err != nil {
		t.Fatalf("read seeded blob: %v", err)
	}
	if string(data) != string(fx.textBytes) || media != fx.textMedia {
		t.Errorf("seeded blob changed: data=%q media=%q", data, media)
	}

	var (
		kind   string
		blobID sql.NullInt64
	)
	if err := db.QueryRowContext(ctx,
		`SELECT kind, blob_id FROM tiles WHERE id = ?`, fx.textTileID).Scan(&kind, &blobID); err != nil {
		t.Fatalf("read seeded text tile: %v", err)
	}
	if kind != "text" || !blobID.Valid || blobID.Int64 != fx.textBlobID {
		t.Errorf("seeded text tile changed: kind=%q blob_id=%v", kind, blobID)
	}

	var urlStr sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT kind, url_string FROM tiles WHERE id = ?`, fx.urlTileID).Scan(&kind, &urlStr); err != nil {
		t.Fatalf("read seeded url tile: %v", err)
	}
	if kind != "url" || urlStr.String != fx.urlString {
		t.Errorf("seeded url tile changed: kind=%q url=%q", kind, urlStr.String)
	}

	var child sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT kind, child_grid_id FROM tiles WHERE id = ?`, fx.wellTileID).Scan(&kind, &child); err != nil {
		t.Fatalf("read seeded well tile: %v", err)
	}
	if kind != "well" || !child.Valid || child.Int64 != fx.childGridID {
		t.Errorf("seeded well tile changed: kind=%q child=%v", kind, child)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM grids WHERE id = ?`, fx.childGridID).Scan(&n); err != nil {
		t.Fatalf("read seeded child grid: %v", err)
	}
	if n != 1 {
		t.Errorf("seeded child grid missing after migration")
	}
}

func mustID(t *testing.T, res sql.Result) int64 {
	t.Helper()
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// ── per-migration fixtures ────────────────────────────────────────────────────

// migrationFixture pins, for one migration, how to seed rows valid at the
// version BEFORE it and how to verify both the new schema and the survival of
// those rows after it. Exactly one fixture per migration (enforced by
// TestMigrationsWellFormed), so each new migration costs one entry — not a
// bespoke test. Empty while migrations is empty (v1).
type migrationFixture struct {
	version int
	seed    func(t *testing.T, db *sql.DB, rootID string)
	verify  func(t *testing.T, db *sql.DB)
}

var migrationFixtures []migrationFixture

// Fixture handle: rows are found again by alt_text, not by object_id.
// object_id was the natural stable handle until schema v10 retired it —
// and a REBUILD migration renumbers nothing but does drop columns, so the
// handle must be a column that survives every step of the chain.
func init() {
	// v2: alt_user (issue #61). Seed a v1 tile with a name; verify the column
	// arrives defaulted to 0 and the old row (and its name) survived.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 2,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v2', ` + rootID + `, 'shell', 0, 0, 1, 1, 'named-before-v2', 100, 100)`); err != nil {
				t.Fatalf("seed v1 tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var alt string
			var altUser int
			if err := db.QueryRow(`SELECT alt_text, alt_user FROM tiles WHERE alt_text = 'named-before-v2'`).Scan(&alt, &altUser); err != nil {
				t.Fatalf("read migrated tile: %v", err)
			}
			if alt != "named-before-v2" {
				t.Errorf("alt_text = %q, want the pre-migration value", alt)
			}
			if altUser != 0 {
				t.Errorf("alt_user = %d, want default 0", altUser)
			}
		},
	})
	// v3: content_zoom (issue #82). Seed a v2 tile; verify the column arrives
	// defaulted to 0 and the row survived.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 3,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v3', ` + rootID + `, 'shell', 2, 2, 1, 1, 'v2-row', 100, 100)`); err != nil {
				t.Fatalf("seed v2 tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var zoom float64
			if err := db.QueryRow(`SELECT content_zoom FROM tiles WHERE alt_text = 'v2-row'`).Scan(&zoom); err != nil {
				t.Fatalf("read migrated tile: %v", err)
			}
			if zoom != 0 {
				t.Errorf("content_zoom = %v, want default 0", zoom)
			}
		},
	})
	// v4: url_history (issue #113). Seed a v3 url tile; verify the column
	// arrives NULL and the row survived.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 4,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, url_string, alt_text, created_at, updated_at)
				VALUES ('fixt-v4', ` + rootID + `, 'url', 4, 4, 1, 1, 'https://example.com', 'v3-row', 100, 100)`); err != nil {
				t.Fatalf("seed v3 tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var hist sql.NullString
			if err := db.QueryRow(`SELECT url_history FROM tiles WHERE alt_text = 'v3-row'`).Scan(&hist); err != nil {
				t.Fatalf("read migrated tile: %v", err)
			}
			if hist.Valid {
				t.Errorf("url_history = %q, want NULL default", hist.String)
			}
		},
	})
	// v5: the 'pane' kind — the chain's first table-REBUILD migration. Seed
	// pins the id-reuse trap: DROP TABLE tiles deletes its sqlite_sequence
	// row, and the copy re-seeds at the max SURVIVING id, so without the
	// save/restore in rebuildTilesForPaneKind a tile deleted before the
	// migration would get its id REUSED after it (embeds/deep links/client
	// caches are keyed by id). Seed a survivor + a higher-id tile that is
	// then deleted; verify the survivor's row is byte-identical, a fresh
	// insert mints ABOVE the deleted id, and the new CHECK accepts 'pane'
	// while still rejecting a malformed well.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 5,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, url_string, alt_text, content_zoom, url_history, created_at, updated_at)
				VALUES ('fixt-v5-survivor', ` + rootID + `, 'url', 6, 6, 2, 1, 'https://survivor.example', 'v5-survivor', 1.5, '{"index":0,"entries":[]}', 100, 100)`); err != nil {
				t.Fatalf("seed survivor tile: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v5-deleted', ` + rootID + `, 'shell', 8, 8, 1, 1, 'v5-doomed', 100, 100)`); err != nil {
				t.Fatalf("seed doomed tile: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM tiles WHERE alt_text = 'v5-doomed'`); err != nil {
				t.Fatalf("delete doomed tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			// The survivor crossed the rebuild intact, id included.
			var kind, url, hist string
			var zoom float64
			var survivorID, deletedMax int64
			if err := db.QueryRow(`SELECT id, kind, url_string, content_zoom, url_history
				FROM tiles WHERE alt_text = 'v5-survivor'`).
				Scan(&survivorID, &kind, &url, &zoom, &hist); err != nil {
				t.Fatalf("read survivor: %v", err)
			}
			if kind != "url" || url != "https://survivor.example" || zoom != 1.5 || hist == "" {
				t.Errorf("survivor row damaged by rebuild: kind=%q url=%q zoom=%v hist=%q", kind, url, zoom, hist)
			}
			deletedMax = survivorID + 1 // the doomed tile was minted right after the survivor
			// The id-reuse trap: a fresh insert must mint ABOVE the deleted id.
			var gridID int64
			if err := db.QueryRow(`SELECT grid_id FROM tiles WHERE alt_text = 'v5-survivor'`).Scan(&gridID); err != nil {
				t.Fatalf("read grid id: %v", err)
			}
			res, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'pane', 10, 10, 2, 2, 'workspace', 100, 100)`, gridID)
			if err != nil {
				t.Fatalf("insert pane tile after rebuild (new CHECK should accept it): %v", err)
			}
			newID, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("new id: %v", err)
			}
			if newID <= deletedMax {
				t.Errorf("id REUSED after rebuild: new id %d <= deleted id %d (sqlite_sequence not restored)", newID, deletedMax)
			}
			// The rebuilt CHECK still rejects a malformed row (a well without
			// a child grid) — the constraint moved, it didn't loosen.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'well', 12, 12, 1, 1, '', 100, 100)`, gridID); err == nil {
				t.Errorf("rebuilt CHECK accepted a well without child_grid_id")
			}
			// The tiles indexes survived the rebuild.
			var nIdx int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND tbl_name = 'tiles' AND name LIKE 'idx_tiles_%'`).Scan(&nIdx); err != nil {
				t.Fatalf("count indexes: %v", err)
			}
			if nIdx != 2 {
				t.Errorf("tiles indexes after rebuild = %d, want 2", nIdx)
			}
		},
	})

	// v6 (link_target_id, the leaf-link variant — a rebuild because the CHECK
	// gains the link branch): a v5 url tile crosses intact with link_target_id
	// NULL (its old meaning: not a link); the new CHECK accepts a url LINK row
	// (url_string NULL, link_target_id set — the exact shape v5 forbade),
	// still rejects a bare url with neither url_string nor link, and rejects
	// a link row that smuggles content (the link branch requires every content
	// column NULL).
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 6,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, url_string, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'url', 20, 6, 2, 1, 'https://v5.example', 'v5 url', 100, 100)`); err != nil {
				t.Fatalf("seed v5 url tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var url string
			var linkTarget sql.NullString
			var gridID int64
			if err := db.QueryRow(`SELECT grid_id, url_string, link_target_id
				FROM tiles WHERE alt_text = 'v5 url'`).Scan(&gridID, &url, &linkTarget); err != nil {
				t.Fatalf("read v5 url tile: %v", err)
			}
			if url != "https://v5.example" || linkTarget.Valid {
				t.Errorf("v5 row damaged: url=%q link_target=%v (want url intact, link NULL)", url, linkTarget)
			}
			// The link branch: a url LINK row (no url_string) is now legal.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, link_target_id, alt_text, created_at, updated_at)
				VALUES (?, 'url', 22, 6, 1, 1, 'aabbccddeeff00112233445566778899/7', 'linked url', 100, 100)`, gridID); err != nil {
				t.Errorf("new CHECK rejected a url link row: %v", err)
			}
			// Still rejects a url row with neither url_string nor link.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'url', 24, 6, 1, 1, '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted a url row with no url_string and no link_target_id")
			}
			// Rejects a link row smuggling content (url_string on a link).
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, url_string, link_target_id, alt_text, created_at, updated_at)
				VALUES (?, 'url', 26, 6, 1, 1, 'https://smuggled.example', 'aabbccddeeff00112233445566778899/8', '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted a link row carrying url_string content")
			}
			// And a link row on the well kind (wells link via child_grid_id).
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, link_target_id, alt_text, created_at, updated_at)
				VALUES (?, 'well', 28, 6, 1, 1, 'aabbccddeeff00112233445566778899/9', '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted link_target_id on a well")
			}
		},
	})

	// v7: url_frozen — the user's standing freeze (issue #237).
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 7,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, url_string, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'url', 30, 6, 1, 1, 'https://v6.example', 'v6 url', 100, 100)`); err != nil {
				t.Fatalf("seed v6 url tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var frozen int
			var url string
			if err := db.QueryRow(`SELECT url_frozen, url_string
				FROM tiles WHERE alt_text = 'v6 url'`).Scan(&frozen, &url); err != nil {
				t.Fatalf("read v6 url tile: %v", err)
			}
			if frozen != 0 || url != "https://v6.example" {
				t.Errorf("v6 row: frozen=%d url=%q, want an unfrozen row with its url intact", frozen, url)
			}
		},
	})

	// v8 was the configure_plugin_id rebuild (the unconfigured plugin well,
	// issue #251). The column is gone again at v10, and a rebuild always
	// materializes the CURRENT tilesTableDDL — so what v8 still pins is the
	// REBUILD's copy list: the v5-era list would silently reset every
	// post-v5 column, so the seed plants a LINK row and a FROZEN url (the
	// two post-v5 facts) and the verify proves both survive.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 8,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, link_target_id, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'text', 40, 6, 1, 1, 'aplugin/77', 'v7 link', 100, 100)`); err != nil {
				t.Fatalf("seed v7 link tile: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, url_string, url_frozen, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'url', 42, 6, 1, 1, 'https://v7.example', 1, 'v7 frozen', 100, 100)`); err != nil {
				t.Fatalf("seed v7 frozen url tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var target string
			var gridID int64
			if err := db.QueryRow(`SELECT grid_id, link_target_id
				FROM tiles WHERE alt_text = 'v7 link'`).Scan(&gridID, &target); err != nil {
				t.Fatalf("read v7 link tile: %v", err)
			}
			if target != "aplugin/77" {
				t.Errorf("link_target_id = %q, want the v7 link to survive the rebuild", target)
			}
			var frozen int
			if err := db.QueryRow(`SELECT url_frozen
				FROM tiles WHERE alt_text = 'v7 frozen'`).Scan(&frozen); err != nil {
				t.Fatalf("read v7 frozen url tile: %v", err)
			}
			if frozen != 1 {
				t.Errorf("url_frozen = %d, want the v7 standing freeze to survive the rebuild", frozen)
			}
			// A childless well is a CHECK violation — it was for one
			// generation (v8) the unconfigured plugin well, and is again.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'well', 46, 6, 1, 1, '', 100, 100)`, gridID); err == nil {
				t.Error("a childless well must violate the CHECK")
			}
		},
	})
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 9,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'text', 50, 6, 1, 1, 'v8 text', 100, 100)`); err != nil {
				t.Fatalf("seed v8 text tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var ns, key string
			var tomb int
			if err := db.QueryRow(`SELECT ns, key, tombstoned FROM tiles WHERE alt_text = 'v8 text'`).Scan(&ns, &key, &tomb); err != nil {
				t.Fatalf("read v8 tile: %v", err)
			}
			if ns != "" || key != "" || tomb != 0 {
				t.Errorf("an old home row must carry the externals' defaults, got ns=%q key=%q tombstoned=%d", ns, key, tomb)
			}
			// An external's context grid and row are accepted; a live key
			// is unique per context; a retired key may be re-minted.
			if _, err := db.Exec(`INSERT INTO grids (object_id, created_at, ns, context_key) VALUES ('fixt-v9-ctx', 100, 'plug1', 'root')`); err != nil {
				t.Fatalf("insert context grid post-migration: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at, ns, key)
				SELECT id, 'text', 0, 0, 1, 1, 'a', 100, 100, 'plug1', 'a' FROM grids WHERE object_id = 'fixt-v9-ctx'`); err != nil {
				t.Fatalf("insert external row post-migration: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO listings (grid_id, entries) SELECT id, X'00' FROM grids WHERE object_id = 'fixt-v9-ctx'`); err != nil {
				t.Fatalf("listings table missing post-migration: %v", err)
			}
		},
	})

	// v10: DEAD STORAGE RETIRED (the owner decision in migrations.go). The
	// seed plants one of each thing that goes and one of each thing that
	// must NOT: a session row, an ordinary well and its child grid, a url
	// tile carrying framing facts, and a deleted high-id tile for the
	// rebuild's id-reuse trap. The verify proves the table and both
	// columns are gone, every surviving row is still there with its facts,
	// the ids of deleted tiles stay dead, and the tightened well CHECK
	// holds. (The stale-plugin-well arm has its own test — see the note in
	// the verify.)
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 10,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO session (id, data) VALUES (1, X'0102')`); err != nil {
				t.Fatalf("seed session row: %v", err)
			}
			res, err := db.Exec(`INSERT INTO grids (object_id, created_at, updated_at) VALUES ('fixt-v10-child', 100, 100)`)
			if err != nil {
				t.Fatalf("seed child grid: %v", err)
			}
			child := mustID(t, res)
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, child_grid_id, alt_text, created_at, updated_at)
				VALUES (`+rootID+`, 'well', 60, 6, 1, 1, ?, 'v9 well', 100, 100)`, child); err != nil {
				t.Fatalf("seed v9 well: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, url_string, url_frozen, content_zoom, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'url', 64, 6, 1, 1, 'https://v9.example', 1, 2.25, 'v9 url', 100, 100)`); err != nil {
				t.Fatalf("seed v9 url: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'shell', 66, 6, 1, 1, 'v9 doomed', 100, 100)`); err != nil {
				t.Fatalf("seed doomed tile: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM tiles WHERE alt_text = 'v9 doomed'`); err != nil {
				t.Fatalf("delete doomed tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			// The session table is gone.
			var nTab int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'session'`).Scan(&nTab); err != nil {
				t.Fatalf("look for session table: %v", err)
			}
			if nTab != 0 {
				t.Error("the session table survived v10")
			}
			// Both object_id columns and both their indexes are gone.
			for _, tbl := range []string{"grids", "tiles"} {
				if _, ok := tableColumnsFP(t, db, tbl)[objectIDColumn]; ok {
					t.Errorf("%s.object_id survived v10", tbl)
				}
			}
			if _, ok := tableColumnsFP(t, db, "tiles")["configure_plugin_id"]; ok {
				t.Error("tiles.configure_plugin_id survived v10")
			}
			var nIdx int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name LIKE '%object_id%'`).Scan(&nIdx); err != nil {
				t.Fatalf("count object_id indexes: %v", err)
			}
			if nIdx != 0 {
				t.Errorf("%d object_id index(es) survived v10", nIdx)
			}
			// The v9 url row kept every fact the user can see.
			var url string
			var frozen int
			var zoom float64
			if err := db.QueryRow(`SELECT url_string, url_frozen, content_zoom FROM tiles WHERE alt_text = 'v9 url'`).
				Scan(&url, &frozen, &zoom); err != nil {
				t.Fatalf("read v9 url: %v", err)
			}
			if url != "https://v9.example" || frozen != 1 || zoom != 2.25 {
				t.Errorf("v9 url damaged: url=%q frozen=%d zoom=%v", url, frozen, zoom)
			}
			// The ordinary well still points at its child grid, and the
			// child grid row survived the grids DROP COLUMN.
			var wellChild int64
			if err := db.QueryRow(`SELECT child_grid_id FROM tiles WHERE alt_text = 'v9 well'`).Scan(&wellChild); err != nil {
				t.Fatalf("read v9 well: %v", err)
			}
			var nGrid int
			if err := db.QueryRow(`SELECT COUNT(*) FROM grids WHERE id = ?`, wellChild).Scan(&nGrid); err != nil {
				t.Fatalf("read child grid: %v", err)
			}
			if nGrid != 1 {
				t.Error("the well's child grid did not survive the grids column drop")
			}
			// (The stale unconfigured plugin well cannot be seeded through
			// the chain — the v5 rebuild already materialized the v10 tiles
			// shape, so a chain-built v9 file has no configure_plugin_id
			// column. TestMigrateV10OverAGenuineV9File builds the
			// genuine v9 shape and covers that arm.)
			// The id-reuse trap across the v10 rebuild.
			res, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'shell', 68, 6, 1, 1, 'v10 post', 100, 100)`, wellChild)
			if err != nil {
				t.Fatalf("insert after rebuild: %v", err)
			}
			newID := mustID(t, res)
			var maxOld int64
			if err := db.QueryRow(`SELECT MAX(id) FROM tiles WHERE alt_text != 'v10 post'`).Scan(&maxOld); err != nil {
				t.Fatalf("read max surviving id: %v", err)
			}
			if newID <= maxOld+1 {
				t.Errorf("id REUSED after the v10 rebuild: new id %d, deleted id was %d", newID, maxOld+1)
			}
			// The tightened CHECK: no childless well of any kind.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (?, 'well', 70, 6, 1, 1, '', 100, 100)`, wellChild); err == nil {
				t.Error("the v10 CHECK accepted a childless well")
			}
			// v9's live-key index came back after DROP TABLE tiles.
			var nLive int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'idx_tiles_live_key'`).Scan(&nLive); err != nil {
				t.Fatalf("look for idx_tiles_live_key: %v", err)
			}
			if nLive != 1 {
				t.Error("idx_tiles_live_key did not survive the v10 rebuild")
			}
		},
	})
}

// v11: ONE framing shape. A chain-built v10 file already carries the
// v11 tiles columns (the v5 rebuild materializes the CURRENT template),
// so what this fixture can reach is the other three halves of the step:
// a doorway's framing survives the rebuild untouched, a plugin context's
// root converts from origin to center, and home's root moves out of the
// system KV table onto its root GRID row. The view_x → view_cx
// conversion itself needs a GENUINE v10 file —
// TestMigrateV11OverAGenuineV10File.
func init() {
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 11,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			res, err := db.Exec(`INSERT INTO grids (created_at, updated_at) VALUES (100, 100)`)
			if err != nil {
				t.Fatalf("seed child grid: %v", err)
			}
			child := mustID(t, res)
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, view_cx, view_cy, view_zoom,
				child_grid_id, alt_text, created_at, updated_at)
				VALUES (`+rootID+`, 'well', 80, 8, 2, 2, 4.25, -6.5, 0.375, ?, 'v10 framed well', 100, 100)`, child); err != nil {
				t.Fatalf("seed framed well: %v", err)
			}
			// A plugin context's root, stored the old way: the ORIGIN of
			// the 1×1 synthetic doorway the client framed it through.
			if _, err := db.Exec(`INSERT INTO grids (created_at, updated_at, ns, context_key, root_cx, root_cy, root_zoom)
				VALUES (100, 100, 'p1', 'root', 3, -2, 0.5)`); err != nil {
				t.Fatalf("seed plugin context root: %v", err)
			}
			// Home's root framing, in the KV table v11 retires.
			for _, kv := range [][2]string{{"root_view_cx", "10"}, {"root_view_cy", "-4"}, {"root_zoom", "0.25"}} {
				if _, err := db.Exec(`INSERT INTO system (key, value) VALUES (?, ?)
					ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
					t.Fatalf("seed home root framing key %s: %v", kv[0], err)
				}
			}
			// A tile deleted before the rebuild: its id must stay dead.
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES (` + rootID + `, 'shell', 82, 8, 1, 1, 'v10 doomed', 100, 100)`); err != nil {
				t.Fatalf("seed doomed tile: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM tiles WHERE alt_text = 'v10 doomed'`); err != nil {
				t.Fatalf("delete doomed tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			// The retired pair is gone; the surviving doorway kept its
			// framing to the bit.
			for _, col := range []string{"view_x", "view_y"} {
				if _, ok := tableColumnsFP(t, db, "tiles")[col]; ok {
					t.Errorf("tiles.%s survived v11", col)
				}
			}
			var cx, cy, zoom float64
			if err := db.QueryRow(`SELECT view_cx, view_cy, view_zoom FROM tiles WHERE alt_text = 'v10 framed well'`).
				Scan(&cx, &cy, &zoom); err != nil {
				t.Fatalf("read the framed well: %v", err)
			}
			if cx != 4.25 || cy != -6.5 || zoom != 0.375 {
				t.Errorf("doorway framing changed across the rebuild: (%v, %v, %v)", cx, cy, zoom)
			}
			// The plugin context's root converted origin → center.
			if err := db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE ns = 'p1' AND context_key = 'root'`).
				Scan(&cx, &cy, &zoom); err != nil {
				t.Fatalf("read the plugin context root: %v", err)
			}
			if cx != 3.5 || cy != -1.5 || zoom != 0.5 {
				t.Errorf("plugin root framing = (%v, %v, %v), want the center (3.5, -1.5, 0.5)", cx, cy, zoom)
			}
			// Home's root moved onto its grid row, converted the same way,
			// and the KV rows are gone.
			var rootID int64
			if err := db.QueryRow(`SELECT value FROM system WHERE key = 'root_grid_id'`).Scan(&rootID); err != nil {
				t.Fatalf("read root grid id: %v", err)
			}
			if err := db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ? AND ns = ''`, rootID).
				Scan(&cx, &cy, &zoom); err != nil {
				t.Fatalf("read home root framing: %v", err)
			}
			if cx != 10.5 || cy != -3.5 || zoom != 0.25 {
				t.Errorf("home root framing = (%v, %v, %v), want the center (10.5, -3.5, 0.25)", cx, cy, zoom)
			}
			var nKeys int
			if err := db.QueryRow(`SELECT COUNT(*) FROM system
				WHERE key IN ('root_view_cx', 'root_view_cy', 'root_zoom')`).Scan(&nKeys); err != nil {
				t.Fatalf("count retired keys: %v", err)
			}
			if nKeys != 0 {
				t.Errorf("%d retired root-framing key(s) survived v11", nKeys)
			}
			// The id-reuse trap across the v11 rebuild.
			res, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				SELECT id, 'shell', 84, 8, 1, 1, 'v11 post', 100, 100 FROM grids WHERE ns = '' ORDER BY id LIMIT 1`)
			if err != nil {
				t.Fatalf("insert after rebuild: %v", err)
			}
			newID := mustID(t, res)
			var maxOld int64
			if err := db.QueryRow(`SELECT MAX(id) FROM tiles WHERE alt_text != 'v11 post'`).Scan(&maxOld); err != nil {
				t.Fatalf("read max surviving id: %v", err)
			}
			if newID <= maxOld+1 {
				t.Errorf("id REUSED after the v11 rebuild: new id %d, deleted id was %d", newID, maxOld+1)
			}
			// v9's live-key index came back after DROP TABLE tiles.
			var nLive int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'idx_tiles_live_key'`).Scan(&nLive); err != nil {
				t.Fatalf("look for idx_tiles_live_key: %v", err)
			}
			if nLive != 1 {
				t.Error("idx_tiles_live_key did not survive the v11 rebuild")
			}
		},
	})

	// v12: the `listings` table retires (the owner decision in
	// migrations.go). It stood alone — no CHECK, no AUTOINCREMENT seed,
	// nothing referencing it — so the step is a plain DROP and the two
	// record tables must come through completely untouched. The seed
	// plants a listing row on a real plugin context plus the durable row
	// beside it; the verify proves the table is gone AND that the row the
	// user can actually see (its id, its placement, its label, its
	// tombstone) survived, because THAT is the memory a dark source now
	// reads from.
	//
	// This fixture IS the genuine-old-file test the drop rule asks for
	// (internal/local/store/CLAUDE.md). The usual problem — a chain-built
	// file at N-1 already has the current shape, so it cannot hold what N
	// drops — does not apply to a table created by a MIGRATION LITERAL:
	// v9 spells `listings` itself, so a chain-built v11 file really has
	// it, and the seed below really plants the retired shape.
	migrationFixtures = append(migrationFixtures, migrationFixture{
		version: 12,
		seed: func(t *testing.T, db *sql.DB, rootID string) {
			t.Helper()
			res, err := db.Exec(`INSERT INTO grids (created_at, updated_at, ns, context_key) VALUES (100, 100, 'p9', 'root')`)
			if err != nil {
				t.Fatalf("seed plugin context: %v", err)
			}
			ctxGrid := mustID(t, res)
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at, ns, key)
				VALUES (?, 'text', 12, 34, 2, 3, 'v11 remembered', 100, 100, 'p9', 'notes.md')`, ctxGrid); err != nil {
				t.Fatalf("seed external row: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, w, h, alt_text, created_at, updated_at, ns, key, tombstoned)
				VALUES (?, 'text', 13, 34, 1, 1, 'v11 gone', 100, 100, 'p9', 'gone.md', 1)`, ctxGrid); err != nil {
				t.Fatalf("seed retired row: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO listings (grid_id, entries, authoritative) VALUES (?, X'0a04', 1)`, ctxGrid); err != nil {
				t.Fatalf("seed listing blob: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'listings'`).Scan(&n); err != nil {
				t.Fatalf("look for the listings table: %v", err)
			}
			if n != 0 {
				t.Error("the listings table survived v12")
			}
			// The node's own memory of what it minted is untouched: the
			// dark-source answer comes from here now.
			var x, y, w, h, tomb int64
			var label string
			if err := db.QueryRow(`SELECT x, y, w, h, alt_text, tombstoned FROM tiles WHERE ns = 'p9' AND key = 'notes.md'`).
				Scan(&x, &y, &w, &h, &label, &tomb); err != nil {
				t.Fatalf("read the external row: %v", err)
			}
			if x != 12 || y != 34 || w != 2 || h != 3 || label != "v11 remembered" || tomb != 0 {
				t.Errorf("the external row changed across v12: %d,%d %dx%d %q tomb=%d", x, y, w, h, label, tomb)
			}
			if err := db.QueryRow(`SELECT tombstoned FROM tiles WHERE ns = 'p9' AND key = 'gone.md'`).Scan(&tomb); err != nil {
				t.Fatalf("read the retired row: %v", err)
			}
			if tomb != 1 {
				t.Error("a retired key came back live across v12")
			}
		},
	})
}

// objectIDColumn names the retired provenance column. Spelled once so the
// v10 fixture's "it is gone" assertions can't drift from each other.
const objectIDColumn = "object_id"
