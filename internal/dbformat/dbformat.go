// Package dbformat is the shared on-disk format contract for a Gridwell
// SQLite file. Those files hold forever-data — placement, framing, the
// identity map deep links depend on — so each carries the same three
// guarantees, enforced by this one engine, which internal/local/store
// delegates to:
//
//   - PRAGMA application_id marks whose file it is, so a foreign SQLite file
//     is refused instead of misread.
//   - PRAGMA user_version is the schema generation. A file stamped newer than
//     the binary is refused, because an old binary must not misread a future
//     schema; an older file is brought forward by the migration chain.
//   - Migrations are additive by default: new columns with defaults, new
//     tables, new indexes. Data written by any released binary stays
//     readable. Never delete a DB to absorb a schema change. The full
//     contract is internal/local/store/CLAUDE.md.
package dbformat

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration is one additive, non-destructive step that brings a DB from
// version To-1 up to version To.
type Migration struct {
	To  int
	Run func(ctx context.Context, tx *sql.Tx) error
}

// EnsureVersion enforces the format contract at Open time, bringing the DB up
// to target using the ordered migration chain:
//
//   - Fresh DB (application_id and user_version both 0): stamp appID and
//     target. The caller's Open already materialized the latest shape, so
//     there is nothing to migrate. That is sound only if the caller proves,
//     with an equivalence test, that the fresh shape equals the v1 base plus
//     the full chain. An unstamped file that already carries data is treated
//     the same: an unversioned shape is by definition the v1 base.
//   - Foreign DB (application_id set but not appID): refuse; not our file.
//   - Newer DB (user_version > target): refuse.
//   - Older DB: run each pending migration in order in one transaction, then
//     stamp target.
func EnsureVersion(ctx context.Context, db *sql.DB, appID int64, target int, migs []Migration) error {
	gotApp, err := readPragmaInt(ctx, db, "application_id")
	if err != nil {
		return err
	}
	userVer, err := readPragmaInt(ctx, db, "user_version")
	if err != nil {
		return err
	}

	if gotApp == 0 && userVer == 0 {
		// Both identity stamps go in one transaction; header pragmas are
		// transactional. A crash between them would leave application_id set
		// with user_version 0, and the next Open would run the full migration
		// chain against the latest-shape tables, whose non-idempotent ADD
		// COLUMNs then fail forever. Atomic means the file is either
		// unstamped, and stamped next Open, or fully stamped. There is no
		// third state.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		if err := setPragmaIntTx(ctx, tx, "application_id", appID); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := setPragmaIntTx(ctx, tx, "user_version", int64(target)); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	if gotApp != appID {
		return fmt.Errorf("not a Gridwell database of this kind: application_id %#x, want %#x", gotApp, appID)
	}
	if userVer > int64(target) {
		return fmt.Errorf("stored schema version %d is newer than this binary's %d", userVer, target)
	}
	if userVer == int64(target) {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	for _, m := range migs {
		if m.To <= int(userVer) || m.To > target {
			continue
		}
		if err := m.Run(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration to v%d: %w", m.To, err)
		}
	}
	// The stamp rides inside the migration transaction; header pragmas are
	// transactional. Stamping after the commit would leave a crash window
	// that persists the DDL without the version recording it, after which
	// every Open re-runs the non-idempotent chain and fails on "duplicate
	// column name", leaving the file unopenable. Atomic means the file is
	// either at the old version with none of the chain applied, or at target
	// with all of it. There is no third state to crash into.
	if err := setPragmaIntTx(ctx, tx, "user_version", int64(target)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readPragmaInt reads an integer-valued PRAGMA. The name is a trusted literal,
// never user input.
func readPragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var v int64
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setPragmaIntTx is setPragmaInt inside a transaction — header pragmas are
// transactional in SQLite, which is what makes the migrate-and-stamp and the
// fresh-identity stamps atomic.
func setPragmaIntTx(ctx context.Context, tx *sql.Tx, name string, v int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
