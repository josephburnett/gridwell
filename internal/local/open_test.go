package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"

	"github.com/josephburnett/gridwell/internal/dbmeta"
)

// The open seam: a store opened the way the binary opens it speaks the config
// id, not the bootstrap-minted stored node uuid, which qualified ids would
// never match: a layout naming a visit by the config id holds it.
func TestOpenVerifiedInjectsConfigIdentity(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gridwell.db")
	// Stamp the config id into the DB, as registration does.
	if err := dbmeta.Create(dbPath, "k3x9m2q", "home"); err != nil {
		t.Fatal(err)
	}
	st, err := OpenVerified(dbPath, "k3x9m2q", "home")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scratch, err := st.ScratchGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.CreateURL(ctx, scratch, 0, 0, 1, 1, "https://example.com", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := st.CreateWorkspace(ctx, root, 0, 0, 1, 1, "ws", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout := `{"v":1,"root":{"pane":{"id":"p1","anchor":"k3x9m2q/` + scratch + `","path":["k3x9m2q/` + v.Id + `"]}},"focus":"p1"}`
	if _, err := st.WriteContent(ctx, ws.Id, ws.Version, []byte(layout)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTile(ctx, rpc.DeleteOf(v)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTile(ctx, v.Id); err != nil {
		t.Fatalf("a layout naming the visit by the config id did not hold it (the bootstrap mint leaked through): %v", err)
	}
}

// A mismatched config id must refuse to open: the DB's stored identity is
// authoritative and a wrong spawn must fail loudly rather than adopt.
func TestOpenVerifiedRefusesMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gridwell.db")
	if err := dbmeta.Create(dbPath, "k3x9m2q", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVerified(dbPath, "z9z9z9z", "home"); err == nil {
		t.Fatal("OpenVerified accepted a config id that contradicts the stored identity")
	}
}
