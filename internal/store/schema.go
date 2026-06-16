package store

import "strings"

// This file is the canonical SQLite schema for Gridwell. Single-tenant:
// there is no users/groups/sessions table. The `system` KV table holds
// singleton state (root grid id and root viewport framing); the schema
// version lives in the SQLite header (PRAGMA user_version), not here.
//
// There are seven tile kinds:
//   - well       (interior): points at a child Gridwell-owned grid (blue).
//   - text       (interior): markdown blob (green).
//   - url        (interior): http(s) URL + frozen JPEG preview (purple).
//   - blackhole  (exit):     deletion sink (red).
//   - file-well  (exit):     points at a host directory; child grid's tile
//     list is reconciled against that directory.
//   - process-well (exit):   points at a host PID; child grid's tile list
//     is reconciled against the process table.
//   - shell      (exit):     interactive bash session inside a gridwell-
//     private tmux session. Live mode attaches the tmux client; freeze
//     captures a JPEG preview and detaches. The bash + scrollback live in
//     the tmux server and persist across ascents (and gridwell restarts);
//     they are gone only when the tile is deleted or the host reboots.
//
// Grids carry an optional (source_kind, source_id): NULL = regular
// Gridwell-owned, 'fs' = backed by a filesystem path, 'proc' = backed by
// a PID's child set. The (source_kind, source_id) pair is unique when
// non-NULL, so two file-wells at the same path share one backing grid.
//
// The grids/tiles/blobs table shapes are materialized in both the durable
// main database and the attached ephemeral `cache` database (see attach.go),
// so their DDL is parameterized by schema prefix via tablesDDL. A test
// asserts the two materializations don't drift.
//
// Well rows carry one view rectangle (view_x, view_y, view_zoom) that is
// at once the preview frame, the descent target, and the ascent return.
// Text rows carry a doc-space window (text_x, text_y, text_w, text_h)
// plus a rendered/text mode plus blob_id (the markdown source). URL rows
// carry a URL string and preview_blob_id (the last-frozen JPEG captured
// at close, hash-deduped through the blobs table just like text content).
// File-well rows carry fs_path; process-well rows carry pid. Synthesized
// file-backed tiles inside an fs-grid carry source_key (their basename
// within the parent); proc-grid tiles carry the PID string as
// source_key (and "@info" for the well-self tile).

// pragmas are connection-level settings applied once at Open, before any
// schema or attach.
const pragmas = `
PRAGMA journal_mode=WAL;
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

// tablesDDL returns the grids/tiles/blobs DDL for a schema: prefix "" for the
// durable main database, "cache." for the attached ephemeral one. Foreign
// keys and index ON-targets stay unqualified so they resolve within whichever
// schema the statement runs against (SQLite has no cross-database FKs).
func tablesDDL(schemaPrefix string) string {
	return strings.ReplaceAll(tablesTemplate, "{{P}}", schemaPrefix)
}

const tablesTemplate = `
CREATE TABLE IF NOT EXISTS {{P}}grids (
    -- AUTOINCREMENT so a deleted grid's id is never reused. Without it,
    -- SQLite recycles the rowid of a deleted grid (e.g. an interior well that
    -- was deleted, taking its owned child grid with it), and a new grid taking
    -- that id would collide with the client's still-cached copy of the old
    -- grid — making a fresh file-well render the deleted well's tiles. Fresh
    -- ids keep the client cache (keyed by id) honest.
    --
    -- No refcount: regular grids are owned 1:1 by their parent well (copy-on-
    -- clone never shares a grid), and host-backed source grids are shared by
    -- identity but disposable, so neither is reference-counted. Only blobs are.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id   TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    source_kind TEXT,
    source_id   TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS {{P}}idx_grids_object_id ON grids(object_id);
CREATE UNIQUE INDEX IF NOT EXISTS {{P}}idx_grids_source ON grids(source_kind, source_id) WHERE source_kind IS NOT NULL;

CREATE TABLE IF NOT EXISTS {{P}}blobs (
    -- AUTOINCREMENT: blob ids feed the client's (tile id, blob id) preview
    -- cache key, so a recycled blob id could serve stale image bytes.
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL UNIQUE,
    size       INTEGER NOT NULL,
    data       BLOB NOT NULL,
    refcount   INTEGER NOT NULL DEFAULT 0,
    -- Self-describing media: an IANA type ('text/markdown', 'image/jpeg')
    -- so a blob is interpretable on its own, independent of the column that
    -- points at it. created_at is first-seen time; blobs are immutable
    -- (content-addressed), so there is no updated_at.
    media_type TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS {{P}}tiles (
    -- AUTOINCREMENT for the same reason as grids: a reused tile id would
    -- collide with the client's per-tile caches (e.g. the URL preview cache
    -- keyed by tile id), showing a deleted tile's frozen frame on a new one.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','blackhole','file-well','process-well','shell')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    -- well / file-well / process-well: the rectangle in child-grid
    -- coordinates that the preview shows and descent restores.
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    -- No FK: an interior well's child grid is in this same (main) DB, but a
    -- file/process well in a main grid points at a source grid in the
    -- attached cache DB, and SQLite has no cross-file foreign keys. Integrity
    -- for interior wells rests on the refcount machinery + property test.
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
    -- file-well-only / process-well-only: the FS path or PID this exit
    -- well points at. source_key is the per-source dedup identifier
    -- for tiles synthesized inside an fs/proc-grid — basename for fs,
    -- PID string (or "@info" for the well-self tile) for proc. NULL
    -- for tiles in regular Gridwell-owned grids.
    fs_path       TEXT,
    pid           INTEGER,
    source_key    TEXT,
    -- Canonical display label. Stamped at insert time and refreshed by
    -- the source-grid reconciler. The client renders alt_text verbatim
    -- (no derivation). Empty string until something stamps it (e.g. a
    -- URL tile before its page title is captured).
    alt_text      TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (kind = 'well'         AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_blob_id IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'text'         AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_blob_id IS NULL AND fs_path IS NULL      AND pid IS NULL)
    OR (kind = 'url'          AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL    AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'blackhole'    AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND preview_blob_id IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'file-well'    AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL AND fs_path IS NOT NULL AND pid IS NULL)
    OR (kind = 'process-well' AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL AND fs_path IS NULL AND pid IS NOT NULL)
    OR (kind = 'shell'        AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS {{P}}idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX IF NOT EXISTS {{P}}idx_tiles_object_id ON tiles(object_id);
CREATE INDEX IF NOT EXISTS {{P}}idx_tiles_child     ON tiles(child_grid_id);
`
