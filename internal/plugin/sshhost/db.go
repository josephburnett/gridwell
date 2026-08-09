// Package sshhost is the multi-connection ssh plugin (issue #199): one plugin
// process hosting MANY remote-node mounts, each a connection WELL the user
// drops on the plugin's root grid. The connection's parameters (host, user,
// key path, …) are the well's CONTENT — committed through the one content
// door (WriteContent) via the #198 creation schema — and its child is the
// remote's node grid, reached through a per-connection sub-namespace segment
// the plugin mints and routes: `<ssh-plugin>/<conn>/<remote-plugin>/<id>`.
// The plugin peels its connection segment exactly as a node peels a plugin
// segment (rpc.TransitQualifyTiles — the same one transit rule), so
// namespaces recurse and server.yaml stops naming hosts.
package sshhost

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/store"

	// This package opens its own DB handle; own the driver registration
	// (the pluginmeta lesson: never rely on another linked package's import).
	_ "modernc.org/sqlite"
)

// Conn is one persisted connection row. ID is the well tile's local numeric
// id in the root grid (AUTOINCREMENT, never reused); NS is the minted
// letter-leading short id that names the connection's sub-namespace in
// chained ids. Both are immutable once minted — NS lives inside stored
// references — so a deleted connection is TOMBSTONED, never dropped: the
// segment stays reserved forever and resolves to "gone", not to a stranger.
type Conn struct {
	ID       int64
	NS       string
	ObjectID string
	Params   string // the params JSON document (the well's content); "" until committed
	Version  int64
	X, Y     int64
	W, H     int64
	AltText  string
	AltUser  bool // the rename latch: a user-set name defers the auto label
	ViewX    int64
	ViewY    int64
	ViewZoom float64
	// RemoteRoot caches the remote node's root grid id (its node grid, e.g.
	// "rnode/0"), learned from the remote's Info on first successful contact.
	// "" = never reached: the well has no child and is inert until the remote
	// answers. Cleared when params change (the old cache may name the wrong
	// machine).
	RemoteRoot string
	Deleted    bool
}

// DB is the plugin's connection store, sharing the plugin's own SQLite file
// with pluginmeta's identity table. Additive-only, like every Gridwell DB.
type DB struct {
	db *sql.DB
}

const connSchema = `
CREATE TABLE IF NOT EXISTS ssh_connections (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ns          TEXT    NOT NULL UNIQUE,
  object_id   TEXT    NOT NULL DEFAULT '',
  params      TEXT    NOT NULL DEFAULT '',
  version     INTEGER NOT NULL DEFAULT 1,
  x           INTEGER NOT NULL DEFAULT 0,
  y           INTEGER NOT NULL DEFAULT 0,
  w           INTEGER NOT NULL DEFAULT 1,
  h           INTEGER NOT NULL DEFAULT 1,
  alt_text    TEXT    NOT NULL DEFAULT '',
  alt_user    INTEGER NOT NULL DEFAULT 0,
  view_x      REAL    NOT NULL DEFAULT 0,
  view_y      REAL    NOT NULL DEFAULT 0,
  view_zoom   REAL    NOT NULL DEFAULT 0,
  remote_root TEXT    NOT NULL DEFAULT '',
  deleted     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ssh_meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);`

// OpenDB opens (creating tables as needed) the connection store inside the
// plugin's DB file.
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sshhost: open %q: %w", path, err)
	}
	// SQLite is single-writer at the file level; one connection eliminates
	// pool-vs-pool lock races (instant SQLITE_BUSY — there's no
	// busy_timeout) and gives deterministic transaction interleaving. Same
	// discipline as the store, fs, and proc.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(connSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sshhost: init schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.db.Close() }

// ErrVersionConflict mirrors the store's optimistic-concurrency refusal; the
// server maps it to the same wire code every other conflict gets.
var ErrVersionConflict = fmt.Errorf("sshhost: version conflict")

// ErrNotFound is the missing/tombstoned-row verdict.
var ErrNotFound = fmt.Errorf("sshhost: not found")

const connCols = `id, ns, object_id, params, version, x, y, w, h, alt_text, alt_user, view_x, view_y, view_zoom, remote_root, deleted`

func scanConn(row interface{ Scan(...any) error }) (*Conn, error) {
	var c Conn
	var altUser, deleted int64
	err := row.Scan(&c.ID, &c.NS, &c.ObjectID, &c.Params, &c.Version,
		&c.X, &c.Y, &c.W, &c.H, &c.AltText, &altUser,
		&c.ViewX, &c.ViewY, &c.ViewZoom, &c.RemoteRoot, &deleted)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.AltUser = altUser != 0
	c.Deleted = deleted != 0
	return &c, nil
}

// Create mints a new connection: a fresh AUTOINCREMENT row id, a fresh
// letter-leading short id for the sub-namespace, and a provenance object_id.
func (d *DB) Create(ctx context.Context, x, y, w, h int64, alt string) (*Conn, error) {
	ns := store.NewShortID()
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO ssh_connections (ns, object_id, x, y, w, h, alt_text) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ns, store.NewUUID(), x, y, w, h, alt)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// Get returns the row for id, tombstoned or not — the caller decides whether
// "deleted" means NotFound (reads) or GONE (Probe).
func (d *DB) Get(ctx context.Context, id int64) (*Conn, error) {
	return scanConn(d.db.QueryRowContext(ctx,
		`SELECT `+connCols+` FROM ssh_connections WHERE id = ?`, id))
}

