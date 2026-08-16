// Package pluginmeta persists a plugin instance's durable identity in its own
// SQLite DB. CLAUDE.md: a plugin's id is "assigned once and stored permanently
// (in the plugin's own DB and referenced in server.yaml)". This is the in-DB
// half of that contract — every Gridwell DB is self-describing, so the server
// can verify on each start that the DB it opens is the one the config names,
// and a lost or rewritten server.yaml can be reconciled against the DB.
//
// Three keys live in the storage-only `_gridwell_meta` table (invisible to the
// proto/DDL drift lint, which checks only grids/tiles):
//   - gridwell : a marker identifying the file as a Gridwell DB
//   - id       : the plugin's durable routing id (== config id)
//   - kind     : the plugin kind (== config kind); selects the schema
//
// id and kind are immutable: once written they are strictly verified, both
// directions, on every subsequent open. The display name is NOT stored here —
// it is config-only and freely editable.
package pluginmeta

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	// The sqlite driver: this package calls sql.Open("sqlite", ...) and so
	// owns the driver registration. It must NOT rely on another linked
	// package (internal/store) happening to import it — gridwell-ssh links
	// pluginmeta without store, and every spawn died with `unknown driver`
	// while tests (which had their own masking import) stayed green.
	_ "modernc.org/sqlite"
)

// Sentinel errors. Callers should use errors.Is to test for them.
var (
	// ErrIDMismatch is returned when a DB already carries a different id than
	// the one configured — the durable identity must never silently change.
	ErrIDMismatch = errors.New("pluginmeta: configured id does not match the one stored in the plugin DB")
	// ErrKindMismatch is returned when a DB already carries a different kind —
	// the kind selects the schema, so opening a DB as the wrong kind is refused.
	ErrKindMismatch = errors.New("pluginmeta: configured kind does not match the one stored in the plugin DB")
	// ErrNotInitialized is returned by Verify when the DB is missing or carries
	// no stored identity. Verify never creates identity — only `gridwell init`
	// (Create) does — so an entry whose DB was never initialized fails loudly
	// instead of silently materializing a fresh, empty store.
	ErrNotInitialized = errors.New("pluginmeta: plugin DB is missing or uninitialized")
)

const (
	keyMarker = "gridwell"
	keyID     = "id"
	keyKind   = "kind"
	// keyLegacyUUID is the pre-id-and-kind key. A DB created before this change
	// stored its id under "uuid"; we read it as a fallback so an existing DB's
	// identity is preserved (and upgraded to "id"), never re-minted.
	keyLegacyUUID = "uuid"
	// markerValue is the marker payload. Its value is unimportant — presence is
	// what identifies the file as a Gridwell DB.
	markerValue = "1"
)

// Meta is the durable identity recorded in a plugin's DB.
type Meta struct {
	ID   string
	Kind string
}

// Create records (id, kind) as the DB's permanent identity, creating the DB
// file and the marker on first run. This is the ONLY place a plugin DB and its
// identity are born — `gridwell init` calls it. It is idempotent for the same
// id but refuses to overwrite a different stored id (a safety net; init always
// mints a fresh id, so the path is empty).
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

// Verify strictly checks an existing DB's identity against (id, kind). It NEVER
// creates a DB or first-run identity: a missing file or a DB with no stored id
// is ErrNotInitialized (the entry's DB was never `gridwell init`-ed). This is
// what makes a changed config id fail loudly — serve points at <home>/db/<id>,
// which for a new id does not exist — instead of silently spawning a fresh store.
//
//   - id == "" && kind == ""  → read-only probe: return whatever is stored
//   - file missing / no id     → ErrNotInitialized
//   - stored id differs        → ErrIDMismatch
//   - stored kind differs       → ErrKindMismatch
//   - match (legacy kind/id keys upgraded in place) → ok
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

	// Read-only probe: report what is stored without asserting anything.
	if id == "" && kind == "" {
		return stored, nil
	}
	if stored.ID == "" {
		return Meta{}, fmt.Errorf("%w (%s)", ErrNotInitialized, dbPath)
	}

	// A legacy DB may carry an id (under the old uuid key) but no kind yet — an
	// empty stored kind is "not recorded", so it is adopted, not a mismatch.
	if stored.ID != id {
		return Meta{}, fmt.Errorf("%w (stored %q, configured %q)", ErrIDMismatch, stored.ID, id)
	}
	if stored.Kind != "" && stored.Kind != kind {
		return Meta{}, fmt.Errorf("%w (stored %q, configured %q)", ErrKindMismatch, stored.Kind, kind)
	}
	// Upgrade a legacy DB in place (the id may have been readable only via the
	// old uuid key, and the kind may not have been recorded at all).
	if err := writeIdentity(db, id, kind); err != nil {
		return Meta{}, err
	}
	return Meta{ID: id, Kind: kind}, nil
}

// open opens the DB and ensures the storage-only identity table exists.
func open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("pluginmeta open %s: %w", dbPath, err)
	}
	// One connection — the single-writer discipline every SQLite handle in
	// this repo pins, so a pooled second connection can never race the file
	// lock into an instant SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _gridwell_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("pluginmeta schema: %w", err)
	}
	return db, nil
}

// writeIdentity upserts the marker + id + kind.
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
			return fmt.Errorf("pluginmeta write %s: %w", kv.k, err)
		}
	}
	return nil
}

// read returns the stored identity, falling back to the legacy uuid key when
// the canonical id key is absent (a DB created before id+kind were split out).
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
		return "", fmt.Errorf("pluginmeta read %s: %w", k, err)
	}
	return v, nil
}

// UpdateKind rewrites the stamped kind in a plugin DB's identity metadata
// — the host-driven half of a kind RENAME (the id never changes). Generic
// on purpose: which names map to which lives with the caller's migration,
// never here.
func UpdateKind(dbPath, kind string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE _gridwell_meta SET v = ? WHERE k = 'kind'`, kind)
	return err
}
