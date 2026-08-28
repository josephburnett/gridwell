package cli

// ── DELETE AFTER 2026-09-16 with kindmigrate.go ─────────────────────────

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
)

// TestMigrateRenamedKinds: a pre-rename home (kind localdb/ssh — and, since
// 2026-08-27, local — in both
// server.yaml and the DB stamps) comes forward in one boot — config and
// stamps agree on the new names, ids untouched — and a second run is a
// no-op.
func TestMigrateRenamedKinds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	// Mint two plugins the NEW way, then regress them to the OLD names —
	// exactly the state an un-upgraded home is in.
	if rc := RunInit([]string{"--kind", "home", "--name", "home"}); rc != 0 {
		t.Fatal("init local")
	}
	if rc := RunInit([]string{"--kind", "remote", "--name", "rtb"}); rc != 0 {
		t.Fatal("init remote")
	}
	cfgPath := filepath.Join(home, "server.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(raw), "kind: home\n", "kind: localdb\n", 1)
	old = strings.Replace(old, "kind: remote\n", "kind: ssh\n", 1)
	if err := os.WriteFile(cfgPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"localdb", "ssh"} {
		if err := pluginmeta.UpdateKind(config.DBFile(home, cfg.Plugins[i].ID), want); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrateRenamedKinds(home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	after, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"home", "remote"} {
		p := after.Plugins[i]
		if p.Kind != want {
			t.Errorf("config kind[%d] = %q, want %q", i, p.Kind, want)
		}
		if p.ID != cfg.Plugins[i].ID {
			t.Errorf("id changed during migration: %q → %q", cfg.Plugins[i].ID, p.ID)
		}
		m, err := pluginmeta.Verify(config.DBFile(home, p.ID), p.ID, want)
		if err != nil {
			t.Fatalf("stamp verify after migrate: %v", err)
		}
		if m.Kind != want {
			t.Errorf("stamp kind = %q, want %q", m.Kind, want)
		}
	}

	// Idempotent: nothing left to match.
	if err := migrateRenamedKinds(home, cfgPath); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// A home stamped "local" (2026-08-16 → 2026-08-27) renames to "home" the
// same one-shot way: config and DB stamp together, ids untouched.
func TestMigrateLocalToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)
	if rc := RunInit([]string{"--kind", "home", "--name", "home"}); rc != 0 {
		t.Fatal("init home")
	}
	cfgPath := filepath.Join(home, "server.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(strings.Replace(string(raw), "kind: home\n", "kind: local\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Plugins[0].ID
	if err := pluginmeta.UpdateKind(config.DBFile(home, id), "local"); err != nil {
		t.Fatal(err)
	}
	if err := migrateRenamedKinds(home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	after, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Plugins[0].Kind != "home" || after.Plugins[0].ID != id {
		t.Fatalf("after = %+v", after.Plugins[0])
	}
	if _, err := pluginmeta.Verify(config.DBFile(home, id), id, "home"); err != nil {
		t.Fatalf("DB stamp not renamed: %v", err)
	}
}
