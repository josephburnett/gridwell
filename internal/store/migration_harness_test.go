package store

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"strings"
	"testing"
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
	s := &Store{db: db, subs: map[*subscriber]struct{}{}}
	seedDeterministic(s)
	return s
}

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

	for _, ddl := range []string{pragmas, systemDDL, tablesV1, sessionDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply v1 ddl: %v", err)
		}
	}
	s := storeOver(db)
	if err := s.bootstrapRoot(ctx); err != nil {
		t.Fatalf("bootstrap v1 root: %v", err)
	}
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
			if err := db.QueryRow(`SELECT alt_text, alt_user FROM tiles WHERE object_id = 'fixt-v2'`).Scan(&alt, &altUser); err != nil {
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
			if err := db.QueryRow(`SELECT content_zoom FROM tiles WHERE object_id = 'fixt-v3'`).Scan(&zoom); err != nil {
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
				VALUES ('fixt-v4', ` + rootID + `, 'url', 4, 4, 1, 1, 'https://example.com', '', 100, 100)`); err != nil {
				t.Fatalf("seed v3 tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var hist sql.NullString
			if err := db.QueryRow(`SELECT url_history FROM tiles WHERE object_id = 'fixt-v4'`).Scan(&hist); err != nil {
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
				VALUES ('fixt-v5-survivor', ` + rootID + `, 'url', 6, 6, 2, 1, 'https://survivor.example', 'named', 1.5, '{"index":0,"entries":[]}', 100, 100)`); err != nil {
				t.Fatalf("seed survivor tile: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v5-deleted', ` + rootID + `, 'shell', 8, 8, 1, 1, '', 100, 100)`); err != nil {
				t.Fatalf("seed doomed tile: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM tiles WHERE object_id = 'fixt-v5-deleted'`); err != nil {
				t.Fatalf("delete doomed tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			// The survivor crossed the rebuild intact, id included.
			var kind, url, alt, hist string
			var zoom float64
			var survivorID, deletedMax int64
			if err := db.QueryRow(`SELECT id, kind, url_string, alt_text, content_zoom, url_history
				FROM tiles WHERE object_id = 'fixt-v5-survivor'`).
				Scan(&survivorID, &kind, &url, &alt, &zoom, &hist); err != nil {
				t.Fatalf("read survivor: %v", err)
			}
			if kind != "url" || url != "https://survivor.example" || alt != "named" || zoom != 1.5 || hist == "" {
				t.Errorf("survivor row damaged by rebuild: kind=%q url=%q alt=%q zoom=%v hist=%q", kind, url, alt, zoom, hist)
			}
			deletedMax = survivorID + 1 // the doomed tile was minted right after the survivor
			// The id-reuse trap: a fresh insert must mint ABOVE the deleted id.
			var gridID int64
			if err := db.QueryRow(`SELECT grid_id FROM tiles WHERE object_id = 'fixt-v5-survivor'`).Scan(&gridID); err != nil {
				t.Fatalf("read grid id: %v", err)
			}
			res, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v5-post', ?, 'pane', 10, 10, 2, 2, 'workspace', 100, 100)`, gridID)
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
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v5-bad', ?, 'well', 12, 12, 1, 1, '', 100, 100)`, gridID); err == nil {
				t.Errorf("rebuilt CHECK accepted a well without child_grid_id")
			}
			// The three tiles indexes survived the rebuild.
			var nIdx int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND tbl_name = 'tiles' AND name LIKE 'idx_tiles_%'`).Scan(&nIdx); err != nil {
				t.Fatalf("count indexes: %v", err)
			}
			if nIdx != 3 {
				t.Errorf("tiles indexes after rebuild = %d, want 3", nIdx)
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
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, url_string, alt_text, created_at, updated_at)
				VALUES ('fixt-v6-url', ` + rootID + `, 'url', 20, 6, 2, 1, 'https://v5.example', 'v5 url', 100, 100)`); err != nil {
				t.Fatalf("seed v5 url tile: %v", err)
			}
		},
		verify: func(t *testing.T, db *sql.DB) {
			t.Helper()
			var url string
			var linkTarget sql.NullString
			var gridID int64
			if err := db.QueryRow(`SELECT grid_id, url_string, link_target_id
				FROM tiles WHERE object_id = 'fixt-v6-url'`).Scan(&gridID, &url, &linkTarget); err != nil {
				t.Fatalf("read v5 url tile: %v", err)
			}
			if url != "https://v5.example" || linkTarget.Valid {
				t.Errorf("v5 row damaged: url=%q link_target=%v (want url intact, link NULL)", url, linkTarget)
			}
			// The link branch: a url LINK row (no url_string) is now legal.
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, link_target_id, alt_text, created_at, updated_at)
				VALUES ('fixt-v6-link', ?, 'url', 22, 6, 1, 1, 'aabbccddeeff00112233445566778899/7', 'linked url', 100, 100)`, gridID); err != nil {
				t.Errorf("new CHECK rejected a url link row: %v", err)
			}
			// Still rejects a url row with neither url_string nor link.
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, alt_text, created_at, updated_at)
				VALUES ('fixt-v6-bad1', ?, 'url', 24, 6, 1, 1, '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted a url row with no url_string and no link_target_id")
			}
			// Rejects a link row smuggling content (url_string on a link).
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, url_string, link_target_id, alt_text, created_at, updated_at)
				VALUES ('fixt-v6-bad2', ?, 'url', 26, 6, 1, 1, 'https://smuggled.example', 'aabbccddeeff00112233445566778899/8', '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted a link row carrying url_string content")
			}
			// And a link row on the well kind (wells link via child_grid_id).
			if _, err := db.Exec(`INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h, link_target_id, alt_text, created_at, updated_at)
				VALUES ('fixt-v6-bad3', ?, 'well', 28, 6, 1, 1, 'aabbccddeeff00112233445566778899/9', '', 100, 100)`, gridID); err == nil {
				t.Errorf("CHECK accepted link_target_id on a well")
			}
		},
	})
}
