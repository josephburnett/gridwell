package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/pluginmeta"
)

// The open seam: a store opened the way the binary opens it reports the config
// id, not the bootstrap-minted system.plugin_uuid, which qualified links
// would never match.
func TestOpenVerifiedInjectsConfigIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "plugin.db")
	// Stamp the config id into the DB, as registration does.
	if err := pluginmeta.Create(dbPath, "k3x9m2q", "home"); err != nil {
		t.Fatal(err)
	}
	st, err := OpenVerified(dbPath, "k3x9m2q", "home")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.PluginUUID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "k3x9m2q" {
		t.Fatalf("PluginUUID = %q, want the verified config id %q (the bootstrap mint leaked through)", got, "k3x9m2q")
	}
}

// A mismatched config id must refuse to open: the DB's stored identity is
// authoritative and a wrong spawn must fail loudly rather than adopt.
func TestOpenVerifiedRefusesMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "plugin.db")
	if err := pluginmeta.Create(dbPath, "k3x9m2q", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVerified(dbPath, "z9z9z9z", "home"); err == nil {
		t.Fatal("OpenVerified accepted a config id that contradicts the stored identity")
	}
}
