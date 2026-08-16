package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
)

// TestRunInitCreatesPlugin verifies the one coordinated step: a DB dir + DB with
// id+kind metadata under the derived path, plus a matching server.yaml entry
// whose id equals the one written into the DB.
func TestRunInitCreatesPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	if rc := RunInit([]string{"--kind", "localdb", "--name", "home"}); rc != 0 {
		t.Fatalf("init returned %d", rc)
	}

	cfg, err := config.Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("plugins: got %d, want 1", len(cfg.Plugins))
	}
	p := cfg.Plugins[0]
	if p.Name != "home" || p.Kind != "localdb" || p.ID == "" {
		t.Fatalf("plugin entry: %+v", p)
	}

	// The DB exists at the derived path and carries the same id + kind.
	dbFile := config.DBFile(home, p.ID)
	if st, err := os.Stat(dbFile); err != nil || st.Size() == 0 {
		t.Fatalf("db missing/empty at %s: %v", dbFile, err)
	}
	m, err := pluginmeta.Verify(dbFile, "", "")
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if m.ID != p.ID || m.Kind != "localdb" {
		t.Errorf("DB metadata %+v does not match config id %q", m, p.ID)
	}
}

func TestRunInitRejectsDuplicateName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	if rc := RunInit([]string{"--kind", "localdb", "--name", "home"}); rc != 0 {
		t.Fatal("first init failed")
	}
	if rc := RunInit([]string{"--kind", "localdb", "--name", "home"}); rc == 0 {
		t.Error("duplicate name should be rejected")
	}
}

func TestRunInitRequiresKindAndName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	if rc := RunInit([]string{"--kind", "localdb"}); rc == 0 {
		t.Error("missing --name should fail")
	}
	if rc := RunInit([]string{"--name", "home"}); rc == 0 {
		t.Error("missing --kind should fail")
	}
}

// TestRunInitSSH: the transport kind registers with no config —
// connections are DATA (#199/#251), added from inside Gridwell. (The
// init-time refusal of the retired config keys was deleted with the
// pre-#251 migration bridge, 2026-08-15: the host no longer knows any
// plugin's config vocabulary — a plugin's own params door validates.)
func TestRunInitSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	rc := RunInit([]string{"--kind", "ssh", "--name", "remote"})
	if rc != 0 {
		t.Fatalf("plain init ssh returned %d", rc)
	}
	cfg, err := config.Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := cfg.Plugins[0]
	if p.Kind != "ssh" || len(p.Config) != 0 {
		t.Fatalf("ssh entry: %+v, want kind ssh with no config", p)
	}
	m, _ := pluginmeta.Verify(config.DBFile(home, p.ID), "", "")
	if m.Kind != "ssh" {
		t.Errorf("DB kind = %q, want ssh", m.Kind)
	}
}
