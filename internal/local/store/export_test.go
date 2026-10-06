package store

import (
	"context"
	"time"
)

// SetClock overrides the time source (test-only: deterministic stamps).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// SetIDGenerator overrides UUID generation (test-only: deterministic
// system.plugin_uuid).
func (s *Store) SetIDGenerator(f func() string) { s.newID = f }

// GetBlob returns the bytes of a blob.
func (s *Store) GetBlob(ctx context.Context, blobID int64) ([]byte, error) {
	data, _, err := s.GetBlobWithMedia(ctx, blobID)
	return data, err
}

// GetBlobWithMedia returns a blob's bytes with its media type.
func (s *Store) GetBlobWithMedia(ctx context.Context, blobID int64) ([]byte, string, error) {
	return readBlob(ctx, s.db, blobID)
}
