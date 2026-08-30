package store

// This file is the canonical SQLite schema for Gridwell. Single-tenant:
// there is no users/groups/sessions table. The `system` KV table holds
// singleton state (root grid id and root viewport framing); the schema
// version lives in the SQLite header (PRAGMA user_version), not here.
//
// The local store holds only Gridwell-owned grids. Host-backed content
// (filesystem directories, the process table) lives in the fs/proc plugins,
// each with its own database; a well that points at one is an ordinary `well`
// row whose child_grid_id is a qualified "<plugin-uuid>/<grid-id>" reference.
//
// There are five tile kinds:
//   - well  (interior): points at a child grid. Blue when the child is in this
//     same store; red ("exit well") when child_grid_id names another plugin.
//   - text  (interior): markdown blob (green).
//   - url   (interior): http(s) URL + frozen JPEG preview (purple).
//   - shell (exit):     interactive bash session inside a gridwell-private tmux
//     session. Live mode attaches the tmux client; freeze captures a JPEG
//     preview and detaches. The bash + scrollback live in the tmux server and
//     persist across ascents (and gridwell restarts); they are gone only when
//     the tile is deleted or the host reboots.
//   - pane  (interior): a durable workspace — blob_id holds the serialized
//     split-pane layout (client/pane LayoutV1). Added at schema v5 via the
//     first CHECK-rebuild migration.
//
// Well rows carry one framing (view_cx, view_cy, view_zoom) — a float
// center in the child grid's coordinates plus a pane-size-independent
// zoom — that is at once the preview frame, the descent target, and the
// ascent return.
// Text rows carry a doc-space window (text_x, text_y, text_w, text_h)
// plus a rendered/text mode plus blob_id (the markdown source). URL rows
// carry a URL string and preview_blob_id (the last-frozen JPEG captured
// at close, hash-deduped through the blobs table just like text content).

