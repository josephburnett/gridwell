package remote

import (
	"database/sql"
	"fmt"
)

// OpenDB opens a standalone connection store at path — the test-only
// shape; production shares the node's one handle (NewDB).
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("remote: open %q: %w", path, err)
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
