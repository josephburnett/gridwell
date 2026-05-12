package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetBlob returns the bytes of a blob and its mime type. The user must have
// read access to at least one tile referencing the blob; otherwise we return
// ErrPermissionDenied.
//
// Permission via "any tile grants read" rather than "all tiles" is the
// natural rule because blobs are content-addressed and shared across tiles
// the user has no business knowing about. If the user can read one tile,
// they can already see the bytes; we just expose the same view via blob id.
func (s *Store) GetBlob(ctx context.Context, userID, blobID int64) ([]byte, string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, owner_id, group_id, mode FROM tiles WHERE blob_id = ?`, blobID)
	if err != nil {
		return nil, "", err
	}
	type row struct{ id, owner, group int64; mode int32 }
	var refs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.owner, &r.group, &r.mode); err != nil {
			rows.Close()
			return nil, "", err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if len(refs) == 0 {
		return nil, "", ErrNotFound
	}
	allowed := false
	for _, r := range refs {
		read, _, err := s.permFor(ctx, s.db, userID, r.owner, r.group, r.mode)
		if err != nil {
			return nil, "", err
		}
		if read {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", ErrPermissionDenied
	}

	var (
		data []byte
		mime sql.NullString
	)
	err = s.db.QueryRowContext(ctx,
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
