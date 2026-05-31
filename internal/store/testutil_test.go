package store

import "testing"

// refcount reads the refcount column from the named table for the given row id.
// table is always a hardcoded literal in callers (no user input).
func refcount(t *testing.T, s *Store, table string, id int64) int64 {
	t.Helper()
	var rc int64
	if err := s.db.QueryRow("SELECT refcount FROM "+table+" WHERE id = ?", id).Scan(&rc); err != nil {
		t.Fatalf("read %s.refcount id=%d: %v", table, id, err)
	}
	return rc
}
