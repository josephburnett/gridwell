package store

// Schema is the canonical SQLite schema for Gridwell. Single-tenant: there is
// no users/groups/sessions table and no per-row ownership. The `system` KV
// table holds singleton state (currently just the current root_grid_id).
const Schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS system (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS grids (
    id              INTEGER PRIMARY KEY,
    object_id       TEXT NOT NULL,
    refcount        INTEGER NOT NULL DEFAULT 1,
    default_view_cx REAL NOT NULL DEFAULT 0,
    default_view_cy REAL NOT NULL DEFAULT 0,
    default_zoom    REAL NOT NULL DEFAULT 1.0,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);

CREATE TABLE IF NOT EXISTS blobs (
    id        INTEGER PRIMARY KEY,
    hash      TEXT NOT NULL UNIQUE,
    size      INTEGER NOT NULL,
    mime_type TEXT,
    data      BLOB NOT NULL,
    refcount  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tiles (
    id            INTEGER PRIMARY KEY,
    object_id     TEXT NOT NULL,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    type          TEXT NOT NULL CHECK (type IN ('well','file')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    -- Text-file tiles: the document-space window (in fixed-scale doc px)
    -- the file was last framed at. view_x/view_y = scroll offset;
    -- view_w/view_h = the window size. The parent-grid preview crops this
    -- rectangle out of the re-rendered doc. 0 means "unset".
    view_w        INTEGER NOT NULL DEFAULT 0,
    view_h        INTEGER NOT NULL DEFAULT 0,
    -- Text-file tiles: "rendered" or "text" (raw). Persists the toggle so
    -- previews and re-descents reflect the last-used mode across reloads.
    file_mode     TEXT,
    child_grid_id INTEGER REFERENCES grids(id),
    capped        INTEGER NOT NULL DEFAULT 0,
    mime_type     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    -- URL tiles only (mime_type='text/uri-list'): the current URL,
    -- mutated by the server's Chromium driver as the live tab navigates.
    url_string    TEXT,
    -- URL tiles only: the latest captured JPEG preview frame.
    preview_jpeg  BLOB,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
        (type = 'well' AND child_grid_id IS NOT NULL AND mime_type IS NULL AND blob_id IS NULL AND url_string IS NULL AND preview_jpeg IS NULL) OR
        (type = 'file' AND child_grid_id IS NULL AND mime_type IS NOT NULL AND (
            (mime_type = 'text/uri-list' AND blob_id IS NULL AND url_string IS NOT NULL) OR
            (mime_type <> 'text/uri-list' AND blob_id IS NOT NULL AND url_string IS NULL AND preview_jpeg IS NULL)
        ))
    )
);
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`
