package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetBlob returns the bytes of a blob and its mime type.
func (s *Store) GetBlob(ctx context.Context, blobID int64) ([]byte, string, error) {
	var (
		data []byte
		mime sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT data, mime_type FROM blobs WHERE id = ?`, blobID,
	).Scan(&data, &mime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("load blob: %w", err)
	}
	m := ""
	if mime.Valid {
		m = mime.String
	}
	return data, m, nil
}
