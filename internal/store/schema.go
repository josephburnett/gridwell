package store

// Schema is the canonical SQLite schema for Ascent. It mirrors spec §9.1.
//
// We deliberately keep this as a single string rather than a migration ladder
// because v1 has no deployed users — there is nothing to migrate from. When
// that changes, swap this for a migrations package.
const Schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS groups (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

-- grids.owner_id references users(id) and users.root_grid_id references
-- grids(id), forming a cycle. Bootstrap (CreateUser) handles this by
-- enabling PRAGMA defer_foreign_keys for the duration of the transaction.
CREATE TABLE IF NOT EXISTS grids (
    id              INTEGER PRIMARY KEY,
    object_id       TEXT NOT NULL,
    owner_id        INTEGER NOT NULL REFERENCES users(id),
    group_id        INTEGER NOT NULL REFERENCES groups(id),
    mode            INTEGER NOT NULL,
    refcount        INTEGER NOT NULL DEFAULT 1,
    default_view_cx REAL NOT NULL DEFAULT 0,
    default_view_cy REAL NOT NULL DEFAULT 0,
    default_zoom    REAL NOT NULL DEFAULT 1.0,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grids_object_id ON grids(object_id);

CREATE TABLE IF NOT EXISTS users (
    id               INTEGER PRIMARY KEY,
    username         TEXT NOT NULL UNIQUE,
    password_hash    TEXT NOT NULL,
    primary_group_id INTEGER NOT NULL REFERENCES groups(id),
    root_grid_id     INTEGER NOT NULL REFERENCES grids(id),
    created_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id  INTEGER NOT NULL REFERENCES users(id),
    group_id INTEGER NOT NULL REFERENCES groups(id),
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS blobs (
    id        INTEGER PRIMARY KEY,
    hash      TEXT NOT NULL UNIQUE,
    size      INTEGER NOT NULL,
    mime_type TEXT,
    data      BLOB NOT NULL,
    refcount  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS nodes (
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
    child_grid_id INTEGER REFERENCES grids(id),
    capped        INTEGER NOT NULL DEFAULT 0,
    mime_type     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    owner_id      INTEGER NOT NULL REFERENCES users(id),
    group_id      INTEGER NOT NULL REFERENCES groups(id),
    mode          INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
        (type = 'well' AND child_grid_id IS NOT NULL AND mime_type IS NULL AND blob_id IS NULL) OR
        (type = 'file' AND child_grid_id IS NULL     AND mime_type IS NOT NULL AND blob_id IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_nodes_grid_id   ON nodes(grid_id);
CREATE INDEX IF NOT EXISTS idx_nodes_object_id ON nodes(object_id);
CREATE INDEX IF NOT EXISTS idx_nodes_child     ON nodes(child_grid_id);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`
