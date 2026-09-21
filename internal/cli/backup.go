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

// RunBackup snapshots a whole Gridwell home — the database plus the loose
// durable files config.DurableFiles names — into a destination directory.
//
//	gridwell backup <dest>
//
// VACUUM INTO is consistent while a live server holds the file under WAL, so
// no downtime is needed, and the layout mirrors the home, so restore is a
// plain copy over the home or a GRIDWELL_HOME pointed at the backup.
func RunBackup(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: gridwell backup <dest-dir>")
		return 2
	}
	dest := args[0]

	home, err := config.Home()
	if err != nil {
		return die("backup", err)
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return die("backup", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "backup: no config at %s — nothing to back up\n", cfgPath)
			return 1
		}
		return die("backup", err)
	}

	if err := backupHome(home, cfgPath, cfg, dest); err != nil {
		return die("backup", err)
	}
	fmt.Printf("gridwell: backed up gridwell.db + server.yaml to %s\n", dest)
	return 0
}

// backupHome writes the snapshot, split from RunBackup so the procedure is
// unit-testable. A destination that already holds a backup is refused:
// overwriting one is the user's explicit call.
func backupHome(home, cfgPath string, cfg *config.ServerConfig, dest string) error {
	if _, err := os.Stat(filepath.Join(dest, "server.yaml")); err == nil {
		return fmt.Errorf("destination %s already holds a backup (server.yaml exists); choose a fresh directory", dest)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}

	// Snapshot the database first and write server.yaml last, so a backup
	// whose server.yaml exists always has its database.
	snap := func(src, dst string, required bool) error {
		if _, err := os.Stat(src); err != nil {
			if !required && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("no database at %s", src)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return vacuumInto(src, dst)
	}
	if cfg.ID == "" {
		return fmt.Errorf("%s names no id — the home has never served; nothing to back up", cfgPath)
	}
	// cache.db is disposable and stays out.
	if err := snap(config.DBFile(home), config.DBFile(dest), true); err != nil {
		return err
	}

	// server.yaml last: it is the completion marker.
	files := config.DurableFiles(home)
	for i := len(files) - 1; i >= 0; i-- {
		src := files[i]
		if filepath.Base(src) == "server.yaml" {
			src = cfgPath
		}
		data, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, filepath.Base(files[i])), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// vacuumInto writes a consistent compacted snapshot of src to dst. It is
// safe against a concurrently-writing server, because VACUUM INTO is a
// point-in-time transaction.
func vacuumInto(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// VACUUM INTO refuses to overwrite, and the fresh-directory check above
	// makes a pre-existing dst a real error.
	if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dst, err)
	}
	return nil
}
