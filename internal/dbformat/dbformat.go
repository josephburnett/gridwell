// Package dbformat is the shared on-disk format contract for a Gridwell
// plugin database. Every plugin owns a SQLite file holding forever-data
// (placement, framing, the identity map deep links depend on), so every one
// of those files carries the same three guarantees, enforced by this ONE
// engine (extracted from internal/store, which delegates here):
//
//   - PRAGMA application_id marks whose file it is, so a foreign SQLite file
//     is refused instead of misread.
//   - PRAGMA user_version is the schema generation. A file stamped NEWER than
//     the binary is refused (an old binary must not misread a future schema);
//     an OLDER file is brought forward by the additive migration chain.
//   - Migrations are additive only — new columns (with defaults), new tables,
//     new indexes. Never drop, rename, retype, or repurpose; data written by
//     any released binary stays readable forever. Never delete a DB to absorb
//     a schema change. (See internal/store/CLAUDE.md for the full contract.)
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
//     there is nothing to migrate — sound only if the caller proves
//     (via an equivalence test) that fresh shape == v1 base + full chain.
//     An unstamped file that already carries data is treated the same: the
//     pre-versioning legacy shape is, by definition, the v1 base.
//   - Foreign DB (application_id set but not appID): refuse — not our file.
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
		// Both identity stamps in ONE transaction (header pragmas are
		// transactional): a crash between them would leave application_id set
		// with user_version 0, and the next Open would run the full migration
		// chain against the latest-shape tables — non-idempotent ADD COLUMNs
		// that fail forever. Atomic means the file is either unstamped
		// (stamped next Open) or fully stamped; no third state.
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
	// The stamp rides IN the migration transaction (header pragmas are
	// transactional): commit-then-stamp had a crash window that persisted the
	// DDL without the version recording it, after which every Open re-ran the
	// non-idempotent chain ("duplicate column name") and the file — forever-
	// data by contract — was unopenable. Atomic means the file is either at
	// the old version with none of the chain applied, or at target with all
	// of it; no third state exists to crash into.
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

// setPragmaInt writes an integer-valued PRAGMA. PRAGMA statements can't bind
// parameters, so the value is formatted in; callers pass trusted constants.
func setPragmaInt(ctx context.Context, db *sql.DB, name string, v int64) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}

// setPragmaIntTx is setPragmaInt inside a transaction — header pragmas are
// transactional in SQLite, which is what makes the migrate-and-stamp and the
// fresh-identity stamps atomic.
func setPragmaIntTx(ctx context.Context, tx *sql.Tx, name string, v int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
