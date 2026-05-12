package store

import (
	"context"
	"database/sql"
)

// Permission bits in the lower 9 bits of mode (rwxrwxrwx). Only r and w are
// used in v1; x is reserved.
const (
	PermOwnerRead  = 0o400
	PermOwnerWrite = 0o200
	PermGroupRead  = 0o040
	PermGroupWrite = 0o020
	PermOtherRead  = 0o004
	PermOtherWrite = 0o002
)

// Effective returns the rw permission bits for userID against an object owned
// by ownerID/groupID with the given mode. Owner bits if user matches owner;
// else group bits if user is in group; else other bits. The returned value
// is the relevant bits shifted into the "other" position so callers can do
// uniform checks like (perm & PermOtherRead) != 0.
//
// The owner-precedes-group rule matches POSIX; "your file is locked even
// from yourself" if you set mode 0o077 is the same surprise as on unix.
func Effective(userID, ownerID, groupID int64, mode int32, inGroup bool) (read, write bool) {
	if userID == ownerID {
		return mode&PermOwnerRead != 0, mode&PermOwnerWrite != 0
	}
	if inGroup {
		return mode&PermGroupRead != 0, mode&PermGroupWrite != 0
	}
	return mode&PermOtherRead != 0, mode&PermOtherWrite != 0
}

// permFor reports (read, write) for userID on a row with the given owner /
// group / mode. Wraps Effective with a database lookup of group membership.
func (s *Store) permFor(ctx context.Context, q userGroupChecker, userID, ownerID, groupID int64, mode int32) (bool, bool, error) {
	inGroup, err := isInGroupTx(ctx, q, userID, groupID)
	if err != nil {
		return false, false, err
	}
	r, w := Effective(userID, ownerID, groupID, mode, inGroup)
	return r, w, nil
}

// permForGrid loads the named grid and returns (read, write, ownerID,
// groupID, mode) for userID against it. Used by RPC handlers.
func (s *Store) permForGrid(ctx context.Context, q queryRower, userID, gridID int64) (read, write bool, err error) {
	var owner, group int64
	var mode int32
	err = q.QueryRowContext(ctx,
		`SELECT owner_id, group_id, mode FROM grids WHERE id = ?`, gridID,
	).Scan(&owner, &group, &mode)
	if err == sql.ErrNoRows {
		return false, false, ErrNotFound
	}
	if err != nil {
		return false, false, err
	}
	r, w, err := s.permFor(ctx, q, userID, owner, group, mode)
	return r, w, err
}

// permForTile loads the named tile and returns (read, write) for userID
// against it.
func (s *Store) permForTile(ctx context.Context, q queryRower, userID, tileID int64) (read, write bool, err error) {
	var owner, group int64
	var mode int32
	err = q.QueryRowContext(ctx,
		`SELECT owner_id, group_id, mode FROM tiles WHERE id = ?`, tileID,
	).Scan(&owner, &group, &mode)
	if err == sql.ErrNoRows {
		return false, false, ErrNotFound
	}
	if err != nil {
		return false, false, err
	}
	r, w, err := s.permFor(ctx, q, userID, owner, group, mode)
	return r, w, err
}

// queryRower is satisfied by both *sql.DB and *sql.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
