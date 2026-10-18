package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/node"
)

// backupTestDir builds a real node directory the way serve does and returns its
// path and its root grid id.
func backupTestDir(t *testing.T) (dir string, rootID string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("GRIDWELL_HOME", dir)
	cfgPath := filepath.Join(dir, "server.yaml")
	// A plugin that has never served has no rows; a backup must not
	// abort on it.
	if err := os.WriteFile(cfgPath, []byte("plugins:\n  - kind: gitlab\n    label: todos\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := node.BuildConfig(dir, cfgPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(config.DBFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	rootID, err = st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return dir, rootID
}

// The backup mirrors the node directory's layout and the copied database opens through
// the full store contract with the same identity, so a restored node is
// byte-meaningful, not just byte-shaped.
func TestBackupSnapshotsHome(t *testing.T) {
	dir, rootID := backupTestDir(t)
	dest := filepath.Join(t.TempDir(), "snap")

	if code := RunBackup([]string{dest}); code != 0 {
		t.Fatalf("backup exit = %d", code)
	}

	if _, err := os.Stat(filepath.Join(dest, "server.yaml")); err != nil {
		t.Fatalf("backup lacks server.yaml: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dest, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	copied := config.DBFile(dest)
	st, err := store.Open(copied)
	if err != nil {
		t.Fatalf("backed-up DB failed the open contract: %v", err)
	}
	defer st.Close()
	gotRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != rootID {
		t.Errorf("backed-up root grid = %s, want %s (identity must survive)", gotRoot, rootID)
	}
	for _, f := range config.DurableFiles(dest) {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("backup lacks %s: %v (a restored node would rotate its password / lose its viewport)", filepath.Base(f), err)
		}
	}
	if len(cfg.Plugins) != 1 {
		t.Errorf("backup config lists %d entries, want the never-served plugin kept (it must not abort the backup)", len(cfg.Plugins))
	}
	_ = dir
}

// Overwriting a previous snapshot must be the user's explicit call.
func TestBackupRefusesOverwrite(t *testing.T) {
	_, _ = backupTestDir(t)
	dest := filepath.Join(t.TempDir(), "snap")
	if code := RunBackup([]string{dest}); code != 0 {
		t.Fatalf("first backup exit = %d", code)
	}
	if code := RunBackup([]string{dest}); code == 0 {
		t.Fatal("second backup into the same dest must refuse, got exit 0")
	}
}

// No args is a usage error, not a panic or a default dest.
func TestBackupUsage(t *testing.T) {
	if code := RunBackup(nil); code != 2 {
		t.Errorf("no-arg exit = %d, want 2", code)
	}
}
