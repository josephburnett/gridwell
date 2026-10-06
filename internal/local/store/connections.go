package store

// The connections table: what the node remembers about a connection beyond
// server.yaml (see connectionsColumns). internal/connection decides what to
// write; each write goes through withMutation and names its row "conn/<name>".

import (
	"context"
	"database/sql"
	"errors"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// ConnectionRow is one remembered connection.
type ConnectionRow struct {
	Name       string
	RemoteRoot string
	Deleted    bool
}

func scanConnection(sc interface{ Scan(...any) error }) (ConnectionRow, error) {
	var r ConnectionRow
	var del int
	if err := sc.Scan(&r.Name, &r.RemoteRoot, &del); err != nil {
		return ConnectionRow{}, err
	}
	r.Deleted = del != 0
	return r, nil
}

// Connection returns the row for name, or ErrNotFound.
func (s *Store) Connection(ctx context.Context, name string) (ConnectionRow, error) {
	r, err := scanConnection(s.db.QueryRowContext(ctx,
		`SELECT name, remote_root, deleted FROM connections WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectionRow{}, ErrNotFound
	}
	return r, err
}

// Connections returns every row, retired ones included, by name.
func (s *Store) Connections(ctx context.Context) ([]ConnectionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, remote_root, deleted FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionRow
	for rows.Next() {
		r, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) writeConnection(ctx context.Context, verb, name string, fn func(tx *sql.Tx) error) error {
	return s.withMutation(ctx, verb, func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return fn(tx)
	}, "conn/"+name)
}

func ensureConnection(ctx context.Context, tx *sql.Tx, name string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO connections (name) VALUES (?)`, name)
	return err
}

// DeclareConnection creates the row for name if absent.
func (s *Store) DeclareConnection(ctx context.Context, name string) error {
	return s.writeConnection(ctx, "DeclareConnection", name, func(tx *sql.Tx) error {
		return ensureConnection(ctx, tx, name)
	})
}

// SetConnectionRoot records the landing a connection learned.
func (s *Store) SetConnectionRoot(ctx context.Context, name, root string) error {
	return s.writeConnection(ctx, "SetConnectionRoot", name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE connections SET remote_root = ? WHERE name = ?`, root, name)
		return err
	})
}

// SetConnectionRetired mirrors retired_names onto the row. Retiring creates
// the row if it never existed, since a retired name on a fresh store is still
// a reservation; reviving touches nothing else on the row.
func (s *Store) SetConnectionRetired(ctx context.Context, name string, retired bool) error {
	verb, del := "ReviveConnection", 0
	if retired {
		verb, del = "RetireConnection", 1
	}
	return s.writeConnection(ctx, verb, name, func(tx *sql.Tx) error {
		if retired {
			if err := ensureConnection(ctx, tx, name); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE connections SET deleted = ? WHERE name = ?`, del, name)
		return err
	})
}
