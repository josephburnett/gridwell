package store

import (
	"context"
	"fmt"
)

// Test-only pragma access: the migration/durability tests read version
// stamps and INJECT corrupt ones. Moved out of the shipped code (deadcode
// sweep 2026-08-08) — production reads/writes pragmas through
// internal/dbformat only.

// readPragmaInt reads an integer-valued PRAGMA (e.g. application_id,
// user_version) from the main database.
func readPragmaInt(ctx context.Context, q gridReader, name string) (int64, error) {
	var v int64
	// PRAGMA names are trusted literals here, never user input.
	if err := q.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setPragmaInt writes an integer-valued PRAGMA. PRAGMA statements can't bind
// parameters, so the value is formatted in; callers pass trusted constants.
func (s *Store) setPragmaInt(ctx context.Context, name string, v int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
