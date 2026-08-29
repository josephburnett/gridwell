package remote

// The transport's store: what the node REMEMBERS about its connections
// beyond what server.yaml declares — the learned landing (the remote's
// home grid id, so a dark remote still has a room to show through the
// mount cache) and the graveyard (a retired name never returns: its
// namespace stays reserved forever, because stored references through it
// must stay dangling rather than re-routed). Everything else about a
// connection — where it is, how to reach it, what it is called — is
// config, read fresh every boot.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// ErrNotFound is the missing-row verdict.
var ErrNotFound = errors.New("remote: not found")

// DB is the connection store.
type DB struct {
	db *sql.DB
}

const connSchema = `
CREATE TABLE IF NOT EXISTS connections (
  name        TEXT    PRIMARY KEY,
  remote_root TEXT    NOT NULL DEFAULT '',
  deleted     INTEGER NOT NULL DEFAULT 0
);`

// OpenDB opens (creating the table as needed) the connection store.
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("remote: open %q: %w", path, err)
	}
	// SQLite is single-writer at the file level; one connection eliminates
	// pool-vs-pool lock races and gives deterministic interleaving.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(connSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("remote: init schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the store.
func (d *DB) Close() error { return d.db.Close() }

// Stored is one remembered connection.
type Stored struct {
	Name       string
	RemoteRoot string
	Deleted    bool
}

// Get returns the row for name, or ErrNotFound.
func (d *DB) Get(ctx context.Context, name string) (Stored, error) {
	var r Stored
	var del int
	err := d.db.QueryRowContext(ctx, `SELECT name, remote_root, deleted FROM connections WHERE name = ?`, name).
		Scan(&r.Name, &r.RemoteRoot, &del)
	if errors.Is(err, sql.ErrNoRows) {
		return Stored{}, ErrNotFound
	}
	if err != nil {
		return Stored{}, err
	}
	r.Deleted = del != 0
	return r, nil
}

// List returns every row, tombstones included, by name.
func (d *DB) List(ctx context.Context) ([]Stored, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name, remote_root, deleted FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		var r Stored
		var del int
		if err := rows.Scan(&r.Name, &r.RemoteRoot, &del); err != nil {
			return nil, err
		}
		r.Deleted = del != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ensure creates the row for name if absent (a fresh declaration).
func (d *DB) Ensure(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO connections (name) VALUES (?)`, name)
	return err
}

// SetRemoteRoot records the learned landing.
func (d *DB) SetRemoteRoot(ctx context.Context, name, root string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE connections SET remote_root = ? WHERE name = ?`, root, name)
	return err
}

// Tombstone retires name forever (creating the row if it never existed —
// a retired_names entry on a fresh store is still a reservation).
func (d *DB) Tombstone(ctx context.Context, name string) error {
	if err := d.Ensure(ctx, name); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `UPDATE connections SET deleted = 1 WHERE name = ?`, name)
	return err
}
