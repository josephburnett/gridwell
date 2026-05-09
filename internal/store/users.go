package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/josephburnett/ascent/internal/auth"
)

// User is the read view of a user row.
type User struct {
	ID             int64
	Username       string
	PrimaryGroupID int64
	RootGridID     int64
}

// CreateUser provisions a new user with their primary group and an empty
// root grid, all in a single transaction. Returns the populated User on
// success.
//
// The provisioning order is constrained by the cyclic FK between users and
// grids (users.root_grid_id ↔ grids.owner_id). We resolve it by deferring
// foreign-key checks for the lifetime of the transaction.
func (s *Store) CreateUser(ctx context.Context, username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("%w: username empty", ErrInvalidArgument)
	}
	if password == "" {
		return nil, fmt.Errorf("%w: password empty", ErrInvalidArgument)
	}
	var p *auth.Params
	if s.hashParams != nil {
		p = &auth.Params{
			Time: s.hashParams.Time, Memory: s.hashParams.Memory,
			Threads: s.hashParams.Threads, KeyLen: s.hashParams.KeyLen,
			SaltLen: s.hashParams.SaltLen,
		}
	}
	hash, err := auth.HashPassword(password, p)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	now := s.now().Unix()

	var u User
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// defer FKs so we can insert user → grid → user-update without
		// hitting the cycle. SQLite checks deferred FKs at COMMIT.
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return fmt.Errorf("defer fks: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO groups (name) VALUES (?)`, username)
		if err != nil {
			return fmt.Errorf("insert group: %w", err)
		}
		groupID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		// Insert user with a placeholder root_grid_id we will overwrite
		// once the grid exists. Cannot use 0 because of NOT NULL — but
		// any positive integer works; the FK is checked at commit.
		res, err = tx.ExecContext(ctx,
			`INSERT INTO users (username, password_hash, primary_group_id, root_grid_id, created_at)
			 VALUES (?, ?, ?, 1, ?)`,
			username, hash, groupID, now)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("user %q already exists", username)
			}
			return fmt.Errorf("insert user: %w", err)
		}
		userID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_groups (user_id, group_id) VALUES (?, ?)`,
			userID, groupID); err != nil {
			return fmt.Errorf("insert user_group: %w", err)
		}

		// Empty root grid, mode 0o640 per spec §7.1.
		objID := s.newID()
		res, err = tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, owner_id, group_id, mode, refcount, created_at)
			 VALUES (?, ?, ?, ?, 1, ?)`,
			objID, userID, groupID, 0o640, now)
		if err != nil {
			return fmt.Errorf("insert grid: %w", err)
		}
		gridID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET root_grid_id = ? WHERE id = ?`, gridID, userID); err != nil {
			return fmt.Errorf("set root_grid_id: %w", err)
		}

		u = User{
			ID:             userID,
			Username:       username,
			PrimaryGroupID: groupID,
			RootGridID:     gridID,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUser fetches a user by id using the store's underlying handle. For
// in-transaction reads, use getUserQR with the tx.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return getUserQR(ctx, s.db, id)
}

// getUserQR loads a user using the given query interface. Both *sql.DB and
// *sql.Tx satisfy queryRower.
func getUserQR(ctx context.Context, q queryRower, id int64) (*User, error) {
	var u User
	err := q.QueryRowContext(ctx,
		`SELECT id, username, primary_group_id, root_grid_id FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.PrimaryGroupID, &u.RootGridID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// AuthenticateUser checks the password and returns the user on match. Returns
// ErrPermissionDenied for both unknown user and bad password (callers should
// not distinguish them in any user-facing message).
func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (*User, error) {
	var (
		u    User
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, primary_group_id, root_grid_id, password_hash
		 FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.PrimaryGroupID, &u.RootGridID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		// Run a dummy verify so the response time of unknown-user matches
		// known-user-with-wrong-password, blunting username enumeration via
		// timing.
		_ = auth.VerifyPassword(password, "$argon2id$v=19$m=8192,t=1,p=1$AAAA$AAAA")
		return nil, ErrPermissionDenied
	}
	if err != nil {
		return nil, err
	}
	if err := auth.VerifyPassword(password, hash); err != nil {
		return nil, ErrPermissionDenied
	}
	return &u, nil
}

// IsInGroup reports whether the user belongs to the given group.
func (s *Store) IsInGroup(ctx context.Context, userID, groupID int64) (bool, error) {
	return isInGroupTx(ctx, s.db, userID, groupID)
}

// userGroupChecker is the minimal interface that both *sql.DB and *sql.Tx
// satisfy; it lets helper functions run inside or outside a transaction.
type userGroupChecker interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func isInGroupTx(ctx context.Context, q userGroupChecker, userID, groupID int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM user_groups WHERE user_id = ? AND group_id = ?`, userID, groupID,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func isUniqueViolation(err error) bool {
	// modernc/sqlite returns errors with "UNIQUE constraint failed" in the
	// message. We do a string check rather than depending on the driver's
	// error type.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
