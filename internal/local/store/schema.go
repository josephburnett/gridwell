package store

// The canonical SQLite schema. The `system` KV table holds singleton state;
// the schema version lives in the SQLite header, not here. A plugin's memory
// lives in the same tables under its own namespace, and a well pointing at one
// is an ordinary `well` row whose child_grid_id is a qualified
// "<plugin-uuid>/<grid-id>" reference.
//
// The five tile kinds:
//   - well: points at a child grid, in this namespace or, as an exit well,
//     another.
//   - text: a markdown blob.
//   - url: an http(s) URL plus a frozen JPEG preview.
//   - shell: an interactive shell in a gridwell-private tmux session. The
//     shell and its scrollback live in the tmux server and survive ascents
//     and restarts; they are gone only when the tile is deleted or the host
//     reboots.
//   - pane: blob_id holds the api/panelayout split-pane layout.
//
// A well row's one framing (view_cx, view_cy, view_zoom) is at once the
// preview frame, the descent target and the ascent return. A text row carries
// a doc-space window plus a rendered/text mode plus blob_id. A url row carries
// the address and preview_blob_id, hash-deduped like text content.

// pragmas are connection-level settings applied once at Open, before any
// schema or attach. synchronous is connection-scoped and defaults to FULL
// whatever the journal mode, so it must be pinned on every Open. NORMAL under
// WAL is durable against application and OS crashes; a power loss can lose the
// last not-yet-checkpointed transaction, never corrupt the file.
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
-- Keys: root_grid_id, plugin_uuid, scratch_grid_id, trash_grid_id. Home's
-- root framing is not here: it lives on its root grid row, ns = '', in the
-- same three columns every other root uses.
`

// tablesDDL is the always-current schema a fresh Open materializes. The grids,
// tiles and connections halves render from the column descriptor in
// columns.go, and every column added there must be matched by a migration;
// TestSchemaEquivalence proves the two agree. The contract is CLAUDE.md in
// this directory.
func tablesDDL() string {
	return gridsTableDDL() + blobsTemplate + tilesTableDDL("tiles") + tilesIndexDDL +
		connectionsTableDDL()
}

// gridsTableDDL renders the grids table from the column descriptor in
// columns.go, which is also what the SELECT list and the scan read.
func gridsTableDDL() string { return createTable("grids", gridsColumns, "") }

// connectionsTableDDL renders the connections table from the column descriptor
// in columns.go. The store owns this shape; internal/connection owns the
// queries and holds no DDL. One text, shared with the v13 migration.
func connectionsTableDDL() string { return createTable("connections", connectionsColumns, "") }

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
    -- ReadContent, never hard-coded at the read site. Blobs are immutable
    -- (content-addressed): size is recomputable from data and first-seen time
    -- carries no meaning for dedup, so neither is stored.
    media_type TEXT NOT NULL DEFAULT ''
);
`

// tilesTableDDL returns the CREATE TABLE for the current tiles shape with the
// name parameterized: "tiles" for a fresh Open, "tiles_new" for the rebuild
// migration, so the two cannot drift. Columns come from columns.go.
func tilesTableDDL(name string) string {
	return createTable(name, tilesColumns, tilesCheck)
}

// tilesCheck is which columns each kind may and may not hold. It is a per-kind
// rule, not a column list, so it stays literal text, and it is the only part
// of the table a rebuild migration can change.
const tilesCheck = `    CHECK (
       (link_target_id IS NULL AND (
          -- well: an interior or exit well always has a child grid.
          (kind = 'well'  AND child_grid_id IS NOT NULL AND blob_id IS NULL AND url_string IS NULL AND preview_blob_id IS NULL AND text_mode IS NULL)
       OR (kind = 'text'  AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL)
       OR (kind = 'url'   AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL)
       OR (kind = 'shell' AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL)
       -- pane: blob_id, nullable, holds the serialized layout in the
       -- api/panelayout format (application/vnd.gridwell.pane-layout+json).
       -- NULL means never arranged, and descent installs the default pane.
       OR (kind = 'pane'  AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL AND text_mode IS NULL)
       ))
    -- link variant: a leaf tile whose content lives in another plugin's
    -- tile, named by link_target_id. No content columns: bytes, preview,
    -- and url are read through the target. The text framing columns and
    -- view_* stay usable, because framing is per-link local, like an exit
    -- well's view.
    OR (link_target_id IS NOT NULL AND kind IN ('text','url','shell','pane')
        AND child_grid_id IS NULL AND blob_id IS NULL AND url_string IS NULL
        AND preview_blob_id IS NULL AND text_mode IS NULL)
    )
`

// tilesIndexDDL is shared by the fresh path and the rebuild migration, for the
// same no-drift reason as tilesTableDDL.
const tilesIndexDDL = `
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`

// externalsIndexDDL is the v9 pair of partial unique indexes over the
// plugin-memory columns. They name columns v9 adds, so they cannot ride
// tablesDDL, which Open applies before migrating an old file; Open creates
// them after the chain instead.
const externalsIndexDDL = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_grids_context ON grids(ns, context_key) WHERE ns != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tiles_live_key ON tiles(ns, grid_id, key) WHERE ns != '' AND tombstoned = 0;
`

// tablesV1 is the frozen schema at schemaVersion 1. Never edit it: tests build
// genuine old files from this text and migrate them forward, so an edit would
// hide migration bugs. New columns go into columns.go plus a migration.
// TestSchemaEquivalence asserts tablesV1 plus every migration equals a fresh
// tablesDDL(), the proof that a new and an upgraded DB converge.
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
