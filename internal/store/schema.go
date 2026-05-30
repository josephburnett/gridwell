package store

// Schema is the canonical SQLite schema for Gridwell. Single-tenant: there is
// no users/groups/sessions table. The `system` KV table holds singleton state
// (root grid id and root viewport framing). Every tile has a kind
// (well/text/url/blackhole) and a version that bumps on content mutation;
// every grid carries a version too. Blobs hold markdown text bytes. Well
// rows carry one view rectangle (view_x, view_y, view_zoom) that is at once
// the preview frame, the descent target, and the ascent return. Text rows
// carry a doc-space window (text_x, text_y, text_w, text_h) plus a
// rendered/text mode. URL rows carry a URL string and the last-frozen JPEG
// preview captured at close.
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
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);

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
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','blackhole')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    -- well-only: the rectangle in child-grid coordinates that the well's
    -- preview shows, and that descent restores. One value, three jobs.
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
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (kind = 'well'      AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL)
    OR (kind = 'text'      AND child_grid_id IS NULL     AND blob_id IS NOT NULL AND url_string IS NULL     AND preview_jpeg IS NULL)
    OR (kind = 'url'       AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL)
    OR (kind = 'blackhole' AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id);
CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id);
`