// GetByNS resolves a connection by its minted namespace segment.
func (d *DB) GetByNS(ctx context.Context, ns string) (*Conn, error) {
	return scanConn(d.db.QueryRowContext(ctx,
		`SELECT `+connCols+` FROM ssh_connections WHERE ns = ?`, ns))
}

// List returns all live (non-tombstoned) connections in id order.
func (d *DB) List(ctx context.Context) ([]*Conn, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+connCols+` FROM ssh_connections WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conn
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// checkClaim loads a LIVE row and verifies the caller's version claim.
func (d *DB) checkClaim(ctx context.Context, id, claim int64) (*Conn, error) {
	c, err := d.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Deleted {
		return nil, ErrNotFound
	}
	if c.Version != claim {
		return nil, fmt.Errorf("%w: tile %d is at version %d, claim was %d", ErrVersionConflict, id, c.Version, claim)
	}
	return c, nil
}

// SetParams commits the params document — a CONTENT edit: claims the version
// and bumps it. The remote-root cache is cleared (the old value may describe
// a different machine); the caller re-learns it from the new endpoint.
func (d *DB) SetParams(ctx context.Context, id, claim int64, params string) (*Conn, error) {
	if _, err := d.checkClaim(ctx, id, claim); err != nil {
		return nil, err
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET params = ?, remote_root = '', version = version + 1 WHERE id = ?`,
		params, id); err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// SetFraming persists the well's viewport — framing-class, never bumps.
func (d *DB) SetFraming(ctx context.Context, id, vx, vy int64, vz float64) (*Conn, error) {
	c, err := d.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Deleted {
		return nil, ErrNotFound
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET view_x = ?, view_y = ?, view_zoom = ? WHERE id = ?`,
		vx, vy, vz, id); err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// SetPlacement moves/resizes the well — versioned like the store's PlaceTile.
func (d *DB) SetPlacement(ctx context.Context, id, claim, x, y, w, h int64) (*Conn, error) {
	if _, err := d.checkClaim(ctx, id, claim); err != nil {
		return nil, err
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET x = ?, y = ?, w = ?, h = ?, version = version + 1 WHERE id = ?`,
		x, y, w, h, id); err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// Rename is the versioned user rename; it latches alt_user so the automatic
// host label defers from then on (the store.SetTileAlt discipline).
func (d *DB) Rename(ctx context.Context, id, claim int64, alt string) (*Conn, error) {
	if _, err := d.checkClaim(ctx, id, claim); err != nil {
		return nil, err
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET alt_text = ?, alt_user = 1, version = version + 1 WHERE id = ?`,
		alt, id); err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// SetRemoteRoot caches the learned remote root — a derived fact, not a user
// edit: no version bump.
func (d *DB) SetRemoteRoot(ctx context.Context, id int64, root string) (*Conn, error) {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET remote_root = ? WHERE id = ? AND deleted = 0`, root, id); err != nil {
		return nil, err
	}
	return d.Get(ctx, id)
}

// Tombstone unlinks the connection. The ROW STAYS: ns is reserved forever
// (stored references may name it), and AUTOINCREMENT never reuses the id.
func (d *DB) Tombstone(ctx context.Context, id, claim int64) error {
	if _, err := d.checkClaim(ctx, id, claim); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE ssh_connections SET deleted = 1 WHERE id = ?`, id)
	return err
}

// RootView reads the plugin root grid's persisted viewport (zeros = unset).
func (d *DB) RootView(ctx context.Context) (cx, cy, zoom float64, err error) {
	row := d.db.QueryRowContext(ctx, `SELECT v FROM ssh_meta WHERE k = 'root_view'`)
	var v string
	if err := row.Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, err
	}
	_, err = fmt.Sscanf(v, "%g %g %g", &cx, &cy, &zoom)
	return cx, cy, zoom, err
}

// SetRootView persists the plugin root grid's viewport (framing only).
func (d *DB) SetRootView(ctx context.Context, cx, cy, zoom float64) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO ssh_meta (k, v) VALUES ('root_view', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		fmt.Sprintf("%g %g %g", cx, cy, zoom))
	return err
}

// GridVersion returns the root grid's monotonic version (structural changes).
func (d *DB) GridVersion(ctx context.Context) (int64, error) {
	row := d.db.QueryRowContext(ctx, `SELECT v FROM ssh_meta WHERE k = 'grid_version'`)
	var v int64
	if err := row.Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return 1, nil
		}
		return 0, err
	}
	return v, nil
}

// BumpGridVersion advances the root grid version.
func (d *DB) BumpGridVersion(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO ssh_meta (k, v) VALUES ('grid_version', '2')
		 ON CONFLICT(k) DO UPDATE SET v = CAST(CAST(v AS INTEGER) + 1 AS TEXT)`)
	return err
}
