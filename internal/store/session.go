package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSession returns this DB's stored Chromium session blob (cookies + web
// storage), or (nil, nil) when none has been captured yet. The blob is opaque —
// the store never interprets it; the Electron host snapshots/restores it into a
// per-plugin partition.
func (s *Store) GetSession(ctx context.Context) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM session WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	return data, nil
}

// PutSession replaces this DB's session blob (last-writer-wins checkout/checkin;
// a DB hosts one session, shared by all its url tiles).
func (s *Store) PutSession(ctx context.Context, data []byte) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO session (id, data) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`, data,
	); err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	return nil
}
