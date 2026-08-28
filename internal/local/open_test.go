package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/pluginmeta"
)

// The #196 seam: the store-side SetPluginID existed and was unit-tested,
// but the binary never called it, so production identity fell back to the
// bootstrap-minted system.plugin_uuid and the boot scratch sweep compared
// workspace blob refs (which carry the CONFIG id) against the mint —
// never matching. This test crosses the open seam: a store opened the way
// the binary opens it must report the config id, not the mint.
func TestOpenVerifiedInjectsConfigIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "plugin.db")
	// gridwell init's registration step: stamp the config id into the DB.
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

// A mismatched config id must refuse to open — the DB's stored identity is
// authoritative and a wrong spawn must fail loudly, not adopt.
func TestOpenVerifiedRefusesMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "plugin.db")
	if err := pluginmeta.Create(dbPath, "k3x9m2q", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVerified(dbPath, "z9z9z9z", "home"); err == nil {
		t.Fatal("OpenVerified accepted a config id that contradicts the stored identity")
	}
}
