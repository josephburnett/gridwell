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
//
// The tiles table is built by tilesTableDDL so its one DDL source is shared
// with the CHECK-rebuild migration path (migrations.go): a rebuild creates
// tiles_new from the same text a fresh Open uses, so the two cannot drift.
func tablesDDL() string { return tablesTemplate + tilesTableDDL("tiles") + tilesIndexDDL }

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
    updated_at  INTEGER NOT NULL DEFAULT 0,
    -- ns names the grid's owner: '' = home; a plugin id = that plugin's
    -- memory (docs/one-node.md §2.6 — one table for every namespace).
    -- context_key is the plugin's stable key for the context this grid
    -- projects ('' for home grids). root_cx/cy/zoom: a plugin context's
    -- persisted root viewport (NULL = never set; home keeps its own in
    -- the system table). Added post-v1 (schema v9, additive).
    ns          TEXT NOT NULL DEFAULT '',
    context_key TEXT NOT NULL DEFAULT '',
    root_cx     REAL,
    root_cy     REAL,
    root_zoom   REAL
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);
-- listings: a plugin context's last good listing (an opaque blob the
-- adapter serializes) — the offline answer (v2 tenet 6). Schema v9.
CREATE TABLE IF NOT EXISTS listings (
    grid_id       INTEGER PRIMARY KEY REFERENCES grids(id),
    entries       BLOB NOT NULL,
    authoritative INTEGER NOT NULL DEFAULT 0
);

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
// CHECK — see internal/store/CLAUDE.md). One text, two readers, no drift.
func tilesTableDDL(name string) string {
	return `
CREATE TABLE IF NOT EXISTS ` + name + ` (
    -- AUTOINCREMENT for the same reason as grids: a reused tile id would
    -- collide with the client's per-tile caches (e.g. the URL preview cache
    -- keyed by tile id), showing a deleted tile's frozen frame on a new one.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','shell','pane')),
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
    -- alt_user=1 marks alt_text as USER-OWNED (the rename gesture, issue
    -- #61): automatic captures (a url's page title on freeze, a shell's
    -- foreground command on detach) must not overwrite a name the user set.
    -- Added post-v1 (schema v2, additive).
    alt_user      INTEGER NOT NULL DEFAULT 0,
    -- content_zoom scales the content rendered INSIDE a text/shell/url tile
    -- (the text font, the terminal font, the page zoom — issue #82).
    -- Framing, never bumps version; 0 = unset (renders at 1.0). Added
    -- post-v1 (schema v3, additive).
    content_zoom  REAL NOT NULL DEFAULT 0,
    -- url_history is a url tile's persisted navigation back-stack (JSON
    -- {index, entries:[{url,title}]}, capped) captured at freeze so a
    -- revived tile can still go "back" (issue #113). Content, rides the
    -- versioned freeze writeback. Added post-v1 (schema v4, additive).
    url_history   TEXT,
    -- link_target_id makes a LEAF tile (text/url/shell/pane) a LINK: a
    -- qualified "<uuid>/<tile-id>" reference to the tile that owns the
    -- content. NULL = an ordinary owned tile. A link row stores no content
    -- of its own (the CHECK's link branch enforces it) — readers resolve
    -- bytes/preview/session through the target id. The well kind's link
    -- variant remains a qualified child_grid_id (the exit well); this
    -- column is never set on wells. Added post-v1 (schema v6, rebuild —
    -- the CHECK gained the link branch).
    link_target_id TEXT,
    -- url_frozen=1 is the USER'S standing freeze on a url tile (issue
    -- #237): descending does not auto-go-live until the reconnect gesture
    -- clears it. Framing, never bumps version. Added post-v1 (schema v7,
    -- additive).
    url_frozen    INTEGER NOT NULL DEFAULT 0,
    -- configure_plugin_id marks a CHILDLESS well as an UNCONFIGURED PLUGIN
    -- WELL (issue #251): the uuid of the parameterized plugin whose
    -- instance will fill it. Adoption sets child_grid_id and the uuid
    -- stays as provenance. '' for every other tile. Added post-v1 (schema
    -- v8, rebuild — the well CHECK branch gained the childless variant).
    configure_plugin_id TEXT NOT NULL DEFAULT '',
    -- ns/key/tombstoned: an EXTERNAL's row (docs/one-node.md §2.6). ns is
    -- the owning plugin id ('' = home); key is the plugin's stable key
    -- for the entry; tombstoned=1 retires the key forever (the id is
    -- never reused; a recreated key mints fresh). Home rows carry the
    -- defaults. Added post-v1 (schema v9, additive).
    ns            TEXT NOT NULL DEFAULT '',
    key           TEXT NOT NULL DEFAULT '',
    tombstoned    INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (link_target_id IS NULL AND (
          -- well: an interior/exit well has a child grid; the one childless
          -- shape is the unconfigured plugin well (configure_plugin_id set).
          (kind = 'well'  AND (child_grid_id IS NOT NULL OR configure_plugin_id != '') AND blob_id IS NULL AND url_string IS NULL AND preview_blob_id IS NULL AND text_mode IS NULL)
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
);
`
}

// tilesIndexDDL recreates the tiles indexes; shared by the fresh path
// (tablesDDL) and the rebuild migration for the same no-drift reason as
// tilesTableDDL.
const tilesIndexDDL = `
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id);
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
