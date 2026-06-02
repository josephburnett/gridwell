package store

// Schema is the canonical SQLite schema for Gridwell. Single-tenant: there
// is no users/groups/sessions table. The `system` KV table holds singleton
// state (root grid id and root viewport framing).
//
// There are six tile kinds:
//   - well       (interior): points at a child Gridwell-owned grid (blue).
//   - text       (interior): markdown blob (green).
//   - url        (interior): http(s) URL + frozen JPEG preview (purple).
//   - blackhole  (exit):     deletion sink (red).
//   - file-well  (exit):     points at a host directory; child grid's tile
//                            list is reconciled against that directory.
//   - process-well (exit):   points at a host PID; child grid's tile list
//                            is reconciled against the process table.
//
// Grids carry an optional (source_kind, source_id): NULL = regular
// Gridwell-owned, 'fs' = backed by a filesystem path, 'proc' = backed by
// a PID's child set. The (source_kind, source_id) pair is unique when
// non-NULL, so two file-wells at the same path share one backing grid.
//
// Well rows carry one view rectangle (view_x, view_y, view_zoom) that is
// at once the preview frame, the descent target, and the ascent return.
// Text rows carry a doc-space window (text_x, text_y, text_w, text_h)
// plus a rendered/text mode. URL rows carry a URL string and the
// last-frozen JPEG preview captured at close. File-well rows carry
// fs_path; process-well rows carry pid. Synthesized file-backed tiles
// inside an fs-grid carry fs_name (their basename within the parent).
const Schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS system (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- Keys: root_grid_id, root_view_cx, root_view_cy, root_zoom.

CREATE TABLE IF NOT EXISTS grids (
    id          INTEGER PRIMARY KEY,
    object_id   TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    refcount    INTEGER NOT NULL DEFAULT 1,
    source_kind TEXT,
    source_id   TEXT,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);
-- idx_grids_source is created by applyMigrations after it has ensured
-- the source_kind column exists. Listing it here would run against an
-- old DB before the migration adds the column.

CREATE TABLE IF NOT EXISTS blobs (
    id        INTEGER PRIMARY KEY,
    hash      TEXT NOT NULL UNIQUE,
    size      INTEGER NOT NULL,
    data      BLOB NOT NULL,
    refcount  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tiles (
    id            INTEGER PRIMARY KEY,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','blackhole','file-well','process-well')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    -- well / file-well / process-well: the rectangle in child-grid
    -- coordinates that the preview shows and descent restores.
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    child_grid_id INTEGER REFERENCES grids(id),
    -- text-only: the framed window in doc-space px (scroll offset + size)
    -- plus rendered/text mode.
    text_x        INTEGER NOT NULL DEFAULT 0,
    text_y        INTEGER NOT NULL DEFAULT 0,
    text_w        INTEGER NOT NULL DEFAULT 0,
    text_h        INTEGER NOT NULL DEFAULT 0,
    text_mode     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    -- url-only: the URL string and the frozen preview JPEG from last close.
    url_string    TEXT,
    preview_jpeg  BLOB,
    -- file-well-only / process-well-only: the FS path or PID this exit
    -- well points at. fs_name is the basename of a file-backed tile
    -- synthesized inside an fs-grid (NULL for tiles in regular grids).
    fs_path       TEXT,
    pid           INTEGER,
    fs_name       TEXT,
    -- Derived label used as the alt-text of dropped embed links. For URL
    -- tiles: the page title captured from Chromium. For text tiles: the
    -- first non-empty line of content with markdown stripped. NULL until
    -- derived.
    alt_text      TEXT,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (kind = 'well'         AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'text'         AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_jpeg IS NULL   AND fs_path IS NULL      AND pid IS NULL)
    OR (kind = 'url'          AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL    AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'blackhole'    AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'file-well'    AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NOT NULL AND pid IS NULL)
    OR (kind = 'process-well' AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`
