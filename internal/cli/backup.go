package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/josephburnett/gridwell/internal/config"
	_ "modernc.org/sqlite"
)

// RunBackup snapshots a whole Gridwell home — every plugin DB plus
// server.yaml — into a destination directory, from which a home can be
// reconstituted by plain copy. For a system whose thesis is permanence, this
// is the cheap insurance against the file itself being lost (disk death,
// accidental rm, a bad home migration).
//
//	gridwell backup <dest>
//
// Each plugin DB is copied with SQLite's VACUUM INTO, which produces a
// consistent, compacted snapshot even while a live server holds the file
// under WAL — no downtime needed. The layout mirrors the home
// (<dest>/server.yaml, <dest>/db/<id>/store.db), so restore is deliberately
// dumb: stop the server, copy the backup's contents over the home (or point
// GRIDWELL_HOME at it), start. The per-plugin DB is the unit of portability;
// this command just makes that property usable. Every DB's own open-time
// contract (application_id / user_version — internal/dbformat) re-verifies
// integrity on first use of a restored copy.
func RunBackup(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: gridwell backup <dest-dir>")
		return 2
	}
	dest := args[0]

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "backup: no config at %s — nothing to back up\n", cfgPath)
		} else {
			fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		}
		return 1
	}

	if err := backupHome(home, cfgPath, cfg, dest); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	fmt.Printf("gridwell: backed up %d plugin DB(s) + server.yaml to %s\n", len(cfg.Plugins), dest)
	return 0
}

// backupHome writes the snapshot. Split from RunBackup so the whole procedure
// is unit-testable against a synthetic home. The destination must not already
// contain a Gridwell backup (no silent overwrite of a previous snapshot —
// deleting or overwriting an existing backup is the user's explicit call).
func backupHome(home, cfgPath string, cfg *config.ServerConfig, dest string) error {
	if _, err := os.Stat(filepath.Join(dest, "server.yaml")); err == nil {
		return fmt.Errorf("destination %s already holds a backup (server.yaml exists); choose a fresh directory", dest)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	// Snapshot every plugin DB first; write server.yaml last, so a completed
	// backup (one whose server.yaml exists) always has all its DBs.
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		src := config.DBFile(home, pc.ID)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("plugin %q (%s): no database at %s", pc.Name, pc.ID, src)
		}
		dstDir := config.DBDir(dest, pc.ID)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		if err := vacuumInto(src, filepath.Join(dstDir, "store.db")); err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
		}
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "server.yaml"), data, 0o600)
}

// vacuumInto opens src read-only and writes a consistent, compacted snapshot
// to dst via SQLite's VACUUM INTO — safe against a concurrently-writing
// server under WAL (the snapshot is a point-in-time transaction).
func vacuumInto(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// VACUUM INTO refuses to overwrite; the fresh-directory check above makes
	// a pre-existing dst a real error worth surfacing verbatim.
	if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dst, err)
	}
	return nil
}
