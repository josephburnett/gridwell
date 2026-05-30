package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetBlob returns the bytes of a blob.
func (s *Store) GetBlob(ctx context.Context, blobID int64) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM blobs WHERE id = ?`, blobID,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load blob: %w", err)
	}
	return data, nil
}
