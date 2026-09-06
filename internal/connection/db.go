package connection

// The transport's store: what the node remembers about its connections beyond
// what server.yaml declares. That is the learned landing, the far node's home
// grid id, so a dark remote still has a room to show through the source cache.
// Everything else about a connection — where it is, how to reach it, what it
// is called — is config, read fresh every boot.
//
// The rows live in the node's one database and their shape belongs to
// internal/local/store, which renders the DDL from its column descriptor and
// migrates it with everything else. This file holds the queries only.
//
// The `deleted` flag is not a fact of its own: it is the mirror of the
// config's retired_names, which is the one owner of retirement, reconciled
// onto these rows at boot (see New) so route and Probe can read "retired" off
// the row they already hold. A retired name never returns and its namespace
// stays reserved forever, so stored references through it stay dangling rather
// than re-routed. A name the config merely stopped declaring is NOT retired:
// its row stays as it was, and its references come back with its stanza.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// ErrNotFound is the missing-row verdict.
var ErrNotFound = errors.New("connection: not found")

// DB is the connection store.
type DB struct {
	db    *sql.DB
	owned bool // Close closes the handle only when this DB opened it
}

// NewDB binds the connection store to the node's one database handle. A second
// handle on the same SQLite file would meet an instant SQLITE_BUSY. Close is a
// no-op; the store owns the handle.
//
// The connections table is the store's too: internal/local/store renders its
// DDL from the column descriptor and the migration chain evolves it, so this
// package holds no DDL and only the queries below. What is checked here is
// that the handle really came from an opened store — a handle the store never
// migrated would otherwise fail at the first Get, long after the wiring
// mistake that caused it.
func NewDB(db *sql.DB) (*DB, error) {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'connections'`).
		Scan(&n); err != nil {
		return nil, fmt.Errorf("connection: look for the connections table: %w", err)
	}
	if n == 0 {
		return nil, errors.New("connection: no connections table on this handle; " +
			"the node's store owns that DDL, so the handle must come from store.Open")
	}
	return &DB{db: db}, nil
}

// Close closes the handle when this DB opened it; a shared handle is the
// store's to close.
func (d *DB) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}

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

// Tombstone retires name forever, creating the row if it never existed: a
// retired_names entry on a fresh store is still a reservation.
func (d *DB) Tombstone(ctx context.Context, name string) error {
	if err := d.Ensure(ctx, name); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `UPDATE connections SET deleted = 1 WHERE name = ?`, name)
	return err
}

// Revive clears a tombstone the config's retired_names does not hold. It
// un-retires nothing: retirement lives in retired_names, and this only brings
// the mirror back in line with it. Everything else on the row — the learned
// landing above all — is untouched.
func (d *DB) Revive(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE connections SET deleted = 0 WHERE name = ?`, name)
	return err
}
