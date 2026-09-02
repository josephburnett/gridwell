package connection

import (
	"database/sql"
	"fmt"
)

// OpenDB opens a standalone connection store at path: the test-only shape.
// Production shares the node's one handle through NewDB.
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("connection: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	d, err := NewDB(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	d.owned = true
	return d, nil
}
