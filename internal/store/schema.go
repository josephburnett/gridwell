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
// There are four tile kinds:
//   - well  (interior): points at a child grid. Blue when the child is in this
//     same store; red ("exit well") when child_grid_id names another plugin.
//   - text  (interior): markdown blob (green).
//   - url   (interior): http(s) URL + frozen JPEG preview (purple).
//   - shell (exit):     interactive bash session inside a gridwell-private tmux
//     session. Live mode attaches the tmux client; freeze captures a JPEG
//     preview and detaches. The bash + scrollback live in the tmux server and
//     persist across ascents (and gridwell restarts); they are gone only when
//     the tile is deleted or the host reboots.
//
// Well rows carry one view rectangle (view_x, view_y, view_zoom) that is
// at once the preview frame, the descent target, and the ascent return.
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
-- Keys: root_grid_id, root_view_cx, root_view_cy, root_zoom.
`

// sessionDDL is the singleton Chromium-session blob for this DB — the plugin is
// the session boundary, so a DB carries exactly one session (cookies + web
// storage), moved over the wire by GetSession/PutSession. Storage-only (not a
// wire record), so it is invisible to the proto/DDL drift lint.
const sessionDDL = `
CREATE TABLE IF NOT EXISTS session (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    data BLOB NOT NULL
);
`

// tablesDDL returns the grids/tiles/blobs DDL for the main database. This is
// the canonical, always-current schema a fresh Open materializes directly, and
// the single readable description of the present shape — read it here, not by
// replaying migrations. Every column added here after v1 must be matched by an
// additive migration (see migrations.go); TestSchemaEquivalence proves the two
// agree. See internal/store/CLAUDE.md for the schema-evolution contract.
func tablesDDL() string { return tablesTemplate }

const tablesTemplate = `
CREATE TABLE IF NOT EXISTS grids (
    -- AUTOINCREMENT so a deleted grid's id is never reused. Without it,
    -- SQLite recycles the rowid of a deleted grid (e.g. an interior well that
    -- was deleted, taking its owned child grid with it), and a new grid taking
    -- that id would collide with the client's still-cached copy of the old
    -- grid — making a fresh well render the deleted well's tiles. Fresh ids
    -- keep the client cache (keyed by id) honest.
    --
    -- No refcount: grids are owned 1:1 by their parent well (copy-on-clone
    -- never shares a grid), so only blobs are reference-counted.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id   TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);

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

CREATE TABLE IF NOT EXISTS tiles (
    -- AUTOINCREMENT for the same reason as grids: a reused tile id would
    -- collide with the client's per-tile caches (e.g. the URL preview cache
    -- keyed by tile id), showing a deleted tile's frozen frame on a new one.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','shell')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    -- well: the rectangle in child-grid coordinates that the preview shows
    -- and descent restores.
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    -- No FK on child_grid_id: an exit well's child grid lives in another
    -- plugin (a qualified "<uuid>/<id>" reference), so the link is a soft
    -- pointer. Interior well integrity rests on the refcount machinery +
    -- property test.
    child_grid_id INTEGER,
    -- text-only: the framed window in doc-space px (scroll offset + size)
    -- plus rendered/text mode.
    text_x        INTEGER NOT NULL DEFAULT 0,
    text_y        INTEGER NOT NULL DEFAULT 0,
    text_w        INTEGER NOT NULL DEFAULT 0,
    text_h        INTEGER NOT NULL DEFAULT 0,
    text_mode     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    -- url-only: the URL string. The frozen JPEG preview from last close
    -- lives in the blobs table; preview_blob_id points at it (NULL until
    -- first close). Hash-deduped across clones the same way text content
    -- is — clones that haven't navigated independently share one row.
    url_string       TEXT,
    preview_blob_id  INTEGER REFERENCES blobs(id),
    -- Canonical display label. Stamped at insert time. The client renders
    -- alt_text verbatim (no derivation). Empty string until something stamps
    -- it (e.g. a URL tile before its page title is captured).
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

// tablesV1 is the FROZEN v1 grids/tiles/blobs schema. It is an immutable,
// byte-for-byte copy of what tablesTemplate was at the moment the localdb
// format was frozen (schemaVersion 1). NEVER edit it: tests build genuine
// "old files" from this text and migrate them forward, so editing it would
// rewrite history and hide migration bugs. New columns/tables go into
// tablesTemplate (the live schema) plus a migration — never here.
//
// TestSchemaEquivalence asserts (tablesV1 + all migrations) produces a schema
// identical to a fresh tablesTemplate; that is the proof that a brand-new DB
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