// pragmas are connection-level settings applied once at Open, before any
// schema or attach. synchronous is connection-scoped (not stored in the file)
// and defaults to FULL regardless of journal mode, so it must be pinned on
// every Open. NORMAL is the SQLite-recommended level under WAL: durable
// against application and OS crashes, and a power loss can lose at most the
// last not-yet-checkpointed transaction — never corrupt the file — in exchange
// for far fewer fsyncs on this write-heavy store.
const pragmas = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;
`

// systemDDL is the main-only singleton KV table.
const systemDDL = `
CREATE TABLE IF NOT EXISTS system (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- Keys: root_grid_id. (root_view_cx/root_view_cy/root_zoom retired at
-- schema v11: home's root framing lives on its root GRID row, ns = '',
-- in the same three columns every other root uses.)
`

// (The `session` table is gone — 2026-08-29, schema v10. It held one
// Chromium session per DB, moved over the wire by GetSession/PutSession;
// both RPCs died 2026-07-26 when the session became host-local, and
// nothing read or wrote the table afterwards. The v10 migration drops it
// from old files; the frozen v1 text lives on in the migration test
// harness, which still builds genuine old files that have it.)

// tablesDDL returns the grids/tiles/blobs DDL for the main database. This is
// the canonical, always-current schema a fresh Open materializes directly.
// The grids and tiles halves are RENDERED from the column descriptor
// (columns.go) — that table, not this function, is where a column is
// described. Every column added there must be matched by an additive
// migration (see migrations.go); TestSchemaEquivalence proves the two agree.
// See internal/local/store/CLAUDE.md for the schema-evolution contract.
//
// The tiles table is built by tilesTableDDL so its one DDL source is shared
// with the CHECK-rebuild migration path (migrations.go): a rebuild creates
// tiles_new from the same text a fresh Open uses, so the two cannot drift.
func tablesDDL() string {
	return gridsTableDDL() + blobsTemplate + tilesTableDDL("tiles") + tilesIndexDDL
}

// gridsTableDDL renders the grids table from the column descriptor
// (columns.go), which is also what the SELECT list and the scan read.
func gridsTableDDL() string { return createTable("grids", gridsColumns, "") }

const blobsTemplate = `
CREATE TABLE IF NOT EXISTS blobs (
    -- AUTOINCREMENT: blob ids feed the client's (tile id, blob id) preview
    -- cache key, so a recycled blob id could serve stale image bytes.
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL UNIQUE,
    data       BLOB NOT NULL,
    refcount   INTEGER NOT NULL DEFAULT 0,
    -- Self-describing media: an IANA type ('text/markdown', 'image/jpeg') so a
    -- blob is interpretable on its own, independent of the column that points
    -- at it. Read back through GetBlob and returned over the wire by
    -- GetTileContent, never hard-coded at the read site. Blobs are immutable
    -- (content-addressed): size is recomputable from data and first-seen time
    -- carries no meaning for dedup, so neither is stored.
    media_type TEXT NOT NULL DEFAULT ''
);
`

// tilesTableDDL returns the CREATE TABLE for the current tiles shape with the
// table name parameterized: "tiles" for a fresh Open, "tiles_new" for the
// CHECK-rebuild migration (the only migration shape that can change the kind
// CHECK — see internal/local/store/CLAUDE.md). The columns come from the
// descriptor in columns.go, which is also what the SELECT list, the scan, the
// clone INSERT and every rebuild copy list read: one description of a column,
// five readers.
func tilesTableDDL(name string) string {
	return createTable(name, tilesColumns, tilesCheck)
}

// tilesCheck is the tiles table's kind CHECK: which columns each kind may
// and may not hold. It is a per-KIND rule over columns, not a column list,
// so it stays literal text — the only part of the table a rebuild migration
// can change (see internal/local/store/CLAUDE.md).
const tilesCheck = `    CHECK (
       (link_target_id IS NULL AND (
          -- well: an interior/exit well always has a child grid. (v8's
          -- childless variant — the unconfigured plugin well — went with
          -- configure_plugin_id at v10.)
          (kind = 'well'  AND child_grid_id IS NOT NULL AND blob_id IS NULL AND url_string IS NULL AND preview_blob_id IS NULL AND text_mode IS NULL)
       OR (kind = 'text'  AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL)
       OR (kind = 'url'   AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL)
       OR (kind = 'shell' AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL)
       -- pane: a durable workspace. blob_id (nullable) holds the serialized
       -- layout (client/pane LayoutV1, application/vnd.gridwell.pane-layout+json);
       -- NULL means never arranged (descent installs the default single pane).
       OR (kind = 'pane'  AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL AND text_mode IS NULL)
       ))
    -- link variant: a leaf tile whose content lives in another plugin's tile
    -- (link_target_id). No content columns — bytes/preview/url are read
    -- through the target. text framing columns (text_x/y/w/h) and view_*
    -- stay usable: framing is per-link local, like an exit well's view.
    OR (link_target_id IS NOT NULL AND kind IN ('text','url','shell','pane')
        AND child_grid_id IS NULL AND blob_id IS NULL AND url_string IS NULL
        AND preview_blob_id IS NULL AND text_mode IS NULL)
    )
`

// tilesIndexDDL recreates the tiles indexes; shared by the fresh path
// (tablesDDL) and the rebuild migration for the same no-drift reason as
// tilesTableDDL.
const tilesIndexDDL = `
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`

// externalsIndexDDL is the v9 pair of partial unique indexes over the
// externals' columns. They name columns the v9 migration ADDS, so they
// cannot ride tablesDDL (Open applies that before migrating an old file);
// Open creates them after the chain, fresh and migrated files alike.
const externalsIndexDDL = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_grids_context ON grids(ns, context_key) WHERE ns != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tiles_live_key ON tiles(ns, grid_id, key) WHERE ns != '' AND tombstoned = 0;
`

// tablesV1 is the FROZEN v1 grids/tiles/blobs schema. It is an immutable,
// byte-for-byte copy of what the live tables were at the moment the localdb
// format was frozen (schemaVersion 1). NEVER edit it: tests build genuine
// "old files" from this text and migrate them forward, so editing it would
// rewrite history and hide migration bugs. New columns/tables go into
// the column descriptor (columns.go) plus a migration — never here.
//
// TestSchemaEquivalence asserts (tablesV1 + all migrations) produces a schema
// identical to a fresh tablesDDL(); that is the proof that a brand-new DB
// and an upgraded old DB converge on the same shape.
const tablesV1 = `
CREATE TABLE IF NOT EXISTS grids (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id   TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);

CREATE TABLE IF NOT EXISTS blobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL UNIQUE,
    data       BLOB NOT NULL,
    refcount   INTEGER NOT NULL DEFAULT 0,
    media_type TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS tiles (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','shell')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    child_grid_id INTEGER,
    text_x        INTEGER NOT NULL DEFAULT 0,
    text_y        INTEGER NOT NULL DEFAULT 0,
    text_w        INTEGER NOT NULL DEFAULT 0,
    text_h        INTEGER NOT NULL DEFAULT 0,
    text_mode     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    url_string       TEXT,
    preview_blob_id  INTEGER REFERENCES blobs(id),
    alt_text      TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (kind = 'well'  AND child_grid_id IS NOT NULL AND blob_id IS NULL AND url_string IS NULL    AND preview_blob_id IS NULL AND text_mode IS NULL)
    OR (kind = 'text'  AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL)
    OR (kind = 'url'   AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL)
    OR (kind = 'shell' AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`
