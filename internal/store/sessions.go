package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/josephburnett/ascent/internal/auth"
)

// SessionTTL is the cookie lifetime per spec §7.2.
const SessionTTL = 30 * 24 * time.Hour

// CreateSession inserts a new session row for userID and returns its token.
// The token is a freshly generated 32-byte random hex string.
func (s *Store) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := auth.NewSessionToken()
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		token, userID, now.Add(SessionTTL).Unix(), now.Unix())
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return token, nil
}

// LookupSession returns the user id for a non-expired session token. Expired
// sessions return ErrSessionExpired and are not auto-deleted (a periodic
// sweep can do that; deletion-on-read would race with concurrent uses).
func (s *Store) LookupSession(ctx context.Context, token string) (int64, error) {
	var (
		userID    int64
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token,
	).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if s.now().Unix() >= expiresAt {
		return 0, ErrSessionExpired
	}
	return userID, nil
}

// DeleteSession removes a session by token. Idempotent: deleting an unknown
// token is not an error.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// SweepExpiredSessions deletes session rows whose expiry has passed. Returns
// the number deleted. The server may call this on a timer.
func (s *Store) SweepExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
