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
	data, _, err := s.GetBlobWithMedia(ctx, blobID)
	return data, err
}

// GetBlobWithMedia returns a blob's bytes together with its self-describing
// IANA media type. The media type travels with the bytes so a reader reports
// what the blob actually is instead of hard-coding a type at the call site.
func (s *Store) GetBlobWithMedia(ctx context.Context, blobID int64) ([]byte, string, error) {
	var (
		data      []byte
		mediaType string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT data, media_type FROM blobs WHERE id = ?`, blobID,
	).Scan(&data, &mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("load blob: %w", err)
	}
	return data, mediaType, nil
}
