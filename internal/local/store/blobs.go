package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// IANA media types stamped on blobs so they are self-describing, independent
// of the tile column that references them.
const (
	mediaMarkdown = "text/markdown"
	mediaJPEG     = "image/jpeg"
)

// readBlob is the one blob read: the bytes with their IANA media type, so a
// reader reports what the blob is instead of hard-coding a type. The caller
// passes the snapshot it read the blob id in; see Store.readSnapshot.
func readBlob(ctx context.Context, q gridReader, blobID int64) ([]byte, string, error) {
	var (
		data      []byte
		mediaType string
	)
	err := q.QueryRowContext(ctx,
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
