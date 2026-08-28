package store

import "time"

// SetClock overrides the time source (test-only: deterministic stamps).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// SetIDGenerator overrides UUID generation (test-only: deterministic
// object_ids).
func (s *Store) SetIDGenerator(f func() string) { s.newID = f }
