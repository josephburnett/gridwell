package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// backupTestHome builds a real home via RunInit (a localdb plugin with a DB)
// and returns (home, the plugin's root grid id).
func backupTestHome(t *testing.T) (home string, rootID string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)
	if code := RunInit([]string{"--kind", "home", "--name", "home"}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	// A plugin entry that has never served: no memory DB yet (2026-08-27:
	// backup aborted on it). And the loose durable files a served home has.
	if code := RunInit([]string{"--kind", "gitlab", "--name", "todos"}); code != 0 {
		t.Fatalf("init gitlab exit = %d", code)
	}
	for _, f := range []string{config.PasswordFile(home), config.NodeViewFile(home)} {
		if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(config.DBFile(home, cfg.Plugins[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	rootID, err = st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return home, rootID
}

// TestBackupSnapshotsHome: the backup mirrors the home layout, and the copied
// DB opens through the full store contract (application_id / user_version /
// schema check) with the SAME identity — a restored home is byte-meaningful,
// not just byte-shaped.
func TestBackupSnapshotsHome(t *testing.T) {
	home, rootID := backupTestHome(t)
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
	copied := config.DBFile(dest, cfg.Plugins[0].ID)
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
			t.Errorf("backup lacks %s: %v (a restored home would rotate its password / lose its viewport)", filepath.Base(f), err)
		}
	}
	if len(cfg.Plugins) != 2 {
		t.Errorf("backup config lists %d entries, want both (the plugin without a DB must not abort)", len(cfg.Plugins))
	}
	_ = home
}

// TestBackupRefusesOverwrite: a destination already holding a backup is
// refused — overwriting a previous snapshot must be the user's explicit call.
func TestBackupRefusesOverwrite(t *testing.T) {
	_, _ = backupTestHome(t)
	dest := filepath.Join(t.TempDir(), "snap")
	if code := RunBackup([]string{dest}); code != 0 {
		t.Fatalf("first backup exit = %d", code)
	}
	if code := RunBackup([]string{dest}); code == 0 {
		t.Fatal("second backup into the same dest must refuse, got exit 0")
	}
}

// TestBackupUsage: no args is a usage error, not a panic or a default dest.
func TestBackupUsage(t *testing.T) {
	if code := RunBackup(nil); code != 2 {
		t.Errorf("no-arg exit = %d, want 2", code)
	}
}
