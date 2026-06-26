// Package pluginmeta persists a plugin instance's durable identity in its own
// SQLite DB. CLAUDE.md: a plugin's UUID is "assigned once and stored
// permanently (in the plugin's own DB and referenced in server.yaml)". This is
// the in-DB half of that contract — the DB becomes self-describing, so a lost
// or rewritten server.yaml can be reconciled against the authoritative source.
package pluginmeta

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrUUIDMismatch is returned when a DB already carries a different UUID than
// the one configured — the durable identity must never silently change.
var ErrUUIDMismatch = errors.New("pluginmeta: configured uuid does not match the one stored in the plugin DB")

// Ensure records uuid as the DB's permanent identity on first run and verifies
// it on every subsequent run. The table is storage-only (not a wire record), so
// it is invisible to the proto/DDL drift lint, which checks only grids/tiles.
//
//   - empty stored, non-empty uuid  → store it (first run)
//   - stored == uuid                → ok
//   - stored != uuid (both set)     → ErrUUIDMismatch
//   - empty uuid                     → return whatever is stored (read-only)
func Ensure(dbPath, uuid string) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("pluginmeta open %s: %w", dbPath, err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _gridwell_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		return "", fmt.Errorf("pluginmeta schema: %w", err)
	}

	var stored string
	switch err := db.QueryRow(`SELECT v FROM _gridwell_meta WHERE k = 'uuid'`).Scan(&stored); {
	case errors.Is(err, sql.ErrNoRows):
		stored = ""
	case err != nil:
		return "", fmt.Errorf("pluginmeta read uuid: %w", err)
	}

	if stored == "" {
		if uuid == "" {
			return "", nil
		}
		if _, err := db.Exec(`INSERT INTO _gridwell_meta (k, v) VALUES ('uuid', ?)`, uuid); err != nil {
			return "", fmt.Errorf("pluginmeta write uuid: %w", err)
		}
		return uuid, nil
	}
	if uuid != "" && uuid != stored {
		return "", fmt.Errorf("%w (stored %q, configured %q)", ErrUUIDMismatch, stored, uuid)
	}
	return stored, nil
}
