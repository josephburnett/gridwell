package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// IANA media types stamped on blobs so they are self-describing,
// independent of the tile column that references them. Markdown source
// (text blob_id) and frozen previews (url/shell preview_blob_id) are the
// only two kinds of bytes the store holds today.
const (
	mediaMarkdown = "text/markdown"
	mediaJPEG     = "image/jpeg"
)

// GetBlob returns the bytes of a blob.
func (s *Store) GetBlob(ctx context.Context, blobID int64) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM `+schemaOf(blobID)+`blobs WHERE id = ?`, blobID,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load blob: %w", err)
	}
	return data, nil
}
