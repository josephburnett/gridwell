// Package dbmeta records a Gridwell database's durable identity inside the
// database itself, so the server checks on each start that the DB it opens is
// the one server.yaml names. Three keys live in the storage-only
// _gridwell_meta table: gridwell, a marker; id, the durable routing id; and
// kind, which schema the DB holds. id and kind are immutable, strictly
// verified in both directions on every open; the display name is config-only.
package dbmeta

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	// This package calls sql.Open("sqlite", ...), so it owns the driver
	// registration. Relying on another package to import it would fail at run
	// time in a binary that links dbmeta without the store.
	_ "modernc.org/sqlite"
)

// Sentinel errors, tested with errors.Is.
var (
	// ErrIDMismatch: the durable identity must never silently change.
	ErrIDMismatch = errors.New("dbmeta: configured id does not match the one stored in the DB")
	// ErrKindMismatch: kind selects the schema, so opening a DB as the wrong
	// kind is refused.
	ErrKindMismatch = errors.New("dbmeta: configured kind does not match the one stored in the DB")
	// ErrNotInitialized: only Create makes an identity, so a DB that was
	// never created fails loudly instead of materializing an empty store.
	ErrNotInitialized = errors.New("dbmeta: the DB is missing or uninitialized")
)

const (
	keyMarker = "gridwell"
	keyID     = "id"
	keyKind   = "kind"
	// keyLegacyUUID is the pre-id-and-kind key, read through a fallback so an
	// older DB's identity is upgraded rather than re-minted.
	keyLegacyUUID = "uuid"
	// markerValue's presence, not its value, identifies a Gridwell DB.
	markerValue = "1"
)

// Meta is the durable identity recorded in a DB.
type Meta struct {
	ID   string
	Kind string
}

// Create is the only place a DB and its identity are born. It is idempotent
// for the same id and refuses to overwrite a different stored one.
func Create(dbPath, id, kind string) error {
	db, err := open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	stored, err := read(db)
	if err != nil {
		return err
	}
	if stored.ID != "" && stored.ID != id {
		return fmt.Errorf("%w (stored %q, configured %q)", ErrIDMismatch, stored.ID, id)
	}
	return writeIdentity(db, id, kind)
}

// Verify never creates a DB or a first-run identity, which is what makes a
// changed config id fail loudly instead of opening a fresh store. Empty id and
// kind are a read-only probe; a missing file or id is ErrNotInitialized.
func Verify(dbPath, id, kind string) (Meta, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return Meta{}, fmt.Errorf("%w (%s)", ErrNotInitialized, dbPath)
	}
	db, err := open(dbPath)
	if err != nil {
		return Meta{}, err
	}
	defer db.Close()
	stored, err := read(db)
	if err != nil {
		return Meta{}, err
	}

	if id == "" && kind == "" {
		return stored, nil
	}
	if stored.ID == "" {
		return Meta{}, fmt.Errorf("%w (%s)", ErrNotInitialized, dbPath)
	}

	// An empty stored kind was never recorded, so it is adopted rather than
	// refused.
	if stored.ID != id {
		return Meta{}, fmt.Errorf("%w (stored %q, configured %q)", ErrIDMismatch, stored.ID, id)
	}
	if stored.Kind != "" && stored.Kind != kind {
		return Meta{}, fmt.Errorf("%w (stored %q, configured %q)", ErrKindMismatch, stored.Kind, kind)
	}
	// Upgrade an older DB in place.
	if err := writeIdentity(db, id, kind); err != nil {
		return Meta{}, err
	}
	return Meta{ID: id, Kind: kind}, nil
}

func open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("dbmeta open %s: %w", dbPath, err)
	}
	// One connection is the single-writer discipline every SQLite handle here
	// pins, so a pooled second cannot race the file lock into SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _gridwell_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbmeta schema: %w", err)
	}
	return db, nil
}

func writeIdentity(db *sql.DB, id, kind string) error {
	for _, kv := range []struct{ k, v string }{
		{keyMarker, markerValue},
		{keyID, id},
		{keyKind, kind},
	} {
		if _, err := db.Exec(
			`INSERT INTO _gridwell_meta (k, v) VALUES (?, ?)
			 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
			kv.k, kv.v); err != nil {
			return fmt.Errorf("dbmeta write %s: %w", kv.k, err)
		}
	}
	return nil
}

// read falls back to the uuid key when the canonical id key is absent.
func read(db *sql.DB) (Meta, error) {
	id, err := readKey(db, keyID)
	if err != nil {
		return Meta{}, err
	}
	if id == "" {
		if id, err = readKey(db, keyLegacyUUID); err != nil {
			return Meta{}, err
		}
	}
	kind, err := readKey(db, keyKind)
	if err != nil {
		return Meta{}, err
	}
	return Meta{ID: id, Kind: kind}, nil
}

func readKey(db *sql.DB, k string) (string, error) {
	var v string
	switch err := db.QueryRow(`SELECT v FROM _gridwell_meta WHERE k = ?`, k).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("dbmeta read %s: %w", k, err)
	}
	return v, nil
}
