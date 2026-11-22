// Package dbformat is the on-disk format contract for a Gridwell SQLite file.
// PRAGMA application_id marks whose file it is, so a foreign SQLite file is
// refused instead of misread. PRAGMA user_version is the schema generation: a
// stamp newer than the binary is refused, an older one brought forward by the
// chain. The contract is internal/local/store/CLAUDE.md.
package dbformat

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration is one additive step that brings a DB from version To-1 up to To.
type Migration struct {
	To  int
	Run func(ctx context.Context, tx *sql.Tx) error
}

// EnsureVersion runs the pending migrations in one transaction, refusing a
// foreign application_id or a user_version past target. A fresh DB, both
// pragmas 0, is stamped straight at target because the caller's Open already
// materialized the latest shape; that holds only while the caller proves by
// equivalence test that the fresh shape equals the v1 base plus the full chain.
func EnsureVersion(ctx context.Context, db *sql.DB, appID int64, target int, migs []Migration) error {
	gotApp, err := readPragmaInt(ctx, db, "application_id")
	if err != nil {
		return err
	}
	userVer, err := readPragmaInt(ctx, db, "user_version")
	if err != nil {
		return err
	}

	fresh := gotApp == 0 && userVer == 0
	if !fresh && gotApp != appID {
		return fmt.Errorf("not a Gridwell database of this kind: application_id %#x, want %#x", gotApp, appID)
	}
	if userVer > int64(target) {
		return fmt.Errorf("stored schema version %d is newer than this binary's %d", userVer, target)
	}
	if !fresh && userVer == int64(target) {
		return nil
	}

	// Every stamp rides inside one transaction, header pragmas being
	// transactional. A fresh file stamped application_id without user_version,
	// or a migrated one with its DDL and no version, would re-run the chain on
	// the next Open, fail on "duplicate column name" and stay unopenable.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if fresh {
		if err := setPragmaIntTx(ctx, tx, "application_id", appID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, m := range migs {
		if fresh || m.To <= int(userVer) || m.To > target {
			continue
		}
		if err := m.Run(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration to v%d: %w", m.To, err)
		}
	}
	if err := setPragmaIntTx(ctx, tx, "user_version", int64(target)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readPragmaInt's name is a trusted literal.
func readPragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var v int64
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setPragmaIntTx relies on header pragmas being transactional in SQLite, which
// is what makes the stamps atomic.
func setPragmaIntTx(ctx context.Context, tx *sql.Tx, name string, v int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
