package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
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
	m, err := pluginmeta.Ensure(dbFile, "", "")
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

// TestRunInitSSHCarriesConfig proves init works for a transport kind too: the
// DB is created with kind=ssh and the --config pairs land in the entry.
func TestRunInitSSHCarriesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	rc := RunInit([]string{"--kind", "ssh", "--name", "remote", "--config", "host=example.com:22", "--config", "user=joe"})
	if rc != 0 {
		t.Fatalf("init ssh returned %d", rc)
	}
	cfg, err := config.Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := cfg.Plugins[0]
	if p.Kind != "ssh" || p.Config["host"] != "example.com:22" || p.Config["user"] != "joe" {
		t.Fatalf("ssh entry: %+v", p)
	}
	m, _ := pluginmeta.Ensure(config.DBFile(home, p.ID), "", "")
	if m.Kind != "ssh" {
		t.Errorf("DB kind = %q, want ssh", m.Kind)
	}
}
